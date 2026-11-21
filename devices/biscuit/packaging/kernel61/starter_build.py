"""Assemble the held Encore starter and matching runtime archive."""

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
from kernel61.rootfs import arm64_bluetooth
from kernel61.starter_install import BASE_SHA, CA_SHA, DTB_SHA, FORMAT, IMAGE_SHA, boot_compatibility, load_bundle, native_identity, starter_version
from package import legacy_ssh_member


def native_payloads(output, image, dtb, header, busybox, fpga, runtime, verifier, core, revision, lock_sha):
    # Compatibility describes the on-disk protocol. Kernel and DTB remain
    # replaceable payloads and are deliberately not part of this identity.
    identity = native_identity(core, revision)
    members = {"rootfs.tar.gz": runtime}
    for slot in ("a", "b"):
        ramdisk = initramfs(busybox, sha(runtime), fpga, native=(slot, identity, verifier))
        members[f"boot_{slot}.img"] = boot_image(header, image, dtb, ramdisk)
    # The OTA uses its signed feed as its envelope. The first-install seed has
    # its own manifest (and cannot be treated as a signed release by itself).
    seed = {**identity, "input_lock_sha256": lock_sha, "kernel_image_sha256": sha(image),
            "dtb_sha256": sha(dtb), "files": {name: sha(data) for name, data in members.items()}}
    for name, files in (("native-ota.zip", members),
                        ("native-seed.zip", {"manifest.json": json.dumps(seed, sort_keys=True).encode(),
                                             "header.bin": header, **members})):
        with zipfile.ZipFile(output / name, "x") as out:
            for file, data in files.items():
                out.writestr(zipfile.ZipInfo(file), data)


def starter_rootfs(base, binaries, version=None, ca=None, *, locked=False):
    files = {
        **{"usr/local/bin/" + name: (data, 0o755) for name, data in binaries.items()},
        "etc/encore-base": ((FORMAT + "\n").encode(), 0o644),
    }
    if version is not None:
        files["etc/encore-arm64-version"] = ((version + "\n").encode(), 0o644)
        files["etc/ssl/certs/ca-certificates.crt"] = (ca, 0o644)
    if locked:
        files["sbin/init"] = ((HERE / "arm64-probe-init").read_bytes(), 0o755)
        files["usr/local/sbin/encore-bluetooth"] = (arm64_bluetooth(), 0o755)
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
test -s /data/encore/config.json && test ! -L /data/encore/config.json &&
  test "$(stat -c '%u:%a' /data/encore/config.json)" = 0:600 || exit 1
key=/data/encore/noise.psk
if test -e "$key" || test -L "$key"; then
  test ! -L "$key" && test -f "$key" && test -s "$key" &&
    test "$(stat -c '%u:%a' "$key")" = 0:600 || exit 1
