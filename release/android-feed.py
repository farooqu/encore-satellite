#!/usr/bin/env python3
"""Offline approval: sign an inspected release JSON as compact Ed25519 JWS.

Uses OpenSSL 3; private keys remain in files outside the checkout. No network or
publication. Prepare JSON from the inspected APK/provenance per android-updates.md.
"""
import argparse
import base64
import json
import pathlib
import subprocess
import tempfile


def encode(value):
    return base64.urlsafe_b64encode(value).rstrip(b"=")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("metadata", type=pathlib.Path)
    parser.add_argument("--key", required=True, type=pathlib.Path)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    args = parser.parse_args()
    metadata = json.loads(args.metadata.read_text())
    if metadata["channel"] not in ("dev", "stable") or metadata["backend"] != "incubator" or metadata["target"] != "android-generic":
        parser.error("wrong target/backend/channel")
    if metadata["channel"] == "stable" and "-" in metadata["core_version"].split("+", 1)[0]:
        parser.error("stable core must not be a prerelease")
    if not 0 < metadata["expires"] - metadata["issued"] <= 7 * 86400:
        parser.error("metadata lifetime must be positive and at most seven days")
    header = b'{"alg":"EdDSA","typ":"encore-android-update+jws"}'
    payload = json.dumps(metadata, sort_keys=True, separators=(",", ":")).encode()
    signing_input = encode(header) + b"." + encode(payload)
    # OpenSSL Ed25519 one-shot signing requires a regular input file, not stdin.
    with tempfile.TemporaryDirectory() as directory:
        message = pathlib.Path(directory) / "input"
        message.write_bytes(signing_input)
        signature = subprocess.check_output(["openssl", "pkeyutl", "-sign", "-rawin", "-inkey", str(args.key), "-in", str(message)])
    if len(signature) != 64:
        parser.error("expected an Ed25519 key")
    with args.output.open("xb") as output:
        output.write(signing_input + b"." + encode(signature) + b"\n")


if __name__ == "__main__":
    main()
