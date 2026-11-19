"""Assemble a private ARM64 hardware trial for the audited Biscuit loader.

No device tools are invoked. The unit header and native Alpine base are
explicit inputs; normal release/ARM32 validators remain unchanged.
"""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import shlex
import stat
import struct
import subprocess
import sys
import tarfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]
sys.path.insert(0, str(HERE.parent))
from kernel61.arm64_probe import BUSYBOX_SHA256, image_header, native_busybox, native_elf
from kernel61.assemble import rootfs_archive, sha
from kernel61.build import COMMIT, SOURCE_SHA256
from package import cpio

TEMPLATE_SHA256 = "a948a28b3871674b1adabd4a9f47b5d0e6f877f5af8acf6faba6f0a37cb50dda"
FPGA_SHA256 = "77a558bacdaaf9e343f02f2d74f27a5f2bb2dc8b6d66cc2499b60ed14ef62fe6"
STORE_ROOTFS = "/store/encore/arm64-probe/rootfs.tar.gz"
RUNTIME_ROOTFS = "/store/encore/arm64-runtime/rootfs.tar.gz"


def boot_image(template, image, dtb, ramdisk):
    if sha(template) != TEMPLATE_SHA256:
        raise ValueError("not the audited same-unit recovery header")
    header = image_header(image)
    # Executing Kaeru ab93750 + embedded LK 2fafb90c uses header.kernel_addr,
    # gzip inflate (28 MiB limit), and an appended root-version evt DTB.
    if header["text_offset"] != 0 or header["image_size"] > 0x1C00000:
        raise ValueError("Image exceeds audited loader footprint/offset contract")
    if (dtb[:4] != b"\xd0\x0d\xfe\xed" or len(dtb) < 40 or
            struct.unpack_from(">I", dtb, 4)[0] != len(dtb) or len(dtb) > 65536):
        raise ValueError("expected one bounded complete appended DTB")
    payload = gzip.compress(image, mtime=0) + dtb.ljust(65536, b"\0")
    ks, ka, rs, ra, second, sa, tags, page, version, word44 = struct.unpack_from("<10I", template, 8)
    if (len(template) != page or page != 2048 or ka != 0x40008000 or
            ra != 0x46000000 or tags != 0x48000000 or second or version):
        raise ValueError("audited header layout changed")
    # RAM 40000000..60000000; ATF 43000000..43030000; ramoops44400000..44600000.
    # Compressed input staging45000000 must not collide with the ramdisk46000000.
    if (0x40000000 + header["image_size"] > 0x43000000 or
            0x45000000 + len(payload) > ra or ra + len(ramdisk) > tags):
        raise ValueError("kernel/staging/initramfs memory ranges overlap")
    out = bytearray(template)
    command = (template[64:576].split(b"\0")[0] + template[608:1632].split(b"\0")[0]).decode("ascii")
    words = shlex.split(command)
    if words.count("bootopt=64S3,32N2,32N2") != 1:
        raise ValueError("expected explicit known ARM32 selector in template")
    words = [word for word in words if not word.startswith(
        ("skip_initramfs", "root=", "dm=", "init=", "rdinit=", "resume=", "rootflags=", "rootfstype="))]
    words[words.index("bootopt=64S3,32N2,32N2")] = "bootopt=64S3,32N2,64N2"
    command = " ".join(words + ["rdinit=/init"]).encode("ascii")
    if len(command) > 511:
        raise ValueError("sanitized command line exceeds the header field")
    out[64:576], out[608:1632] = command.ljust(512, b"\0"), bytes(1024)
    struct.pack_into("<II", out, 8, len(payload), 0x40000000)
    struct.pack_into("<I", out, 16, len(ramdisk))
    digest = hashlib.sha1()
    for data in (payload, ramdisk, b""):
        digest.update(data)
        digest.update(struct.pack("<I", len(data)))
    out[576:608] = digest.digest().ljust(32, b"\0")
    align = lambda data: data.ljust((len(data) + page - 1) // page * page, b"\0")
    result = bytes(out) + align(payload) + align(ramdisk)
    if len(result) > 16 << 20:
        raise ValueError("recovery image exceeds p12 (16 MiB)")
    return result


def repack(data, replacements):
    """Preserve entries; replace only named experimental payloads."""
    buf = io.BytesIO()
    with tarfile.open(fileobj=io.BytesIO(data)) as source, tarfile.open(fileobj=buf, mode="w") as target:
        for info in source:
            name = info.name.removeprefix("./").rstrip("/")
            if name in replacements:
                continue
            target.addfile(info, source.extractfile(info) if info.isfile() else None)
        for name, (content, mode) in replacements.items():
            info = tarfile.TarInfo(name)
            info.mode, info.size = mode, len(content)
            target.addfile(info, io.BytesIO(content))
    return gzip.compress(buf.getvalue(), mtime=0)


def initramfs(busybox, rootfs_hash, microphone_firmware, store_rootfs=STORE_ROOTFS):
    init = (HERE / "init").read_text().replace("@RESCUE_ONLY@", "0").replace(
        "@ROOTFS_SHA256@", rootfs_hash).replace(
        "/store/encore/kernel61-trial/rootfs.tar.gz", store_rootfs)
    files = [(name, stat.S_IFDIR | 0o755, b"") for name in
             ("bin", "sbin", "usr", "usr/local", "usr/local/sbin", "dev", "dev/pts",
              "proc", "sys", "run", "store", "newroot", "lib", "lib/firmware")]
    files += [("bin/busybox.static", stat.S_IFREG | 0o755, busybox),
              ("lib/firmware/i2s_to_spi_v34.bin", stat.S_IFREG | 0o644, microphone_firmware),
              ("bin/sh", stat.S_IFLNK | 0o777, b"busybox.static"),
              ("init", stat.S_IFREG | 0o755, init.encode()),
              ("usr/local/sbin/usb-rescue", stat.S_IFREG | 0o755, (HERE / "usb-rescue").read_bytes())]
    return cpio(files)


def runtime_files(directory, dual_acm=False):
    """Consume an audited private runtime without rebuilding its naming overlay."""
    manifest = json.loads((directory / "manifest.json").read_bytes())
    if (manifest["format"] != "encore-private-arm64-provision-runtime-v1" or
            manifest["architecture"] != "aarch64" or
            manifest["config"] != {"schema": 1, "name": "speaker-office"}):
        raise ValueError("expected the explicit pre-rename private runtime")
    for name, digest in manifest["inputs"].items():
        if sha((ROOT / name).read_bytes()) != digest:
            raise ValueError("private runtime source drift: " + name)
    for source, overlay in manifest["overlays"].items():
        if sha((directory / overlay["file"]).read_bytes()) != overlay["sha256"]:
            raise ValueError("private overlay drift: " + source)
    runtime = (directory / "encore-satellite").read_bytes()
    if manifest["files"]["encore-satellite"] != {"bytes": len(runtime), "sha256": sha(runtime)}:
        raise ValueError("private runtime binary drift")
    native_elf(runtime, static=True)
    # Mounts/firewall/firmware policy is shared with the hardware build; radio
    # ownership moves from the one-shot base into independent init services.
    boot = (HERE / "boot.sh").read_text().split("/usr/local/bin/wmtup", 1)[0]
    boot += ('cp /etc/encore/runtime-config.json /data/encore/config.json\n'
             'chmod 600 /data/encore/config.json\n'
             'touch /run/encore/base-ready\n')
    inittab = b"::respawn:/usr/local/sbin/usb-rescue\n"
    if dual_acm:
        inittab += b"::respawn:/usr/local/sbin/usb-console\n"
    for name in ("base", "radio", "wifi", "dhcp", "bluetooth", "network", "app"):
        inittab += ("::respawn:/usr/local/sbin/runtime-service " + name + "\n").encode()
    files = {
        "usr/local/bin/encore-satellite": (runtime, 0o755),
        "usr/local/sbin/runtime-service": ((HERE / "runtime-service").read_bytes(), 0o755),
        "usr/local/sbin/usb-console": ((HERE / "usb-console").read_bytes(), 0o755),
        "etc/encore/boot.sh": (boot.encode(), 0o755),
        "etc/encore/runtime-config.json": (b'{"schema":1,"name":"speaker-office"}\n', 0o600),
        "etc/encore/usb-ctty": (b"1\n", 0o644),
        "etc/encore/usb-consoles": (b"2\n" if dual_acm else b"1\n", 0o644),
        "etc/inittab": (inittab, 0o644),
    }
    return files, manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kernel-directory", type=Path, required=True)
    parser.add_argument("--base-rootfs", type=Path, required=True)
    parser.add_argument("--base-sha256", required=True)
    parser.add_argument("--busybox-apk", type=Path, required=True)
    parser.add_argument("--microphone-firmware", type=Path, required=True)
    parser.add_argument("--template", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--private-runtime", type=Path,
                        help="sealed pre-rename runtime directory; enables integrated init services")
    parser.add_argument("--dual-acm", action="store_true",
                        help="opt-in unqualified second ACM (runtime root only; rescue stays single)")
    args = parser.parse_args()
    if args.dual_acm and not args.private_runtime:
        parser.error("dual ACM requires an integrated private runtime")
    inputs = {path.name: sha(path.read_bytes()) for path in HERE.iterdir() if path.is_file()}
    service_sources = {str(path.relative_to(ROOT)): sha(path.read_bytes())
                       for path in HERE.parent.iterdir() if path.is_file()}
    base = args.base_rootfs.read_bytes()
    if sha(base) != args.base_sha256:
        parser.error("native base rootfs differs from supplied pin")
    busybox = native_busybox(args.busybox_apk.read_bytes())
    microphone_firmware = args.microphone_firmware.read_bytes()
    if sha(microphone_firmware) != FPGA_SHA256:
        parser.error("microphone firmware differs from the pinned kernel source's retained blob")
    kernel = args.kernel_directory
    record = json.loads((kernel / "manifest.json").read_bytes())
    if record["source_commit"] != COMMIT or record["source_sha256"] != SOURCE_SHA256:
        raise ValueError("kernel source pin differs from the ARM64 experiment")
    for name in ("Image", "biscuit-arm64-probe.dtb", "effective.config", "System.map"):
        data = (kernel / name).read_bytes()
        if record["files"][name] != {"bytes": len(data), "sha256": sha(data)}:
            raise ValueError("kernel artifact differs from manifest: " + name)
    for node, prop, expected in (("/", "version", "evt"), ("/memory", "reg", "0 40000000 0 20000000"),
                                  ("/reserved-memory/atf-reserved-memory@43000000", "reg", "0 43000000 0 30000"),
                                  ("/reserved-memory/ramoops@44400000", "reg", "0 44400000 0 200000")):
        actual = subprocess.check_output(["fdtget", "-ts" if prop == "version" else "-tx",
                                          str(kernel / "biscuit-arm64-probe.dtb"), node, prop], text=True).strip()
        if actual != expected:
            raise ValueError("DT loader/memory contract differs: " + node)
    config = (kernel / "effective.config").read_text().splitlines()
    for line in (HERE / "arm64-probe.fragment").read_text().splitlines():
        if line.startswith("CONFIG_"):
            key, value = line.split("=", 1)
            if (value == "n" and any(c.startswith(key + "=") for c in config)) or (value != "n" and line not in config):
                raise ValueError("kernel lacks required full-feature setting: " + line)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    sources = {str(path.relative_to(ROOT)): sha(path.read_bytes())
               for directory in ("devices/biscuit", "platforms", "core", "backends", "cmd/btbridge")
               for path in (ROOT / directory).rglob("*.go")}
    sources.update({name: sha((ROOT / name).read_bytes()) for name in ("go.mod", "go.sum")})
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="arm64")
    helpers = {}
    packages = [("wmtup", "devices/biscuit/cmd/wmtup"), ("btbridge", "cmd/btbridge")]
    for name, package in packages:
        subprocess.run(["go", "build", "-trimpath", "-tags=biscuit_kernel61", "-o", str(output / name), "./" + package],
                       cwd=ROOT, env=env, check=True)
        data = (output / name).read_bytes()
        native_elf(data, static=True)
        helpers["usr/local/bin/" + name] = data, 0o755
    helpers["lib/firmware/i2s_to_spi_v34.bin"] = microphone_firmware, 0o644
    with tarfile.open(fileobj=io.BytesIO(base)) as archive:
        for info in archive:
            if info.isfile():
                data = archive.extractfile(info).read()
                if data.startswith(b"\x7fELF"):
                    native_elf(data)
        inventory = archive.extractfile("./lib/apk/db/installed").read()
        release = archive.extractfile("./etc/alpine-release").read().decode().strip()
    if not release.startswith("3.22."):
        raise ValueError("hardware experiment must retain Alpine 3.22")
    base = repack(base, helpers)
    rootfs, embedded_busybox = rootfs_archive(base, None, record, elf_check=native_elf)
    if sha(embedded_busybox) != BUSYBOX_SHA256:
        raise ValueError("base and initramfs BusyBox differ")
    rootfs = repack(rootfs, {
        "sbin/init": ((HERE / "arm64-probe-init").read_bytes(), 0o755),
        "etc/inittab": (b"::respawn:/usr/local/sbin/usb-rescue\n", 0o644),
        "etc/passwd": (b"root:x:0:0:root:/root:/bin/sh\nmessagebus:x:100:101:messagebus:/run/dbus:/sbin/nologin\n", 0o644),
        "etc/group": (b"root:x:0:root\nmessagebus:x:101:messagebus\n", 0o644),
        "etc/shadow": (b"root:!:0:0:99999:7:::\n", 0o600),
        "etc/resolv.conf": (b"", 0o644),
    })
    runtime_manifest = None
    store_rootfs = STORE_ROOTFS
    if args.private_runtime:
        replacements, runtime_manifest = runtime_files(args.private_runtime, args.dual_acm)
        rootfs = repack(rootfs, replacements)
        store_rootfs = RUNTIME_ROOTFS
        with tarfile.open(fileobj=io.BytesIO(rootfs)) as archive:
            if sum(info.size for info in archive if info.isfile()) > 96 << 20:
                raise ValueError("integrated rootfs exceeds 96 MiB file budget")
    ramdisk = initramfs(busybox, sha(rootfs), microphone_firmware, store_rootfs)
    image = boot_image(args.template.read_bytes(), (kernel / "Image").read_bytes(),
                       (kernel / "biscuit-arm64-probe.dtb").read_bytes(), ramdisk)
    for name, data in (("rootfs.tar.gz", rootfs), ("initramfs.gz", ramdisk),
                       ("recovery.img", image), ("apk-installed.txt", inventory),
                       ("i2s_to_spi_v34.bin", microphone_firmware)):
        (output / name).write_bytes(data)
    if any(sha((ROOT / name).read_bytes()) != digest for name, digest in sources.items()):
        raise ValueError("runtime sources changed during compilation")
    if any(sha((HERE / name).read_bytes()) != digest for name, digest in inputs.items()):
        raise ValueError("packaging sources changed during assembly")
    if any(sha((ROOT / name).read_bytes()) != digest for name, digest in service_sources.items()):
        raise ValueError("service sources changed during assembly")
    manifest = {"format": ("encore-biscuit-arm64-runtime-trial-v1" if runtime_manifest else
                           "encore-biscuit-arm64-hardware-trial-v1"), "release": False,
                "architecture": "aarch64", "distribution": "Alpine " + release,
                "base_sha256": args.base_sha256, "template_sha256": TEMPLATE_SHA256,
                "microphone_firmware": {"sha256": FPGA_SHA256, "source": "pinned Linux source firmware/i2s_to_spi_v34.bin",
                                        "redistribution": "unresolved license/source grant; private trial only"},
                "kernel": record, "runtime_sources": sources, "store_rootfs": store_rootfs,
                "private_runtime": runtime_manifest, "usb_ports": 2 if args.dual_acm else 1,
                "inputs": inputs, "service_sources": service_sources,
                "policy": ("RAM root; store ro,noload; independent USB; single-attempt init services; "
                           "pre-rename identity; no saved credentials; no automatic diagnostic PCM opens"
                           if runtime_manifest else
                           "RAM root; store ro,noload; USB rescue only"),
                "unqualified": ["integrated physical boot/services and optional dual ACM", "SMP/CPU frequency/idle",
                                "RF/association/Improv", "acoustic playback/microphone signal", "thermal policy"],
                "files": {path.name: {"bytes": path.stat().st_size, "sha256": sha(path.read_bytes())}
                          for path in output.iterdir() if path.is_file()}}
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({"output": str(output), "files": manifest["files"]}, indent=2))


if __name__ == "__main__":
    main()
