"""Offline public CLI contracts with disposable keys and synthetic source/bundles."""
import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch
import zipfile

import pipeline
from biscuit_dev import ANCHOR, SEMVER_BASE
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
        with zipfile.ZipFile(apk, "w") as archive:
            for abi in ("armeabi-v7a", "arm64-v8a"):
                archive.writestr(f"lib/{abi}/libgojni.so", "synthetic engine")
        shutil.copyfile(apk, self.build / "second.apk")
        cert = "9" * 64
        badging = ("package: name='com.encore.satellite.incubator' versionCode='5' versionName='1.2.0-incubator'\n"
                   "launchable-activity: name='com.encore.satellite.MainActivity'\n"
                   "uses-permission: name='android.permission.REQUEST_INSTALL_PACKAGES'\n"
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

    def test_dev_publication_requires_matching_draft_and_immutable_readback(self):
        self.prepare()
        for remote_matches, immutable, expected in (
            (False, True, "uploaded assets differ; leave draft unpublished"),
            (True, False, "/releases/tag/android-generic-r5"),
            (True, True, None),
        ):
            with self.subTest(remote_matches=remote_matches, immutable=immutable):
                operations = []

                def read_command(*command):
                    if command[0] == "git":
                        return b""
                    if "/git/matching-refs/" in command[-1]:
                        return b"[]"
                    if command[-1].endswith("releases?per_page=100"):
                        return b"[[]]"
                    if command[-1].endswith("releases/tags/android-generic-r5"):
                        return json.dumps({"immutable": immutable}).encode()
                    self.fail(f"Unexpected read: {command}")

                def write_command(command, *, check):
                    operation = command[2]
                    operations.append(operation)
                    if operation == "download":
                        draft = Path(command[-1])
                        for asset in self.output.iterdir():
                            shutil.copyfile(asset, draft / asset.name)
                        if not remote_matches:
                            (draft / "app.apk").write_bytes(b"wrong remote bytes")

                with (patch.dict(os.environ, {"IMMUTABILITY_READ_TOKEN": ""}),
                      patch.object(pipeline, "run", side_effect=read_command),
                      patch.object(pipeline.subprocess, "run", side_effect=write_command)):
                    with self.assertRaisesRegex(ValueError, "IMMUTABILITY_READ_TOKEN required"):
                        pipeline.publish(self.output, self.receipt, execute=True)
                    self.assertEqual(operations, [])
                    if expected:
                        with self.assertRaisesRegex(ValueError, expected):
                            pipeline.publish(self.output, self.receipt, execute=True, automatic_dev=True)
                    else:
                        pipeline.publish(self.output, self.receipt, execute=True, automatic_dev=True)
                self.assertEqual(operations, ["create", "upload", "download"] + (["edit"] if remote_matches else []))

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

    def test_biscuit_feed_serves_only_retained_rootfs(self):
        receipt = {"target": "biscuit", "core_version": "0.1.42", "revision": 1000000041,
                   "artifact": {"name": "encore-biscuit-arm64-0.1.42.zip", "size": 600, "sha256": "6" * 64},
                   "ota": {"name": "rootfs.tar.gz", "size": 200, "sha256": "2" * 64},
                   "compatibility": {"format": pipeline.ARM64_STARTER, "base_sha256": "1" * 64}}
        site = self.build / "site"
        for channel in ("dev", "stable"):
            feed = self.build / f"biscuit-{channel}.jws"
            payload = pipeline.metadata(receipt, channel, self.now, self.now + 3600)
            self.assertEqual((payload["target"], payload["format"], payload["size"], payload["sha256"]),
                             ("biscuit-arm64", "encore-biscuit-arm64-rootfs-v1", 200, "2" * 64))
            self.assertEqual(payload["url"],
                             "https://github.com/farooqu/encore-satellite/releases/download/biscuit-0.1.42/rootfs.tar.gz")
            pipeline.sign(payload, pipeline.ARM64_FEED_TYPE, self.key, feed)
            pipeline.stage_feed(receipt, feed, self.public, site, channel, self.now)
            self.assertEqual((site / "biscuit-arm64/incubator" / f"{channel}.jws").read_bytes(), feed.read_bytes())
        bad = self.build / "starter-as-ota.jws"
        payload.update(url=payload["url"].replace("rootfs.tar.gz", receipt["artifact"]["name"]),
                       size=600, sha256="6" * 64)
        pipeline.sign(payload, pipeline.ARM64_FEED_TYPE, self.key, bad)
        with self.assertRaisesRegex(ValueError, "feed differs from retained artifact"):
            pipeline.stage_feed(receipt, bad, self.public, site, "stable", self.now)

    def test_android_inspection_contract_and_signed_feed(self):
        self.prepare()
        self.verified("verify")
        feed = self.feed("dev")
        self.stage(feed, "dev")
        payload = pipeline.verify_jws(feed, pipeline.ANDROID_FEED_TYPE, self.public)
        self.assertEqual((payload["source"], payload["revision"], payload["certificate_sha256"]), (self.source, 5, self.spec["certificate_sha256"]))
        with zipfile.ZipFile(self.build / "missing-engine.apk", "w") as archive:
            # Another library keeps arm64 in the APK's advertised ABIs.
            archive.writestr("lib/arm64-v8a/libother.so", "not the engine")
            archive.writestr("lib/armeabi-v7a/libgojni.so", "synthetic engine")
        with self.assertRaisesRegex(ValueError, "APK missing Go engine"):
            pipeline.inspect_artifact({"target": "android-generic", "revision": 5, "core_version": "1.2.0"},
                                      self.build / "missing-engine.apk", self.aapt, self.signer)
        for name, data in (("badging", (self.build / "badging").read_text() + "application-debuggable\n"),
                           ("manifest", (self.build / "manifest").read_text().replace("1.2.0", "1.1.9")),
                           ("cert", f"Signer #1 certificate SHA-256 digest: {'8' * 64}\n")):
            path = self.build / name
            original = path.read_text()
            path.write_text(data)
            self.cli("prepare", self.spec_path, "--output", self.build / "rejected", "--aapt", self.aapt, "--apksigner", self.signer, success=False)
            path.write_text(original)

    def test_main_derived_android_signing_uses_exact_source_version(self):
        # Set the synthetic initial commit as the anchor, then build the next one.
        resolver = self.repo / "release/biscuit_dev.py"
        resolver.write_text(resolver.read_text().replace(ANCHOR, self.source))
        self.git("add", str(resolver))
        self.git("commit", "-qm", "first release after anchor")
        self.source = self.git("rev-parse", "HEAD").strip()
        self.spec.pop("reservation")
        self.spec["identity"] = "main"
        core, revision = "0.1.2", 1_000_000_001
        badging = self.build / "badging"
        badging.write_text(badging.read_text().replace("versionCode='5'", f"versionCode='{revision}'").replace("1.2.0", core))
        manifest = self.build / "manifest"
        manifest.write_text(manifest.read_text().replace("1.2.0", core))
        self.cli("inspect-android", self.spec["artifact"], "--revision", revision, "--core-version", core,
                 "--version", f"{core}-incubator", "--certificate-sha256", self.spec["certificate_sha256"],
                 "--aapt", self.aapt, "--apksigner", self.signer)
        self.spec_path.write_text(json.dumps(self.spec))
        self.cli("prepare", self.spec_path, "--output", self.output, "--aapt", self.aapt, "--apksigner", self.signer)
        path = self.output / "receipt.json"
        original = path.read_bytes()
        self.receipt = json.loads(original)
        self.assertEqual((self.receipt["core_version"], self.receipt["revision"], self.receipt["source"]),
                         (core, revision, self.source))
        self.assertNotIn("reservation", self.receipt)
        for change in ({"core_version": "0.1.3"}, {"revision": revision + 1}):
            path.write_text(json.dumps({**self.receipt, **change}))
            result = self.cli("sign-receipt", self.output, "--key", self.key, "--receipt-sha256", pipeline.digest(path), success=False)
            self.assertIn("Android receipt differs from its main-derived identity", result.stderr)
        path.write_bytes(original)
        self.sha = pipeline.digest(path)
        self.cli("sign-receipt", self.output, "--key", self.key, "--receipt-sha256", self.sha)
        dev, stable = (pipeline.verify_jws(self.feed(channel), pipeline.ANDROID_FEED_TYPE, self.public)
                       for channel in ("dev", "stable"))
        self.assertEqual({**dev, "channel": "stable"}, stable)

    def test_release_history_advances_lockstep_and_holds_minor(self):
        # Synthetic old anchor and one height-versioned commit before cutover.
        self.git("commit", "--allow-empty", "-qm", "last height-versioned source")
        base = self.git("rev-parse", "HEAD").strip()
        resolver = self.repo / "release/biscuit_dev.py"
        resolver.write_text(resolver.read_text().replace(ANCHOR, self.source).replace(SEMVER_BASE, base))

        def resolve(source):
            return subprocess.check_output([sys.executable, resolver, source], cwd=self.repo, text=True).strip()

        self.assertEqual(resolve(base), "0.1.2 1000000001")
        for subject, expected in (
            ("docs: explain feeds", "0.1.3 1000000002"),
            ("fix(core): fix shared startup", "0.1.4 1000000003"),
            ("perf(biscuit): reduce boot reads", "0.1.5 1000000004"),
            ("feat(core): held minor bump", ""),
            ("fix: do not leak a held minor through a patch", ""),
        ):
            self.git("commit", "--allow-empty", "-qm", subject)
            self.assertEqual(resolve(self.git("rev-parse", "HEAD").strip()), expected)
        self.assertEqual(resolve(base), "0.1.2 1000000001")


class NativeMetadataTests(unittest.TestCase):
    def test_feed_addresses_only_native_ota_on_selected_channel(self):
        receipt = {"target": "biscuit", "core_version": "0.1.42", "revision": 1000000041,
                   "artifact": {"name": "encore-biscuit-arm64-0.1.42-native-seed.zip", "size": 600, "sha256": "6" * 64},
                   "ota": {"name": "encore-biscuit-arm64-0.1.42-native-ota.zip", "size": 200, "sha256": "2" * 64},
                   "compatibility": {"format": pipeline.NATIVE_STARTER, "base_sha256": "1" * 64}}
        now = 1_800_000_000
        for channel in ("stable", "dev"):
            self.assertEqual(pipeline.metadata(receipt, channel, now, now + 3600), {
                "target": "biscuit-native-ab", "backend": "incubator", "format": "encore-biscuit-native-ab-v1",
                "base_sha256": "1" * 64, "channel": channel, "core_version": "0.1.42", "revision": 1000000041,
                "url": "https://github.com/farooqu/encore-satellite/releases/download/biscuit-0.1.42/encore-biscuit-arm64-0.1.42-native-ota.zip",
                "size": 200, "sha256": "2" * 64, "issued": now, "expires": now + 3600})


if __name__ == "__main__":
    unittest.main()
