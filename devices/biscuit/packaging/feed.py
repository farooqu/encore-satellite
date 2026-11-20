#!/usr/bin/env python3
"""Offline approval of an inspected Dot bundle as compact Ed25519 JWS.

No publication, key generation, device access or trust provisioning. The metadata
key stays offline, outside the checkout. Requires OpenSSL 3.
"""

import argparse
import base64
import json
from pathlib import Path
import subprocess
import tempfile
import time
from urllib.parse import urlsplit
import zipfile

from package import sha
from kernel61.starter_install import load_bundle as load_arm64_starter


def encode(value):
    return base64.urlsafe_b64encode(value).rstrip(b"=")


def arm64_metadata(starter, pin, url, issued, expires):
    parsed = urlsplit(url)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username is not None
            or parsed.password is not None or parsed.query or parsed.fragment):
        raise ValueError("artifact URL must be HTTPS without credentials, query or fragment")
    if not 0 < expires - issued <= 7 * 86400:
        raise ValueError("invalid lifetime (maximum seven days)")
    manifest, rootfs, _ = load_arm64_starter(starter, pin)
    core = manifest.get("core_version")
    if not core or "-" in core.split("+", 1)[0]:
        raise ValueError("public ARM64 release requires an upgrade-capable starter with a stable version")
    return {"target": "biscuit-arm64", "backend": "incubator", "format": "encore-biscuit-arm64-rootfs-v1",
            "channel": "stable", "core_version": core, "revision": manifest["package_revision"],
            "base_sha256": manifest["retained_base_sha256"], "url": url, "size": len(rootfs),
            "sha256": sha(rootfs), "issued": issued, "expires": expires}, rootfs


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--url", required=True)
    parser.add_argument("--channel", choices=("stable",), required=True)
    parser.add_argument("--issued", type=int, default=int(time.time()))
    parser.add_argument("--expires", type=int, required=True)
    parser.add_argument("--key", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--arm64-starter-sha256", required=True, help="reviewed starter ZIP pin; sign its rootfs for the public feed")
    parser.add_argument("--arm64-rootfs-output", type=Path, required=True, help="write the exact archive to retain as the public asset")
    args = parser.parse_args()
    try:
        release, rootfs = arm64_metadata(args.bundle, args.arm64_starter_sha256, args.url, args.issued, args.expires)
        header = b'{"alg":"EdDSA","typ":"encore-linux-update+jws"}'
        message = encode(header) + b"." + encode(json.dumps(release, sort_keys=True, separators=(",", ":")).encode())
        # Ed25519 OpenSSL one-shot signing needs a regular input file.
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "message"
            path.write_bytes(message)
            signature = subprocess.check_output(["openssl", "pkeyutl", "-sign", "-rawin", "-inkey", str(args.key), "-in", str(path)])
        if len(signature) != 64:
            raise ValueError("expected an Ed25519 metadata key")
        with args.arm64_rootfs_output.open("xb") as output:
            output.write(rootfs)
        with args.output.open("xb") as output:
            output.write(message + b"." + encode(signature) + b"\n")
    except (ValueError, OSError, subprocess.SubprocessError, zipfile.BadZipFile) as error:
        parser.exit(1, f"Linux feed approval failed: {error}\n")


if __name__ == "__main__":
    main()
