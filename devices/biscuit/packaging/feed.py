#!/usr/bin/env python3
"""Offline approval of an inspected Dot bundle as compact Ed25519 JWS.

No publication, key generation, device access or trust provisioning. The metadata
key stays offline, outside the checkout. Requires OpenSSL 3.
"""

import argparse
import base64
import json
from pathlib import Path
import re
import subprocess
import tempfile
import time
from urllib.parse import urlsplit
import zipfile

from package import sha, FORMAT
from install import load_bundle
from kernel61.starter_install import load_bundle as load_arm64_starter


def encode(value):
    return base64.urlsafe_b64encode(value).rstrip(b"=")


def metadata(bundle, url, channel, issued, expires):
    check_feed_args(url, channel, issued, expires)
    size = bundle.stat().st_size
    if not 0 < size <= 256 * 1024 * 1024:
        raise ValueError("bundle exceeds device download limit")
    digest = sha(bundle.read_bytes())
    # The existing USB validator accepts only dev provenance. A stable channel
    # entry promotes these exact immutable bytes; no package rewrite/rebuild.
    manifest, _ = load_bundle(bundle, digest)
    core = manifest.get("core_version", "")
    pattern = (r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
               r"(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
               r"(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?"
               r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?")
    if not re.fullmatch(pattern, core) or channel == "stable" and "-" in core.split("+", 1)[0]:
        raise ValueError("expected complete core SemVer; stable cannot be a prerelease")
    return {"target": "biscuit", "backend": "incubator", "format": FORMAT,
            "channel": channel, "core_version": core, "revision": manifest["package_revision"],
            "base_sha256": manifest["base_sha256"], "url": url, "size": size,
            "sha256": digest, "issued": issued, "expires": expires}


def check_feed_args(url, channel, issued, expires):
    parsed = urlsplit(url)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username is not None
            or parsed.password is not None or parsed.query or parsed.fragment):
        raise ValueError("artifact URL must be HTTPS without credentials, query or fragment")
    if channel not in ("dev", "stable") or not 0 < expires - issued <= 7 * 86400:
        raise ValueError("invalid channel or lifetime (maximum seven days)")


def arm64_metadata(starter, pin, url, issued, expires):
    check_feed_args(url, "stable", issued, expires)
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
    parser.add_argument("--channel", choices=("dev", "stable"), required=True)
    parser.add_argument("--issued", type=int, default=int(time.time()))
    parser.add_argument("--expires", type=int, required=True)
    parser.add_argument("--key", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--arm64-starter-sha256", help="reviewed starter ZIP pin; sign its rootfs for the public feed")
    parser.add_argument("--arm64-rootfs-output", type=Path, help="write the exact archive to retain as the public asset")
    args = parser.parse_args()
    try:
        if args.arm64_starter_sha256 or args.arm64_rootfs_output:
            if args.channel != "stable" or not args.arm64_starter_sha256 or not args.arm64_rootfs_output:
                raise ValueError("ARM64 requires stable, a reviewed starter SHA-256, and a rootfs output path")
            release, rootfs = arm64_metadata(args.bundle, args.arm64_starter_sha256, args.url, args.issued, args.expires)
        else:
            release = metadata(args.bundle, args.url, args.channel, args.issued, args.expires)
        header = b'{"alg":"EdDSA","typ":"encore-linux-update+jws"}'
        message = encode(header) + b"." + encode(json.dumps(release, sort_keys=True, separators=(",", ":")).encode())
        # Ed25519 OpenSSL one-shot signing needs a regular input file.
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "message"
            path.write_bytes(message)
            signature = subprocess.check_output(["openssl", "pkeyutl", "-sign", "-rawin", "-inkey", str(args.key), "-in", str(path)])
        if len(signature) != 64:
            raise ValueError("expected an Ed25519 metadata key")
        if args.arm64_rootfs_output:
            with args.arm64_rootfs_output.open("xb") as output:
                output.write(rootfs)
        with args.output.open("xb") as output:
            output.write(message + b"." + encode(signature) + b"\n")
    except (ValueError, OSError, subprocess.SubprocessError, zipfile.BadZipFile) as error:
        parser.exit(1, f"Linux feed approval failed: {error}\n")


if __name__ == "__main__":
    main()
