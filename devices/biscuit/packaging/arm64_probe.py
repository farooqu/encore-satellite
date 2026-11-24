"""Build the single-core ARM64 full-hardware kernel, never invoke a device.

Output is a raw Image and separate DTB. arm64_assemble.py supplies the native
userspace and the explicitly audited unit-specific Android boot envelope.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tarfile

HERE = Path(__file__).resolve().parent

COMMIT = "5bc811d3cd7091e8d70f9faa0bc12c5cf4e77ae0"
SOURCE_SHA256 = "2d763595ea9299add581aad7bac95749b82b0705ff9c992ddf1ed16d52747087"
BUSYBOX_APK_SHA256 = "ee469aee2958feffd7f64dd96655704025ff60af614f77a1d7323dc237c34da2"
BUSYBOX_SHA256 = "a6bb1e6e349cbdfa690a78cd0017413c5446bdae02ee003ceb682d4ae1a004ff"


def sha(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def image_header(image):
    """ARM64 booting.rst header; deliberately reject ARM32/compressed images."""
    if len(image) < 64 or image[56:60] != b"ARM\x64":
        raise ValueError("expected an uncompressed ARM64 Image")
    offset, size, flags = struct.unpack_from("<3Q", image, 8)
    if not size or size < len(image) or flags & 1 or (flags >> 1) & 3 != 1:
        raise ValueError("expected bounded little-endian 4-KiB ARM64 Image")
    return {"text_offset": offset, "image_size": size, "flags": flags,
            "alignment": 2 << 20}


def native_elf(data, static=False):
    if (len(data) < 64 or data[:6] != b"\x7fELF\x02\x01" or
            struct.unpack_from("<H", data, 18)[0] != 183):
        raise ValueError("expected a little-endian AArch64 ELF")
    if static:
        offset = struct.unpack_from("<Q", data, 32)[0]
        size, count = struct.unpack_from("<HH", data, 54)
        if size < 56 or offset + size * count > len(data):
            raise ValueError("invalid AArch64 ELF program headers")
        if any(struct.unpack_from("<I", data, offset + size * i)[0] == 3 for i in range(count)):
            raise ValueError("expected statically linked AArch64 helper")


def native_busybox(apk):
    if hashlib.sha256(apk).hexdigest() != BUSYBOX_APK_SHA256:
        raise ValueError("BusyBox APK differs from retained AArch64 package")
    with tarfile.open(fileobj=io.BytesIO(apk)) as archive:
        busybox = archive.extractfile("bin/busybox.static").read()
    native_elf(busybox, static=True)
    if hashlib.sha256(busybox).hexdigest() != BUSYBOX_SHA256:
        raise ValueError("expected exact static AArch64 BusyBox")
    return busybox


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-archive", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--jobs", type=int, default=6)
    args = parser.parse_args()
    if args.jobs < 1 or sha(args.source_archive) != SOURCE_SHA256:
        parser.error("positive jobs and exact pinned kernel source required")
    compiler = subprocess.check_output(
        ["aarch64-linux-gnu-gcc", "-dumpfullversion"], text=True).strip()
    if compiler != "14.2.0":
        parser.error("probe requires AArch64 GCC 14.2.0")
    patches = ("capture.patch", "arm64-probe.patch", "arm64-connectivity.patch",
               "playback-sram.patch", "stp-read.patch", "fpga-zero-padding.patch",
               "le-smp-ready.patch")
    inputs = {name: sha(HERE / name) for name in
              ("arm64_probe.py", "arm64-probe.fragment", "arm64-probe.dts",
               "biscuit.dts", "encore.fragment", *patches)}
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    env = dict(os.environ, ARCH="arm64", CROSS_COMPILE="aarch64-linux-gnu-",
               SOURCE_DATE_EPOCH="1794783774", KBUILD_BUILD_VERSION="1",
               KBUILD_BUILD_USER="encore", KBUILD_BUILD_HOST="arm64-audiofix1",
               KBUILD_BUILD_TIMESTAMP=datetime.fromtimestamp(
                   1794783774, timezone.utc).strftime("%a %b %d %H:%M:%S UTC %Y"))
    source, out = output / "src", output / "out"
    with tarfile.open(args.source_archive) as archive:
        archive.extractall(output / "unpack", filter="data")
    roots = list((output / "unpack").iterdir())
    if len(roots) != 1 or not roots[0].is_dir():
        raise ValueError("unexpected source archive root")
    roots[0].rename(source)
    (output / "unpack").rmdir()
    for name in patches:
        cwd = source
        if name == "arm64-connectivity.patch":
            cwd /= "drivers/misc/mediatek/mt8163-connectivity"
        with (HERE / name).open("rb") as patch:
            subprocess.run(["patch", "--batch", "--fuzz=0", "-p1"], cwd=cwd,
                           stdin=patch, check=True)
    # The pinned upstream archive is a build input, not the corresponding
    # source to redistribute for the zero-padded kernel.
    (source / "sound/soc/mediatek/mt8163-amzn-spi/amzn-fpga-oracle-tail.h").unlink()
    out.mkdir()
    shutil.copyfile(source / "arch/arm/configs/mt8163_arm32_defconfig", out / ".config")
    subprocess.run(["bash", str(source / "scripts/kconfig/merge_config.sh"), "-m",
                    "-O", str(out), str(out / ".config"),
                    str(HERE / "encore.fragment"),
                    str(HERE / "arm64-probe.fragment")], env=env, check=True)
    make = ["make", "-C", str(source), f"O={out}"]
    subprocess.run(make + ["olddefconfig"], env=env, check=True)
    config = set((out / ".config").read_text().splitlines())
    for line in (HERE / "arm64-probe.fragment").read_text().splitlines():
        if line.startswith("CONFIG_"):
            key, value = line.split("=", 1)
            # Kconfig omits symbols hidden behind a disabled parent. Both
            # absence and an explicit "not set" mean disabled, never y/m.
            if ((value == "n" and any(c.startswith(key + "=") for c in config)) or
                    (value != "n" and line not in config)):
                raise ValueError("probe config requirement lost: " + line)
    if "CONFIG_ARM64=y" not in config:
        raise ValueError("kernel is not ARM64")
    subprocess.run(make + [f"-j{args.jobs}", "Image"], env=env, check=True)
    dts = source / "arch/arm/boot/dts"
    shutil.copyfile(HERE / "biscuit.dts", dts / "encore-biscuit.dts")
    preprocessed = output / "probe.dts"
    with preprocessed.open("wb") as stream:
        subprocess.run(["aarch64-linux-gnu-gcc", "-E", "-P", "-x", "assembler-with-cpp",
                        "-nostdinc", "-undef", "-D__DTS__", "-I", str(source / "include"),
                        "-I", str(dts), str(HERE / "arm64-probe.dts")], stdout=stream, check=True)
    subprocess.run([str(out / "scripts/dtc/dtc"), "-I", "dts", "-O", "dtb", "-o",
                    str(output / "biscuit-arm64-probe.dtb"), str(preprocessed)], check=True)
    for original, name in ((out / "arch/arm64/boot/Image", "Image"),
                           (out / ".config", "effective.config"),
                           (out / "System.map", "System.map")):
        shutil.copyfile(original, output / name)
    subprocess.run([sys.executable, "-B", str(source / "libreecho/run_contract_tests.py")], check=True)
    header = image_header((output / "Image").read_bytes())
    if any(sha(HERE / name) != digest for name, digest in inputs.items()):
        raise ValueError("probe inputs changed during build")
    manifest = {"format": "encore-biscuit-arm64-hardware-probe-v1", "installable": False,
                "status": "NON_RELEASE_NOT_FOR_FLASHING", "architecture": "aarch64",
                "source_commit": COMMIT, "source_sha256": SOURCE_SHA256,
                "compiler": compiler, "inputs": inputs, "image": header,
                "connectivity_patch_origin": "LibreEcho-mt8163-connectivity c96a44de2474d9f9c086a40156c132ab81772e82",
                "boot_policy": "single-core; full board drivers; native RAM userspace assembled separately",
                "files": {p.name: {"bytes": p.stat().st_size, "sha256": sha(p)}
                          for p in output.iterdir() if p.is_file()}}
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print("ARM64 HARDWARE KERNEL; assemble the audited boot envelope separately:", output)


if __name__ == "__main__":
    main()
