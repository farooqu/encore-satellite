"""Build a private non-installable 6.1 experiment. Never calls a device tool."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import tarfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]
COMMIT = "5bc811d3cd7091e8d70f9faa0bc12c5cf4e77ae0"
SOURCE_SHA256 = "2d763595ea9299add581aad7bac95749b82b0705ff9c992ddf1ed16d52747087"


def sha(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def validate_payload(kernel, dtb, combined):
    if len(kernel) < 48 or struct.unpack_from("<I", kernel, 36)[0] != 0x016F2818:
        raise ValueError("expected ARM zImage")
    if not 40 <= len(dtb) <= 65536 or dtb[:4] != bytes.fromhex("d00dfeed"):
        raise ValueError("expected one DTB fitting a 64 KiB slot")
    if struct.unpack_from(">I", dtb, 4)[0] != len(dtb):
        raise ValueError("truncated or trailing DTB bytes")
    if combined != kernel + dtb.ljust(65536, b"\0"):
        raise ValueError("expected zImage plus exactly one padded DTB")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-archive", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True,
                        help="new directory; existing outputs are never overwritten")
    parser.add_argument("--jobs", type=int, default=2)
    args = parser.parse_args()
    if args.jobs < 1:
        parser.error("jobs must be positive")
    if sha(args.source_archive) != SOURCE_SHA256:
        parser.error("source archive differs from the pinned LibreEcho snapshot")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    inputs = {p.name: sha(p) for p in sorted(HERE.iterdir()) if p.is_file()}
    runtime_sources = {str(p.relative_to(ROOT)): sha(p)
                       for directory in ("core", "backends", "platforms", str(HERE.parents[1].relative_to(ROOT)))
                       for p in sorted((ROOT / directory).rglob("*.go"))}
    runtime_sources.update({name: sha(ROOT / name) for name in ("go.mod", "go.sum")})
    env = dict(os.environ, ARCH="arm", CROSS_COMPILE="arm-linux-gnueabihf-",
               SOURCE_DATE_EPOCH="1794783774", KBUILD_BUILD_VERSION="1",
               KBUILD_BUILD_USER="encore", KBUILD_BUILD_HOST="nonrelease-compat",
               KBUILD_BUILD_TIMESTAMP=datetime.fromtimestamp(1794783774, timezone.utc).strftime("%a %b %d %H:%M:%S UTC %Y"))

    def run(*command, **kwargs):
        subprocess.run(command, check=True, env=env, **kwargs)

    compiler = subprocess.check_output(
        ["arm-linux-gnueabihf-gcc", "-dumpfullversion"], text=True).strip()
    if compiler != "14.2.0":
        parser.error("this experiment requires ARM hard-float GCC 14.2.0")
    with tarfile.open(args.source_archive) as archive:
        archive.extractall(output / "unpack", filter="data")
    roots = list((output / "unpack").iterdir())
    if len(roots) != 1 or not roots[0].is_dir():
        raise ValueError("unexpected source archive root")
    source = output / "src"
    roots[0].rename(source)
    (output / "unpack").rmdir()
    with (HERE / "capture.patch").open("rb") as patch:
        run("patch", "--batch", "--fuzz=0", "-p1", cwd=source, stdin=patch)
    dts = source / "arch/arm/boot/dts"
    shutil.copyfile(HERE / "biscuit.dts", dts / "encore-biscuit.dts")
    out = output / "out"

    def make(*targets):
        run("make", "-C", str(source), f"O={out}", *targets)

    make("mt8163_arm32_defconfig")
    run("bash", str(source / "scripts/kconfig/merge_config.sh"), "-m", "-O",
        str(out), str(out / ".config"), str(HERE / "encore.fragment"))
    make("olddefconfig")
    config = (out / ".config").read_text().splitlines()
    for line in (HERE / "encore.fragment").read_text().splitlines():
        if line.startswith("CONFIG_"):
            key, value = line.split("=", 1)
            expected = f"# {key} is not set" if value == "n" else line
            if expected not in config:
                raise ValueError("config requirement lost: " + expected)
    make(f"-j{args.jobs}", "zImage", "encore-biscuit.dtb")
    for src, dest in ((out / "arch/arm/boot/zImage", "zImage"),
                      (out / "arch/arm/boot/dts/encore-biscuit.dtb", "biscuit.dtb"),
                      (out / ".config", "effective.config"),
                      (out / "System.map", "System.map")):
        shutil.copyfile(src, output / dest)
    kernel, dtb = (output / "zImage").read_bytes(), (output / "biscuit.dtb").read_bytes()
    combined = kernel + dtb.ljust(65536, b"\0")
    validate_payload(kernel, dtb, combined)
    (output / "zImage-dtb").write_bytes(combined)
    run("python3", str(ROOT / "tools/check_ble_kernel.py"), str(output / "effective.config"))
    goenv = dict(env, CGO_ENABLED="0", GOOS="linux", GOARCH="arm", GOARM="7")
    subprocess.run(["go", "build", "-trimpath", "-tags=biscuit_kernel61", "-o",
                    str(output / "encore-satellite"), "./" + str(HERE.parents[1].relative_to(ROOT)) +
                    "/cmd/encore-satellite"], cwd=ROOT, env=goenv, check=True)
    shutil.copyfile(HERE / "usb-rescue", output / "usb-rescue")
    if any(sha(HERE / name) != digest for name, digest in inputs.items()) or any(
            sha(ROOT / name) != digest for name, digest in runtime_sources.items()):
        raise ValueError("build inputs changed during compilation; refusing manifest")
    manifest = {
        "status": "NON_RELEASE_NOT_FOR_FLASHING", "installable": False,
        "kernel_source": "https://github.com/aslater3/LibreEcho-Linux",
        "kernel_commit": COMMIT, "source_sha256": SOURCE_SHA256,
        "compiler": compiler, "runtime_build_tag": "biscuit_kernel61",
        "inputs": inputs, "runtime_sources": runtime_sources,
        "files": {p.name: {"bytes": p.stat().st_size, "sha256": sha(p)}
                  for p in sorted(output.iterdir()) if p.is_file()},
        "encore_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "encore_diff_sha256": hashlib.sha256(subprocess.check_output(
            ["git", "diff", "HEAD"], cwd=ROOT)).hexdigest(),
        "unqualified": ["boot envelope/rescue", "thermal policy and sensor population",
                        "amplifier/mux and acoustic safety", "privacy hardware",
                        "Wi-Fi/BLE/Sendspin coexistence and identity", "rollback/power loss"],
        "firmware_inventory": "FPGA bitstream and recovered oracle-tail grant/source review unresolved",
    }
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print("NON_RELEASE_NOT_FOR_FLASHING:", output)


if __name__ == "__main__":
    main()
