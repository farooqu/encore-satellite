"""Offline public CLI contracts with disposable keys and synthetic source/bundles."""
import copy
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest

import pipeline
from test_fixtures import inventory


class PipelineTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        shutil.copytree(pipeline.ROOT / "release", self.repo / "release", ignore=shutil.ignore_patterns("__pycache__"))
        (self.repo / ".gitignore").write_text("/build/\n__pycache__/\n")
        # A non-prerelease fixture can exercise both channels without changing
        # the real ledger or pretending the first dev reservation is stable.
        for name, data in zip(("targets.json", "ledger.json"), inventory("1.2.0")):
            (self.repo / "release" / name).write_text(json.dumps(data))
        shutil.rmtree(self.repo / "release/changelogs", ignore_errors=True)
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Pipeline test")
        self.git("add", ".")
        self.git("commit", "-qm", "synthetic release source")
        self.source = self.git("rev-parse", "HEAD").strip()
        self.build = self.repo / "build"
        self.build.mkdir()
        for name in ("dependencies.tar.gz", "evidence.txt"):
            (self.build / name).write_bytes(b"synthetic fixture, not distributable sources\n")
        # SDK output fixtures test parsing/rejection, not real APK signing.
        apk = self.build / "app.apk"
        apk.write_bytes(b"synthetic APK fixture")
        shutil.copyfile(apk, self.build / "second.apk")
        cert = "9" * 64
        badging = ("package: name='com.encore.satellite.incubator' versionCode='5' versionName='1.2.0-incubator'\n"
                   "sdkVersion:'26'\nnative-code: 'armeabi-v7a' 'arm64-v8a'\n")
        manifest = ('A: android:name(0x01010003)="com.encore.core_version"\n'
                    '  A: android:value(0x01010024)="1.2.0" (Raw: "1.2.0")\n')
        for name, data in (("badging", badging), ("manifest", manifest),
                           ("cert", f"Signer #1 certificate SHA-256 digest: {cert}\n")):
            (self.build / name).write_text(data)
        aapt, signer = self.build / "aapt", self.build / "apksigner"
        aapt.write_text('#!/usr/bin/env python3\nimport pathlib, sys\n'
                        'print((pathlib.Path(__file__).parent / ("badging" if sys.argv[2] == "badging" else "manifest")).read_text())\n')
        signer.write_text('#!/usr/bin/env python3\nimport pathlib\nprint((pathlib.Path(__file__).parent / "cert").read_text())\n')
        aapt.chmod(0o755)
        signer.chmod(0o755)
        self.aapt, self.signer = aapt, signer
        self.spec = {"target": "android-generic", "reservation": "first-incubator-dev",
                     "artifact": str(apk), "evidence": str(self.build / "evidence.txt"),
                     "reproduced_artifact": str(self.build / "second.apk"),
                     "certificate_sha256": cert, "sources": {"dependencies": str(self.build / "dependencies.tar.gz")},
                     "toolchain": "synthetic toolchain fixture", "recipe": "synthetic build receipt",
                     "review": "approved-for-distribution"}
        self.spec_path = self.build / "spec.json"
        self.output = self.build / "retained"
        self.key, self.public = self.root / "private.pem", self.root / "public.pem"
        subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(self.key)], check=True, capture_output=True)
        subprocess.run(["openssl", "pkey", "-in", str(self.key), "-pubout", "-out", str(self.public)], check=True, capture_output=True)
        self.now = int(time.time())

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, text=True, stderr=subprocess.PIPE)

    def cli(self, *args, success=True):
        result = subprocess.run([sys.executable, str(self.repo / "release/pipeline.py"), *map(str, args)],
                                cwd=self.repo, capture_output=True, text=True)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return result

    def prepare(self):
        self.spec_path.write_text(json.dumps(self.spec))
        self.cli("prepare", self.spec_path, "--output", self.output, "--aapt", self.aapt, "--apksigner", self.signer)
        self.cli("sign-receipt", self.output, "--key", self.key)
        self.receipt = json.loads((self.output / "receipt.json").read_text())
        self.sha = pipeline.digest(self.output / "receipt.json")

    def verified(self, command, *args, success=True):
        return self.cli(command, self.output, "--public-key", self.public, "--receipt-sha256", self.sha, *args, success=success)

    def feed(self, channel, issued=None):
        issued = self.now if issued is None else issued
        path = self.build / f"{channel}-{issued}.jws"
        self.verified("feed", "--channel", channel, "--issued", issued, "--expires", issued + 3600,
                      "--key", self.key, "--output", path)
        return path

    def stage(self, path, channel, success=True):
        self.verified("stage-feed", "--channel", channel, "--feed", path, "--site", self.build / "site", success=success)

    def test_retain_reproduce_verify_and_publish_dry_run(self):
        self.prepare()
        self.verified("verify")
        # Two preparations have byte-identical archive AND receipt, regardless
        # of output location. Existing directories/signatures cannot overwrite.
        other = self.build / "second"
        self.cli("prepare", self.spec_path, "--output", other, "--aapt", self.aapt, "--apksigner", self.signer)
        for name in ("encore-source.tar.gz", "receipt.json", "app.apk"):
            self.assertEqual((self.output / name).read_bytes(), (other / name).read_bytes())
        self.cli("prepare", self.spec_path, "--output", self.output, success=False)
        self.cli("sign-receipt", self.output, "--key", self.key, success=False)
        plan = json.loads(self.verified("publish").stdout)
        self.assertEqual(plan["mode"], "dry-run")
        self.assertIn("android-generic-r5", plan["commands"][0])
        self.assertIn(self.source, plan["commands"][0])
        self.assertNotIn("--clobber", str(plan))
        self.assertNotIn("dev.jws", str(plan))

    def test_tampered_or_incomplete_assets_and_receipts_fail(self):
        self.prepare()
        for name in ("app.apk", "dependencies.tar.gz", "encore-source.tar.gz", "receipt.jws", "receipt.json"):
            with self.subTest(name=name):
                path = self.output / name
                original = path.read_bytes()
                path.write_bytes(original + b"tampered")
                self.verified("verify", success=False)
                path.write_bytes(original)
        extra = self.output / "unreviewed.bin"
        extra.write_bytes(b"no")
        self.verified("verify", success=False)
        extra.unlink()
        (self.output / "dependencies.tar.gz").unlink()
        self.verified("verify", success=False)

    def test_prepare_rejects_dirty_source_different_assembly_and_missing_sources(self):
        self.spec_path.write_text(json.dumps(self.spec))
        args = ("prepare", self.spec_path, "--output", self.output, "--aapt", self.aapt, "--apksigner", self.signer)
        dirty = self.repo / "uncommitted"
        dirty.write_text("dirty")
        self.assertIn("commit source", self.cli(*args, success=False).stderr)
        dirty.unlink()
        repeated = Path(self.spec["reproduced_artifact"])
        repeated.write_bytes(b"non-reproducible assembly")
        self.assertIn("distinct byte-identical output", self.cli(*args, success=False).stderr)
        shutil.copyfile(self.spec["artifact"], repeated)
        self.spec["sources"] = {}
        self.spec_path.write_text(json.dumps(self.spec))
        self.assertIn("matching corresponding-source roles required", self.cli(*args, success=False).stderr)

    def test_promotion_renewal_and_checkpoints_preserve_bytes(self):
        self.prepare()
        before = {p.name: p.read_bytes() for p in self.output.iterdir()}
        dev, stable = self.feed("dev"), self.feed("stable")
        self.stage(dev, "dev")
        self.stage(stable, "stable")
        self.stage(stable, "stable")  # Idempotent redeploy after Pages failure.
        renewed = self.feed("dev", self.now + 1)
        self.stage(renewed, "dev")
        self.stage(dev, "dev", success=False)
        self.stage(dev, "stable", success=False)
        self.assertEqual(before, {p.name: p.read_bytes() for p in self.output.iterdir()})
        self.assertEqual((self.build / "site/android-generic/incubator/stable.jws").read_bytes(), stable.read_bytes())
        self.assertEqual(len(list((self.build / "site/history/android-generic/dev").iterdir())), 2)
        current = self.build / "site/android-generic/incubator/dev.jws"
        current.unlink()
        self.stage(dev, "dev", success=False)  # Deletion cannot reset rollback protection.
        shutil.copyfile(dev, current)
        self.stage(dev, "dev", success=False)  # Nor can restoring an older signed entry.

    def test_signed_wrong_digest_expiry_root_and_downgrade_are_rejected(self):
        self.prepare()
        good = self.feed("dev")
        self.stage(good, "dev")
        original = pipeline.verify_jws(good, pipeline.ANDROID_FEED_TYPE, self.public)
        for changes in ({"sha256": "0" * 64}, {"issued": self.now - 7200, "expires": self.now - 3600},
                        {"issued": self.now + 3600, "expires": self.now + 7200}, {"channel": "stable"},
                        {"revision": 19}, {"url": "http://example.invalid/latest"}):
            with self.subTest(changes=changes):
                invalid = self.build / "invalid.jws"
                changed = {**original, **changes}
                if invalid.exists():
                    invalid.unlink()
                pipeline.sign(changed, pipeline.ANDROID_FEED_TYPE, self.key, invalid)
                self.stage(invalid, "dev", success=False)
        old = self.build / "site/android-generic/incubator/dev.jws"
        pipeline.sign({**original, "revision": 20}, pipeline.ANDROID_FEED_TYPE, self.key, self.build / "newer.jws")
        shutil.copyfile(self.build / "newer.jws", old)
        shutil.copyfile(old, self.build / f"site/history/android-generic/dev/{original['issued']}.jws")
        self.stage(self.feed("dev", self.now + 2), "dev", success=False)
        key = self.root / "wrong.pem"
        subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(key)], check=True, capture_output=True)
        wrong = self.build / "wrong.jws"
        pipeline.sign(original, pipeline.ANDROID_FEED_TYPE, key, wrong)
        self.stage(wrong, "dev", success=False)

    def test_prerelease_promotion_and_semver_rollback(self):
        self.prepare()
        prerelease = copy.deepcopy(self.receipt)
        prerelease["core_version"] = "1.2.0-rc.1"
        with self.assertRaisesRegex(ValueError, "prerelease"):
            pipeline.metadata(prerelease, "stable", self.now, self.now + 3600)
        # Numeric prerelease and numeric core components must not sort lexically.
        versions = ["1.2.0-alpha.2", "1.2.0-alpha.10", "1.2.0-rc.1", "1.2.0", "1.10.0"]
        self.assertEqual(sorted(reversed(versions), key=pipeline.core_key), versions)
        self.assertEqual(pipeline.core_key("1.2.0+a"), pipeline.core_key("1.2.0+b"))
        good = self.feed("dev")
        previous = pipeline.verify_jws(good, pipeline.ANDROID_FEED_TYPE, self.public)
        previous.update(revision=4, core_version="1.10.0", issued=self.now - 1)
        site = self.build / "site/android-generic/incubator"
        site.mkdir(parents=True)
        pipeline.sign(previous, pipeline.ANDROID_FEED_TYPE, self.key, site / "dev.jws")
        history = self.build / "site/history/android-generic/dev"
        history.mkdir(parents=True)
        shutil.copyfile(site / "dev.jws", history / f"{previous['issued']}.jws")
        self.stage(good, "dev", success=False)

    def test_android_inspection_contract_and_signed_feed(self):
        self.prepare()
        self.verified("verify")
        feed = self.feed("dev")
        self.stage(feed, "dev")
        payload = pipeline.verify_jws(feed, pipeline.ANDROID_FEED_TYPE, self.public)
        self.assertEqual((payload["source"], payload["revision"], payload["certificate_sha256"]), (self.source, 5, self.spec["certificate_sha256"]))
        for name, data in (("badging", (self.build / "badging").read_text() + "application-debuggable\n"),
                           ("manifest", (self.build / "manifest").read_text().replace("1.2.0", "1.1.9")),
                           ("cert", f"Signer #1 certificate SHA-256 digest: {'8' * 64}\n")):
            path = self.build / name
            original = path.read_text()
            path.write_text(data)
            self.cli("prepare", self.spec_path, "--output", self.build / "rejected", "--aapt", self.aapt, "--apksigner", self.signer, success=False)
            path.write_text(original)


if __name__ == "__main__":
    unittest.main()