fi''')
    startup = startup.replace('fw=/store/encore/firmware',
                              '/usr/local/sbin/encore-stock-firmware load\nfw=/run/encore/firmware')
    files["etc/encore/boot.sh"] = ((startup + "touch /run/encore/base-ready\n").encode(), 0o755)
    inittab = "::respawn:/usr/local/sbin/usb-rescue\n"
    for service in ("base", "radio", "wifi", "dhcp", "time-sync", "bluetooth", "network", "enrollment", "app", "health"):
        inittab += f"::respawn:/usr/local/sbin/runtime-service {service}\n"
    files["etc/inittab"] = (inittab.encode(), 0o644)
    # The private diagnostic base contains no device data. Drop the diagnostic
    # entry points and its fixed naming overlay rather than ship hidden extras.
    discard = {"etc/encore/runtime-config.json", "etc/encore/usb-consoles", "usr/local/bin/pcm-silence-check",
               "usr/local/bin/hwprobe", "usr/local/bin/encore-diagnostic",
               "usr/local/sbin/arm64-hardware-probe", "usr/local/sbin/usb-console", "usr/local/bin/encore-sendspin"}
    clean = io.BytesIO()
    with tarfile.open(fileobj=io.BytesIO(base), mode="r:gz") as src, tarfile.open(fileobj=clean, mode="w") as dst:
        for member in src:
            member.name = member.name.removeprefix("./")
            # Regenerate the PAX path too, or it overrides the normalized name.
            member.pax_headers.pop("path", None)
            if member.name not in discard and not legacy_ssh_member(member.name):
                dst.addfile(member, src.extractfile(member) if member.isfile() else None)
    return repack(clean.getvalue(), files)


def source_hashes():
    paths = {p for directory in ("devices/biscuit", "platforms", "core", "backends", "internal", "cmd/btbridge")
             for p in (ROOT / directory).rglob("*.go")}
    paths.update(p for directory in (HERE, HERE.parent) for p in directory.iterdir() if p.is_file())
    paths.update((ROOT / "go.mod", ROOT / "go.sum"))
    return {str(p.relative_to(ROOT)): sha(p.read_bytes()) for p in sorted(paths)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sealed-base", type=Path, required=True)
    parser.add_argument("--revision", type=int, required=True)
    parser.add_argument("--core-version", required=True, help="complete core SemVer, including prerelease when applicable")
    parser.add_argument("--ca-bundle", type=Path, required=True, help="pinned Alpine Mozilla CA bundle")
    parser.add_argument("--output", type=Path, required=True, help="new private build directory")
    parser.add_argument("--input-lock-sha256", help="digest from the same-commit external-input builder")
    parser.add_argument("--native-payloads", action="store_true", help="also produce unsigned native A/B seed and OTA candidates")
    args = parser.parse_args()
    if args.native_payloads and not args.input_lock_sha256:
        parser.error("native A/B requires the same-commit external-input build")
    version = starter_version(args.core_version, args.revision)
    ca = args.ca_bundle.read_bytes()
    if sha(ca) != CA_SHA:
        parser.error("expected the pinned Alpine ca-certificates-bundle")
    base_dir = args.sealed_base
    base, image, dtb, header = ((base_dir / name).read_bytes() for name in
                                 ("rootfs.tar.gz", "Image", "biscuit-arm64-probe.dtb", "audited-header.bin"))
    if not args.input_lock_sha256 and (sha(base), sha(image), sha(dtb)) != (BASE_SHA, IMAGE_SHA, DTB_SHA):
        parser.error("starter requires the exact retained audiofix1 rootfs and stpfix1 Image/DTB")
    with tarfile.open(fileobj=io.BytesIO(base), mode="r:gz") as archive:
        busybox = archive.extractfile("bin/busybox.static").read()
        fpga = archive.extractfile("lib/firmware/i2s_to_spi_v34.bin").read()
        alpine = archive.extractfile("etc/alpine-release").read().decode().strip()

    sources = source_hashes()
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
    verifier = output / "arm64-verify"
    subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-o", str(verifier),
                    "./devices/biscuit/cmd/arm64-verify"], cwd=ROOT, env=env, check=True)
    binaries["arm64-verify"] = verifier.read_bytes()
    rootfs = starter_rootfs(base, binaries, version, ca, locked=bool(args.input_lock_sha256))
    compatibility = boot_compatibility(image, dtb) if args.input_lock_sha256 else BASE_SHA
    signed = (args.core_version, args.revision, binaries["arm64-verify"])
    if args.input_lock_sha256:
        signed += (compatibility,)
    ramdisk = initramfs(busybox, sha(rootfs), fpga, starter=True,
                        signed=signed, recovery_rearm=True)
    recovery = boot_image(header, image, dtb, ramdisk)
    if any(sha((ROOT / path).read_bytes()) != digest for path, digest in sources.items()):
        raise RuntimeError("source changed during starter assembly")

    members = {"rootfs.tar.gz": rootfs, "recovery.img": recovery, "header.bin": header}
    manifest = {"format": FORMAT, "backend": "incubator", "package_revision": args.revision,
                "core_version": args.core_version,
                "api_key_provisioning": True,
                "recovery_rearm": True,
                "architecture": "aarch64", "distribution": "Alpine " + alpine, "release": False,
                "retained_base_sha256": compatibility, "kernel_image_sha256": sha(image), "dtb_sha256": sha(dtb),
                "go": subprocess.check_output(["go", "version"], text=True).strip(), "sources": sources,
                "files": {name: sha(data) for name, data in members.items()}}
    if args.input_lock_sha256:
        manifest["input_lock_sha256"] = args.input_lock_sha256
    bundle = output / "starter.zip"
    with zipfile.ZipFile(bundle, "x") as out:
        for name, data in {"manifest.json": json.dumps(manifest, sort_keys=True).encode(), **members}.items():
            out.writestr(zipfile.ZipInfo(name), data)
    digest = sha(bundle.read_bytes())
    _, verified_rootfs, _ = load_bundle(bundle, digest)
    runtime = output / "rootfs.tar.gz"
    runtime.write_bytes(verified_rootfs)
    if args.native_payloads:
        native_payloads(output, image, dtb, header, busybox, fpga, verified_rootfs,
                        binaries["arm64-verify"], args.core_version, args.revision, args.input_lock_sha256)
    print(f"{digest}  {bundle}")
    print(f"{sha(verified_rootfs)}  {runtime}")


if __name__ == "__main__":
    main()
