"""Read-only provenance for historical Biscuit Linux 3.18 artifacts."""

import hashlib
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
FILES = ("zImage-dtb", "zImage-dtb.config", "zImage-dtb.System.map", "source.tar.gz")
BUILDER_SHA256 = "a95e2d598fcf6f7213a69c24f18ed1076bd4357d51c837d006d3dff2585f548e"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def inputs():
    lock = json.loads((HERE / "kernel-inputs.json").read_bytes())
    return {"lock": lock, "builder_sha256": BUILDER_SHA256}


def identity():
    return digest(json.dumps(inputs(), sort_keys=True).encode())


def validate(record, image):
    if (not isinstance(record, dict)
            or record.get("schema") != 1 or record.get("target") != "biscuit"
            or record.get("inputs") != inputs() or record.get("inputs_sha256") != identity()
            or not isinstance(record.get("files"), dict)
            or set(record.get("files", {})) != set(FILES)
            or record["files"]["zImage-dtb"] != digest(image)):
        raise ValueError("historical kernel artifact provenance/inputs or image checksum mismatch")


def load(directory):
    directory = Path(directory)
    record = json.loads((directory / "provenance.json").read_bytes())
    image = (directory / "zImage-dtb").read_bytes()
    validate(record, image)
    for name in FILES:
        if digest((directory / name).read_bytes()) != record["files"][name]:
            raise ValueError(f"kernel artifact checksum mismatch: {name}")
    return record, image
