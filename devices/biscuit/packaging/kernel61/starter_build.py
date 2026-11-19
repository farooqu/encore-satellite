"""Assemble the held Encore starter from the sealed rootfs and stpfix1 kernel."""

import argparse
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from kernel61.arm64_assemble import HERE, ROOT, boot_image, initramfs, repack, sha
from kernel61.starter_install import DTB_SHA, FORMAT, IMAGE_SHA, load_bundle

BASE_SHA = "a8386b120851db6bf30cd6a2a354d605d3c76ea83e62b18c5549079a9b373fbe"


def starter_rootfs(base, binaries):
    files = {
        **{"usr/local/bin/" + name: (data, 0o755) for name, data in binaries.items()},
        "etc/encore-base": ((FORMAT + "\n").encode(), 0o644),
    }
    for source, target in (("runtime-service", "runtime-service"), ("stock-firmware", "encore-stock-firmware"),
                           ("usb-rescue", "usb-rescue"), ("../run", "encore-run"),
                           ("../slotctl", "slotctl"), ("../health", "encore-health"),
                           ("../network", "encore-network"), ("../firewall", "encore-firewall")):
        files["usr/local/sbin/" + target] = ((HERE / source).read_bytes(), 0o755)
    files["etc/encore/dhcp.sh"] = ((HERE.parent / "dhcp.sh").read_bytes(), 0o755)
    startup = (HERE / "boot.sh").read_text().split('/usr/local/bin/wmtup -patches', 1)[0]
    startup = startup.replace('mount -t tmpfs -o mode=0700,size=16m tmpfs /data',
                              '''mount -t ext4 -o noatime,nosuid,nodev,noexec /dev/mmcblk0p16 /data
test ! -L /data/encore
test "$(stat -c '%u:%a' /data/encore)" = 0:700
for state in config.json noise.psk; do
  file=/data/encore/$state
  test -s "$file" && test ! -L "$file" && test "$(stat -c '%u:%a' "$file")" = 0:600 || exit 1
done''')
    startup = startup.replace('fw=/store/encore/firmware',
                              '/usr/local/sbin/encore-stock-firmware load\nfw=/run/encore/firmware')
    files["etc/encore/boot.sh"] = ((startup + "touch /run/encore/base-ready\n").encode(), 0o755)
    inittab = "::respawn:/usr/local/sbin/usb-rescue\n"
    for service in ("base", "radio", "wifi", "dhcp", "bluetooth", "network", "enrollment", "app", "health"):
        inittab += f"::respawn:/usr/local/sbin/runtime-service {service}\n"
    files["etc/inittab"] = (inittab.encode(), 0o644)
    # The private diagnostic base contains no device data. Drop the diagnostic
    # entry points and its fixed naming overlay rather than ship hidden extras.
    discard = {"etc/encore/runtime-config.json", "etc/encore/usb-consoles", "usr/local/bin/pcm-silence-check",
               "usr/local/bin/hwprobe", "usr/local/bin/encore-diagnostic",
               "usr/local/sbin/arm64-hardware-probe", "usr/local/sbin/usb-console",
               "usr/local/sbin/encore-ssh", "usr/local/bin/encore-sendspin"}
    clean = io.BytesIO()
    with tarfile.open(fileobj=io.BytesIO(base), mode="r:gz") as src, tarfile.open(fileobj=clean, mode="w") as dst:
        for member in src:
            member.name = member.name.removeprefix("./")
            # Regenerate the PAX path too, or it overrides the normalized name.
            member.pax_headers.pop("path", None)
            if member.name not in discard:
                dst.addfile(member, src.extractfile(member) if member.isfile() else None)
    return repack(clean.getvalue(), files)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sealed-base", type=Path, required=True)
    parser.add_argument("--revision", type=int, required=True)
    parser.add_argument("--output", type=Path, required=True, help="new private build directory")
    args = parser.parse_args()
    if args.revision < 1:
        parser.error("revision must be positive")
    base_dir = args.sealed_base
    base, image, dtb, header = ((base_dir / name).read_bytes() for name in
                                 ("rootfs.tar.gz", "Image", "biscuit-arm64-probe.dtb", "audited-header.bin"))
    if (sha(base), sha(image), sha(dtb)) != (BASE_SHA, IMAGE_SHA, DTB_SHA):
        parser.error("starter requires the exact retained audiofix1 rootfs and stpfix1 Image/DTB")
    with tarfile.open(fileobj=io.BytesIO(base), mode="r:gz") as archive:
        busybox = archive.extractfile("bin/busybox.static").read()
        fpga = archive.extractfile("lib/firmware/i2s_to_spi_v34.bin").read()
        alpine = archive.extractfile("etc/alpine-release").read().decode().strip()

    paths = {p for directory in ("devices/biscuit", "platforms", "core", "backends", "internal", "cmd/btbridge")
             for p in (ROOT / directory).rglob("*.go")}
    paths.update(p for directory in (HERE, HERE.parent) for p in directory.iterdir() if p.is_file())
    paths.update((ROOT / "go.mod", ROOT / "go.sum"))
    sources = {str(p.relative_to(ROOT)): sha(p.read_bytes()) for p in sorted(paths)}
    output = args.output.resolve()
    output.mkdir(mode=0o700, parents=True)
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="arm64")
    binaries = {}
    for name, package in (("encore-satellite", "./devices/biscuit/cmd/encore-satellite"),
                          ("wmtup", "./devices/biscuit/cmd/wmtup"), ("btbridge", "./cmd/btbridge")):
        path = output / name
        subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-o", str(path), package],
                       cwd=ROOT, env=env, check=True)
        binaries[name] = path.read_bytes()
    rootfs = starter_rootfs(base, binaries)
    ramdisk = initramfs(busybox, sha(rootfs), fpga, starter=True)
    recovery = boot_image(header, image, dtb, ramdisk)
    if any(sha((ROOT / path).read_bytes()) != digest for path, digest in sources.items()):
        raise RuntimeError("source changed during starter assembly")

    members = {"rootfs.tar.gz": rootfs, "recovery.img": recovery, "header.bin": header}
    manifest = {"format": FORMAT, "backend": "incubator", "package_revision": args.revision,
                "architecture": "aarch64", "distribution": "Alpine " + alpine, "release": False,
                "retained_base_sha256": BASE_SHA, "kernel_image_sha256": IMAGE_SHA, "dtb_sha256": DTB_SHA,
                "go": subprocess.check_output(["go", "version"], text=True).strip(), "sources": sources,
                "files": {name: sha(data) for name, data in members.items()}}
    bundle = output / "starter.zip"
    with zipfile.ZipFile(bundle, "x") as out:
        for name, data in {"manifest.json": json.dumps(manifest, sort_keys=True).encode(), **members}.items():
            out.writestr(zipfile.ZipInfo(name), data)
    digest = sha(bundle.read_bytes())
    load_bundle(bundle, digest)
    print(f"{digest}  {bundle}")


if __name__ == "__main__":
    main()
