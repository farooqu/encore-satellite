"""Project-owned kernel artifact provenance, shared by build and packaging."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
FILES = ("zImage-dtb", "zImage-dtb.config", "zImage-dtb.System.map", "source.tar.gz")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def inputs():
    lock = json.loads((HERE / "kernel-inputs.json").read_bytes())
    return {"lock": lock, "builder_sha256": digest((HERE / "kernel.sh").read_bytes())}


def identity():
    return digest(json.dumps(inputs(), sort_keys=True).encode())


def validate(record, image):
    if (not isinstance(record, dict)
            or record.get("schema") != 1 or record.get("target") != "echo-dot-2"
            or record.get("inputs") != inputs() or record.get("inputs_sha256") != identity()
            or not isinstance(record.get("files"), dict)
            or set(record.get("files", {})) != set(FILES)
            or record["files"]["zImage-dtb"] != digest(image)):
        raise ValueError("kernel artifact provenance/inputs or image checksum mismatch; rebuild with make dot-kernel")


def load(directory):
    directory = Path(directory)
    record = json.loads((directory / "provenance.json").read_bytes())
    image = (directory / "zImage-dtb").read_bytes()
    validate(record, image)
    for name in FILES:
        if digest((directory / name).read_bytes()) != record["files"][name]:
            raise ValueError(f"kernel artifact checksum mismatch: {name}")
    return record, image


def record(directory, work):
    directory, work = Path(directory), Path(work)
    data = {"schema": 1, "target": "echo-dot-2", "inputs": inputs(),
            "inputs_sha256": identity(), "hardware_qualified": False,
            "encore_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
            "encore_dirty": bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT)),
            "build_environment": {
                "image": os.environ.get("ENCORE_KERNEL_BUILD_IMAGE", "native (not the CI container)"),
                "os_release": Path("/etc/os-release").read_text(),
                "machine": platform.machine(),
                "host_cc": subprocess.check_output(["cc", "--version"], text=True).splitlines()[0],
                "cross_cc": subprocess.check_output([str(work / "toolchain/bin/arm-eabi-gcc"), "--version"], text=True).splitlines()[0],
                "tools": {tool: subprocess.check_output([tool, "--version"], text=True).splitlines()[0]
                          for tool in ("make", "ld", "git", "patch", "tar", "gzip", "python3")}},
            "files": {name: digest((directory / name).read_bytes()) for name in FILES}}
    (directory / "provenance.json").write_text(json.dumps(data, sort_keys=True, indent=2) + "\n")
    load(directory)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("identity", "path", "verify", "record"))
    parser.add_argument("--root", type=Path, default=ROOT / "build/echo-dot-2/kernel")
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--work", type=Path)
    args = parser.parse_args()
    if args.operation == "identity":
        print(identity())
    elif args.operation == "path":
        print(args.root / identity())
    elif args.operation == "verify":
        data, _ = load(args.directory)
        print("Kernel artifact verified:", data["files"]["zImage-dtb"])
    else:
        record(args.directory, args.work)
