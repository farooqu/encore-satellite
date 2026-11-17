"""Offline Linux channel signing, with disposable keys and no publication."""
import base64
import gzip
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import time
import unittest
import zipfile

import feed
from test_package import write_bundle


class FeedTests(unittest.TestCase):
    def test_sign_promote_and_reject_unsafe_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bundle = root / "bundle.zip"
            archive = io.BytesIO()
            with tarfile.open(fileobj=archive, mode="w") as tar:
                for name, data in {"sbin/init": b"init", "usr/local/bin/encore-satellite": b"app",
                                   "etc/encore-base": b"encore-dot-2-v1\n", "etc/inittab": b"init"}.items():
                    info = tarfile.TarInfo(name)
                    info.size = len(data)
                    tar.addfile(info, io.BytesIO(data))
            write_bundle(bundle, gzip.compress(archive.getvalue()), b"initramfs", 14)
            with zipfile.ZipFile(bundle) as source:
                files = {name: source.read(name) for name in source.namelist()}
            manifest = json.loads(files["manifest.json"])
            manifest["core_version"] = "1.10.0"
            files["manifest.json"] = json.dumps(manifest).encode()
            with zipfile.ZipFile(bundle, "w") as target:
                for name, data in files.items():
                    target.writestr(name, data)
            before = bundle.read_bytes()
            issued = int(time.time()) - 600
            expires = issued + 3600
            url = "https://updates.example.test/bundle.zip"
            key = root / "disposable.pem"
            public = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(key)], check=True, capture_output=True)
            subprocess.run(["openssl", "pkey", "-in", str(key), "-pubout", "-out", str(public)], check=True, capture_output=True)
            output = root / "stable.jws"
            command = ["python3", str(Path(feed.__file__)), "--bundle", str(bundle), "--url", url,
                       "--channel", "stable", "--issued", str(issued), "--expires", str(expires),
                       "--key", str(key), "--output", str(output)]
            subprocess.run(command, check=True, capture_output=True)
            header, payload, signature = output.read_bytes().strip().split(b".")
            decode = lambda value: base64.urlsafe_b64decode(value + b"=" * (-len(value) % 4))
            self.assertEqual(decode(header), b'{"alg":"EdDSA","typ":"encore-linux-update+jws"}')
            release = json.loads(decode(payload))
            self.assertEqual((release["channel"], release["revision"], release["core_version"]), ("stable", 14, "1.10.0"))
            self.assertEqual(bundle.read_bytes(), before)  # promotion is not rebuilding
            message = root / "message"
            sig = root / "signature"
            message.write_bytes(header + b"." + payload)
            sig.write_bytes(decode(signature))
            subprocess.run(["openssl", "pkeyutl", "-verify", "-rawin", "-pubin", "-inkey", str(public),
                            "-in", str(message), "-sigfile", str(sig)], check=True, capture_output=True)
            self.assertNotEqual(subprocess.run(command, capture_output=True).returncode, 0)
            for bad in ("http://example.test/b.zip", "https://user:pass@example.test/b.zip", "https://example.test/b.zip?key=secret"):
                with self.assertRaises(ValueError):
                    feed.metadata(bundle, bad, "dev", issued, expires)
            with self.assertRaises(ValueError):
                feed.metadata(bundle, url, "stable", issued, issued + 7 * 86400 + 1)
            manifest["core_version"] = "1.10.0-rc.1"
            files["manifest.json"] = json.dumps(manifest).encode()
            with zipfile.ZipFile(bundle, "w") as target:
                for name, data in files.items():
                    target.writestr(name, data)
            with self.assertRaises(ValueError):
                feed.metadata(bundle, url, "stable", issued, expires)
