"""Check a built kernel .config (or config.gz), not a Fire OS version string.

This is a build-time prerequisite check, not runtime/hardware qualification.
The supported Dot base builds these in; modules would need a separate verified
module-loading/ABI contract. Linux 3.18 has BLE in CONFIG_BT, without BT_LE.
"""

import gzip
import pathlib
import sys


def validate(text):
    config = dict(line.split("=", 1) for line in text.splitlines() if line.startswith("CONFIG_") and "=" in line)
    missing = [key for key in ("CONFIG_BT", "CONFIG_BT_HCIVHCI") if config.get(key) != "y"]
    if "# CONFIG_BT_LE is not set" in text or config.get("CONFIG_BT_LE", "y") != "y":
        missing.append("CONFIG_BT_LE")
    return missing


if __name__ == "__main__":
    path = pathlib.Path(sys.argv[1])
    data = path.read_bytes()
    if data.startswith(b"\x1f\x8b"):
        data = gzip.decompress(data)
    missing = validate(data.decode())
    if missing:
        raise SystemExit("Missing built-in BLE kernel prerequisites: " + ", ".join(missing))
    print("BLE kernel config prerequisites passed; runtime BlueZ/Improv and RF tests still required")
