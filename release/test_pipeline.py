"""Offline public CLI contracts with disposable keys and synthetic source/bundles."""
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest
from unittest.mock import patch
import zipfile

import biscuit_dev
import check
import dev_plan
import pipeline
import product_versions
import stable_plan
from biscuit_dev import ANCHOR, SEMVER_BASE
from test_fixtures import inventory


class BiscuitSourceTests(unittest.TestCase):
    def test_release_binds_sendspin_source_and_notices_without_changing_historical_receipts(self):
        def archive(files):
            stream = io.BytesIO()
            with tarfile.open(fileobj=stream, mode="w:gz") as tar:
                for name, content in files.items():
                    member = tarfile.TarInfo(name)
                    member.size = len(content)
                    tar.addfile(member, io.BytesIO(content))
            return stream.getvalue()

        sha = lambda data: hashlib.sha256(data).hexdigest()
        source = {"alpine-source.tar.gz": b"alpine", "go-source.tar.gz": b"go", "go.mod": b"mod", "go.sum": b"sum"}
        pins = {name: sha(data) for name, data in source.items()}
        pins["linux-source.tar.gz"] = "1" * 64
        manifest = {"kernel_image_sha256": "2" * 64, "dtb_sha256": "3" * 64}
        kernel = {"source_sha256": pins["linux-source.tar.gz"], "files": {
            "Image": {"sha256": manifest["kernel_image_sha256"]},
            "biscuit-arm64-probe.dtb": {"sha256": manifest["dtb_sha256"]}}}
        lock_path = "platforms/linuxsendspin/dependencies.lock.json"
        lock = b'{"schema": 1}\n'
        music = {"encore-sendspin": b"binary", "notices/README.txt": b"notice", "source.tar.gz": b"source kit"}
        provenance = {"schema": 1, "target": "arm64", "dependencies": json.loads(lock),
                      "artifacts": {name: sha(data) for name, data in music.items()}}
        runtime = {"etc/encore-sendspin.json": json.dumps(provenance).encode(),
                   "usr/local/bin/encore-sendspin": music["encore-sendspin"],
                   "usr/share/licenses/encore/sendspin/README.txt": music["notices/README.txt"],
                   "usr/share/licenses/encore/sendspin/dependencies.lock.json": lock}

        def git(*args):
            if args[:2] == ("git", "ls-tree"):
                return b"" if historical else b"arm64-musl.cmake\n"
            return {**source, lock_path: lock}[args[-1].split(":", 1)[1]]

        scratch = pipeline.ROOT / ".amp/in/orb-backup"
        scratch.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=scratch) as tmp, patch.object(pipeline, "biscuit_lock", return_value=(None, pins)), \
                patch.object(pipeline, "run", side_effect=git):
            sources = {role: Path(tmp) / role for role in ("alpine", "kernel")}
            sources["kernel"].write_bytes(archive({"manifest.json": json.dumps(kernel).encode()}))

            def verify(files, rootfs):
                sources["alpine"].write_bytes(archive(files))
                pipeline.verify_biscuit_sources({"source": "a" * 40}, manifest, sources, archive(rootfs))

            historical = True
            verify(source, {})
            historical = False
            with self.assertRaisesRegex(KeyError, "sendspin-source.tar.gz"):
                verify(source, runtime)
            source["sendspin-source.tar.gz"] = b"substituted"
            with self.assertRaisesRegex(ValueError, "Sendspin source kit differs"):
                verify(source, runtime)
            source["sendspin-source.tar.gz"] = music["source.tar.gz"]
            verify(source, runtime)
            runtime["usr/share/licenses/encore/sendspin/README.txt"] = b"substituted"
            with self.assertRaisesRegex(ValueError, "artifact differs from provenance: notices/README.txt"):
                verify(source, runtime)


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
        self.assertIn("android-1.2.0", plan["commands"][0])
        self.assertIn(self.source, plan["commands"][0])
        self.assertNotIn("--clobber", str(plan))
        self.assertNotIn("dev.jws", str(plan))

    def test_dev_publication_requires_matching_draft_and_immutable_readback(self):
        self.prepare()
        for tagged_source, remote_matches, immutable, expected in (
            (None, False, True, "uploaded assets differ; leave draft unpublished"),
            (None, True, False, "/releases/tag/android-1.2.0"),
            (None, True, True, None),
            (self.source, True, True, None),
            ("0" * 40, True, True, "existing Dev tag must be a lightweight tag at the exact receipt source"),
        ):
            with self.subTest(tagged_source=tagged_source, remote_matches=remote_matches, immutable=immutable):
                operations = []

                def read_command(*command):
                    if command[0] == "git":
                        return b""
                    if "/git/matching-refs/" in command[-1]:
                        refs = [{"ref": "refs/tags/android-1.2.0", "object": {"type": "commit", "sha": tagged_source}}] if tagged_source else []
                        return json.dumps(refs).encode()
                    if command[-1].endswith("releases?per_page=100"):
                        return b"[[]]"
                    if command[-1].endswith("releases/tags/android-1.2.0"):
                        return json.dumps({"immutable": immutable}).encode()
                    self.fail(f"Unexpected read: {command}")

                def write_command(command, *, check):
                    operation = command[2]
                    operations.append(operation)
                    if operation == "create":
                        self.assertEqual("--verify-tag" in command, tagged_source is not None)
                        self.assertEqual("--target" in command, tagged_source is None)
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
                wanted = [] if tagged_source not in (None, self.source) else ["create", "upload", "download"]
                self.assertEqual(operations, wanted + (["edit"] if wanted and remote_matches else []))

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

    def test_inline_changelog_can_be_added_on_feed_renewal(self):
        self.prepare()
        self.stage(self.feed("dev"), "dev")
        notes = "## Android\n\n- Restore playback after reconnect\n"
        plan = self.build / "plan.json"
        plan.write_text(json.dumps({"source": self.source, "version": self.receipt["core_version"],
                                    "revision": self.receipt["revision"], "targets": ["android-generic", "biscuit"],
                                    "changelog": notes + "\n## Biscuit\n\n- Fix boot pacing\n",
                                    "target_changelogs": {"android-generic": notes, "biscuit": "- Fix boot pacing"}}))
        output = self.build / "with-notes.jws"
        args = ("--channel", "dev", "--issued", self.now + 1, "--expires", self.now + 3600,
                "--key", self.key, "--output", output, "--dev-plan", plan)
        self.verified("feed", *args)
        self.stage(output, "dev")
        payload = pipeline.verify_jws(output, pipeline.ANDROID_FEED_TYPE, self.public)
        self.assertEqual(payload["release_summary"], notes.strip())
        receipt = json.loads(plan.read_bytes())
        output.unlink()
        plan.write_text(json.dumps({**receipt, "target_changelogs": {"biscuit": "- Fix boot pacing"}}))
        self.assertIn("no changelog for candidate target", self.verified("feed", *args, success=False).stderr)
        plan.write_text(json.dumps({**receipt, "revision": receipt["revision"] + 1}))
        self.assertIn("Dev release plan differs", self.verified("feed", *args, success=False).stderr)

        excessive = self.build / "excessive.jws"
        pipeline.sign({**payload, "issued": self.now + 2, "release_summary": "é" * 1537},
                      pipeline.ANDROID_FEED_TYPE, self.key, excessive)
        self.stage(excessive, "dev", success=False)
        trimmed = pipeline.inline_changelog("é" * 2000)
        self.assertLessEqual(len(trimmed.encode("utf-8")), 3072)
        self.assertTrue(trimmed.startswith("é" * 1500))
        self.assertTrue(trimmed.endswith("full changelog._"))

    def test_prerelease_promotion_and_semver_rollback(self):
        self.prepare()
        prerelease = copy.deepcopy(self.receipt)
        prerelease["core_version"] = "1.2.0-rc.1"
        with self.assertRaisesRegex(ValueError, "prerelease"):
            pipeline.metadata(prerelease, "stable", self.now, self.now + 3600)
        self.assertEqual(pipeline.tag({**prerelease, "core_version": "0.1.1-dev.1"}), "android-0.1.1-dev.1")
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
        self.assertEqual(dev["revision"], revision)
        self.assertEqual(dev["url"], f"https://github.com/farooqu/encore-satellite/releases/download/android-{core}/app.apk")
        notes = "## Android\n\n- Fix startup\n"
        plan = {"source": self.source, "version": core, "revision": revision,
                "targets": ["android-generic"], "changelog": notes + "\n## Biscuit\n\n- No build\n"}
        plan_path = self.build / "plan.json"
        plan_path.write_text(json.dumps(plan))
        command = json.loads(self.verified("publish", "--automatic-dev", "--dev-plan", plan_path).stdout)["commands"][0]
        self.assertEqual(command[3], f"android-{core}")
        self.assertEqual(command[command.index("--title") + 1], f"Android {core}")
        self.assertEqual(command[command.index("--notes") + 1], notes)
        plan_path.write_text(json.dumps({**plan, "targets": ["biscuit"]}))
        rejected = self.verified("publish", "--automatic-dev", "--dev-plan", plan_path, success=False)
        self.assertIn("Dev release plan differs from signed candidate", rejected.stderr)

    def test_product_train_and_same_byte_stable_feed(self):
        legacy_source = self.source
        resolver = self.repo / "release/biscuit_dev.py"
        resolver.write_text(resolver.read_text().replace(ANCHOR, legacy_source))
        self.git("add", str(resolver))
        self.git("commit", "-qm", "chore(release): establish fixture anchor")
        baseline = self.git("rev-parse", "HEAD").strip()
        target = "android-generic"
        revision = 1_000_000_002
        policy = self.repo / "release/dev-trains.json"
        train = {"base": "0.10.0", "first_revision": revision, "ordering_major": 1}
        policy.write_text(json.dumps({target: train}))
        self.git("add", str(policy))
        self.git("commit", "-qm", "chore(release): open Tablet dev train")
        source = self.git("rev-parse", "HEAD").strip()
        self.spec.pop("reservation")
        self.spec["identity"] = "main"
        label, core = "0.10.0-dev.1", f"1.0.{revision}"
        badging = self.build / "badging"
        badging.write_text(badging.read_text().replace("versionCode='5'", f"versionCode='{revision}'")
                           .replace("1.2.0-incubator", label))
        manifest = self.build / "manifest"
        manifest.write_text(manifest.read_text().replace("1.2.0", core))
        self.spec_path.write_text(json.dumps(self.spec))
        self.cli("prepare", self.spec_path, "--output", self.output, "--aapt", self.aapt, "--apksigner", self.signer)
        self.sha = pipeline.digest(self.output / "receipt.json")
        self.cli("sign-receipt", self.output, "--key", self.key, "--receipt-sha256", self.sha)
        dev = pipeline.verify_jws(self.feed("dev"), pipeline.ANDROID_FEED_TYPE, self.public)
        self.assertEqual((dev["display_version"], dev["version"], dev["core_version"]), (label, label, core))
        self.assertTrue(dev["url"].endswith(f"/android-tablet-v{label}/app.apk"))
        for product, tag in (("biscuit", "biscuit-v0.10.0-dev.1"), ("android-assistant", "android-assistant-v0.10.0-dev.1")):
            self.assertEqual(product_versions.dev_tag_target(tag), product)
            self.assertIsNone(product_versions.dev_tag_target(tag.split("-dev.")[0]))

        plan = self.build / "stable-plan.json"
        plan.write_text(subprocess.check_output([sys.executable, "release/stable_plan.py", target, source, baseline,
                                                 "--initial", "0.10.0"], cwd=self.repo, text=True))
        promotion = self.build / "promotion.json"
        promotion.write_text(self.verified("stable-record", "--plan", plan).stdout)
        self.assertEqual(json.loads(promotion.read_bytes())["tag"], "android-tablet-v0.10.0")
        feed = self.build / "stable.jws"
        args = ("--channel", "stable", "--issued", self.now, "--expires", self.now + 3600,
                "--key", self.key, "--output", feed)
        self.verified("feed", *args, success=False)
        self.verified("feed", *args, "--promotion", promotion)
        stable = pipeline.verify_jws(feed, pipeline.ANDROID_FEED_TYPE, self.public)
        self.assertEqual(stable, {**dev, "channel": "stable", "display_version": "0.10.0"})
        stage = ("--channel", "stable", "--feed", feed, "--site", self.build / "site", "--promotion", promotion)
        self.verified("stage-feed", *stage)
        self.verified("stage-feed", *stage)  # Retry remains idempotent.
        promotion.write_text(json.dumps({**json.loads(promotion.read_bytes()), "update_sha256": "f" * 64}))
        self.verified("stage-feed", *stage, success=False)

        self.git("commit", "--allow-empty", "-qm", "docs: document cutover")
        later = self.git("rev-parse", "HEAD").strip()
        with patch.multiple(biscuit_dev, ROOT=self.repo, ANCHOR=legacy_source):
            product_versions.check_train_change(source, baseline)
            self.assertEqual(product_versions.product_identity(later, target)["version"], "0.10.0-dev.2")
            self.assertEqual(product_versions.product_identity(source, target)["version"], label)
            self.assertEqual(product_versions.product_identity(later, "biscuit")["version"], "0.1.4")
            policy.write_text(json.dumps({target: {**train, "base": "0.10.1", "first_revision": revision + 2}}))
            self.git("add", str(policy))
            self.git("commit", "-qm", "chore(release): open next Tablet dev train")
            next_train = self.git("rev-parse", "HEAD").strip()
            product_versions.check_train_change(next_train, later)
            self.assertEqual(product_versions.product_identity(next_train, target)["version"], "0.10.1-dev.1")

    def test_dev_plan_opens_train_even_if_legacy_release_has_the_same_base(self):
        legacy = self.source
        self.git("tag", "android-assistant-r1000000090", legacy)
        target = "android-assistant"
        policy = self.repo / "release/dev-trains.json"
        policy.write_text(json.dumps({target: {"base": "0.10.0", "first_revision": 1000000091, "ordering_major": 1}}))
        self.git("add", str(policy))
        self.git("commit", "-qm", "chore(release): open Assistant Dev train")
        source = self.git("rev-parse", "HEAD").strip()
        registry, _ = inventory()
        release = {"tag_name": "android-assistant-r1000000090", "immutable": True, "draft": False,
                   "prerelease": True, "assets": [{"name": name} for name in ("encore-satellite-assistant.apk", "SHA256SUMS")]}
        with (patch.object(dev_plan, "ROOT", self.repo), patch.object(check, "ROOT", self.repo),
              patch.object(biscuit_dev, "ROOT", self.repo),
              patch.object(biscuit_dev, "identity", side_effect=lambda sha: ("0.10.0", 1000000090 + (sha != legacy))),
              patch.object(dev_plan, "identity", return_value=("0.10.0", 1000000091))):
            result = dev_plan.plan(source, [release], registry, [target])
        self.assertEqual(result["targets"], [target])
        self.assertEqual(result["identities"][target]["version"], "0.10.0-dev.1")

    def test_stable_bumps_take_strongest_change_per_affected_product(self):
        def commit(subject, path):
            entry = self.repo / path
            entry.parent.mkdir(parents=True, exist_ok=True)
            entry.write_text(subject)
            self.git("add", path)
            self.git("commit", "-qm", subject)
            return self.git("rev-parse", "HEAD").strip()

        base = commit("chore: start Stable baseline", "README.md")
        contract = ("feat(sendspin): share playback controls\n\nRelease-Targets: android-assistant\n"
                    "Version-Bumps: android-generic=minor, biscuit=patch")
        commit(contract, "core/session/session.go")
        commit("fix(android): reconnect", "devices/android-generic/Host.kt")
        commit("feat(android): organize apps", "devices/android-generic/Host.kt")
        commit("feat(android): add a filter", "devices/android-generic/Host.kt")
        candidate = commit("test(sendspin): add fixtures", "core/session/session_test.go")
        breaking = commit("feat(android)!: replace profiles", "devices/android-generic/Host.kt")
        registry, _ = inventory()
        with (patch.object(dev_plan, "ROOT", self.repo), patch.object(check, "ROOT", self.repo),
              patch.multiple(biscuit_dev, ROOT=self.repo, ANCHOR=self.source)):
            def preview(product, source=candidate, baseline=base):
                return stable_plan.preview(product, source, baseline, registry, previous="0.10.4")

            tablet = preview("android-generic")
            self.assertEqual((tablet["bump"], tablet["version"], tablet["next_dev_base"]), ("minor", "0.11.0", "0.11.1"))
            self.assertEqual(tablet["review_commits"], [])
            self.assertEqual(preview("biscuit")["version"], "0.10.5")
            self.assertIsNone(preview("android-assistant")["version"])
            self.assertEqual(preview("android-generic", breaking, candidate)["version"], "1.0.0")
            self.assertIsNone(preview("biscuit", breaking, candidate)["version"])

    def test_selective_dev_plans_follow_each_published_source_and_stage_revision_gaps(self):
        def commit(subject, path):
            entry = self.repo / path
            entry.parent.mkdir(parents=True, exist_ok=True)
            entry.write_text(subject)
            self.git("add", path)
            self.git("commit", "-qm", subject)
            return self.git("rev-parse", "HEAD").strip()

        releases = []

        def publish(tag, source, draft=False):
            self.git("tag", tag, source)
            release = {"tag_name": tag, "immutable": not draft, "draft": draft,
                       "assets": [{"name": name} for name in ("receipt.json", "receipt.jws")]}
            releases.append(release)
            return release

        android, biscuit = "android-generic", "biscuit"
        base = commit("last lockstep build", "core/session/session.go")
        publish("android-0.1.2", base)
        publish("biscuit-0.1.2", base)
        docs = commit("docs: explain setup", "docs/setup.md")
        android_tip = commit("fix(android): handle reconnect", "devices/android-generic/app/Host.java")
        # A recovery tag and draft cannot establish a release baseline.
        android_release = publish("android-0.1.4", android_tip, draft=True)
        publish("android-generic-r1000000003", android_tip)  # Old tags cannot establish a baseline.
        biscuit_tip = commit("fix(biscuit): update kernel notice", "devices/biscuit/packaging/README.md")
        registry, _ = inventory("1.2.0")

        def plan(source):
            with (patch.object(biscuit_dev, "ROOT", self.repo), patch.object(check, "ROOT", self.repo),
                  patch.multiple(dev_plan, ROOT=self.repo, ANCHOR=self.source),
                  patch.object(biscuit_dev, "ANCHOR", self.source),
                  patch.object(biscuit_dev, "SEMVER_BASE", base)):
                return dev_plan.plan(source, releases, registry, [android, biscuit])

        docs_plan = plan(docs)
        self.assertEqual(docs_plan["targets"], [])
        self.assertEqual(docs_plan["changelog"].count("No build"), 3)
        android_plan = plan(android_tip)
        self.assertEqual(android_plan["targets"], [android])
        self.assertIn("## Biscuit\n\n- No build", android_plan["changelog"])
        self.assertEqual(plan(biscuit_tip)["targets"], [android, biscuit])  # Accumulated unpublished Android fix.
        android_release.update(immutable=True, draft=False)
        biscuit_plan = plan(biscuit_tip)
        self.assertEqual(biscuit_plan["targets"], [biscuit])
        self.assertIn("## Android\n\n- No build", biscuit_plan["changelog"])
        self.assertIn("### Fixes\n\n- Update kernel notice", biscuit_plan["changelog"])
        self.assertNotIn("handle reconnect", biscuit_plan["changelog"])
        publish("biscuit-0.1.5", biscuit_tip)
        self.assertEqual(plan(android_tip), android_plan)  # Same-source and later releases are ignored on replay.
        linux_tip = commit("fix(biscuit): update native OTA", "platforms/linuxupdate/verify.go")
        self.assertEqual(plan(linux_tip)["targets"], [biscuit])
        shared_tip = commit("fix(core): restore session state", "core/session/session.go")
        shared = plan(shared_tip)
        self.assertEqual(shared["targets"], [android, biscuit])
        self.assertEqual(shared["changelog"].count("- Restore session state"), 2)
        self.assertIn("- Update native OTA", shared["changelog"].split("## Biscuit", 1)[1])
        self.assertNotIn("Update native OTA", shared["changelog"].split("## Biscuit", 1)[0])
        self.assertIn("- Update native OTA", shared["target_changelogs"][biscuit])
        self.assertNotIn("Update native OTA", shared["target_changelogs"][android])
        publish("android-0.1.7", shared_tip)
        publish("biscuit-0.1.7", shared_tip)
        build_inputs = commit("fix(build): pin shared Go modules", "go.sum")
        self.assertEqual(plan(build_inputs)["targets"], [android, biscuit])
        minor_tip = commit("feat(core): next minor", "core/session/session.go")
        minor_plan = plan(minor_tip)
        self.assertEqual((minor_plan["version"], minor_plan["targets"]), ("0.2.0", [android, biscuit]))
        # If the feature's CI fails, the next green push still plans against
        # the last published release, not the test fix's parent.
        retry = plan(commit("test(core): stabilize CI", "core/session/session_test.go"))
        self.assertEqual((retry["version"], retry["targets"]), ("0.2.1", [android, biscuit]))
        for target in retry["targets"]:
            self.assertIn("### Features\n\n- Next minor", retry["target_changelogs"][target])
            self.assertNotIn("stabilize CI", retry["target_changelogs"][target])

        self.assertEqual((android_plan["version"], android_plan["revision"]), ("0.1.4", 1000000003))
        self.assertEqual((biscuit_plan["version"], biscuit_plan["revision"]), ("0.1.5", 1000000004))
        self.prepare()
        site = self.build / "site"
        receipts = {
            android: self.receipt,
            biscuit: {"target": biscuit, "backend": "incubator",
                      "artifact": {"name": "seed.zip"}, "ota": {"name": "ota.zip", "size": 200, "sha256": "2" * 64},
                      "compatibility": {"format": pipeline.NATIVE_STARTER, "base_sha256": "1" * 64}},
        }
        feeds = {}
        for offset, release in enumerate((plan(base), docs_plan, android_plan, biscuit_plan)):
            for target in release["targets"]:
                receipt = {**receipts[target], "source": release["source"], "core_version": release["version"],
                           "revision": release["revision"]}
                issued = self.now + offset
                feed = self.build / f"{target}-{issued}.jws"
                metadata = pipeline.metadata(receipt, "dev", issued, issued + 3600)
                kind = pipeline.ANDROID_FEED_TYPE if target == android else pipeline.ARM64_FEED_TYPE
                pipeline.sign(metadata, kind, self.key, feed)
                pipeline.stage_feed(receipt, feed, self.public, site, "dev", now=issued)
                feeds[target] = (metadata, feed.read_bytes())
            for metadata, previous in feeds.values():
                self.assertEqual((site / metadata["target"] / "incubator/dev.jws").read_bytes(), previous)
        self.assertEqual([feeds[target][0]["revision"] for target in (android, biscuit)], [1000000003, 1000000004])

    def test_assistant_uses_its_own_published_baseline_and_shared_audio_inputs(self):
        def commit(subject, *paths):
            for path in paths:
                file = self.repo / path
                file.parent.mkdir(parents=True, exist_ok=True)
                file.write_text(subject)
            self.git("add", *paths)
            self.git("commit", "-qm", subject)
            return self.git("rev-parse", "HEAD").strip()

        def published(tag, source, *assets, draft=False):
            self.git("tag", tag, source)
            return {"tag_name": tag, "immutable": not draft, "draft": draft,
                    "assets": [{"name": name} for name in assets]}

        assistant = "android-assistant"
        assistant_assets = ("encore-satellite-assistant.apk", "SHA256SUMS")
        commit("fix(core): old change before Assistant", "core/session/history.go")
        base = commit("fix(assistant): ship first mic host", "devices/android-assistant/build-android")
        releases = [published("android-assistant-r1", base, *assistant_assets)]
        assistant_tip = commit("fix(assistant): keep wake cue local", "devices/android-assistant/Mic.kt")
        releases.append(published("android-assistant-r2", assistant_tip, *assistant_assets, draft=True))
        # A later Tablet release cannot consume the unshipped Assistant fix.
        tablet_tip = commit("fix(tablet): redraw dashboard", "devices/android-generic/Tablet.kt")
        releases.append(published("android-0.1.4", tablet_tip, "receipt.json", "receipt.jws"))
        releases.append(published("biscuit-0.1.4", tablet_tip, "receipt.json", "receipt.jws"))
        mixed = commit("fix(tablet): change layout and adjust shared voice host",
                       "devices/android-generic/Layout.kt", "platforms/android/src/main/java/host/AudioIo.kt")
        commit("feat(tablet): improve display and voice\n\n"
               "Release-Note-android-assistant: remember the last microphone setting\n"
               "Release-Note-android-generic: restore the dashboard on launch",
               "devices/android-generic/Dashboard.kt", "platforms/android/src/main/java/host/AudioIo.kt")
        shared_tip = commit("fix(android): avoid clipping echo reference\n\nBREAKING CHANGE: reset audio settings",
                            "platforms/android/src/main/cpp/voice_aec.cpp")
        registry, _ = inventory()
        armed = list(dev_plan.TARGETS)
        with (patch.multiple(dev_plan, ROOT=self.repo, ANCHOR=self.source), patch.object(check, "ROOT", self.repo),
              patch.multiple(biscuit_dev, ROOT=self.repo, ANCHOR=self.source, SEMVER_BASE=base)):
            initial = dev_plan.plan(base, [], registry, armed)["target_changelogs"][assistant]
            self.assertIn("Ship first mic host", initial)
            self.assertNotIn("old change before Assistant", initial)
            self.assertEqual(dev_plan.plan(tablet_tip, releases, registry, armed)["targets"],
                             ["android-generic", "biscuit", assistant])
            result = dev_plan.plan(shared_tip, releases, registry, armed)
            self.assertEqual(result["targets"], ["android-generic", assistant])
            notes = result["target_changelogs"]
            self.assertIn("### Breaking changes\n\n- Avoid clipping echo reference", notes[assistant])
            self.assertIn("### Features\n\n- Remember the last microphone setting", notes[assistant])
            self.assertIn("### Fixes\n\n- Keep wake cue local", notes[assistant])
            self.assertIn(f"### Other changes\n\n- Changes spanning multiple products ([{mixed[:10]}]", notes[assistant])
            self.assertIn(f"/compare/{base}...{shared_tip}", notes[assistant])
            self.assertNotIn(".kt", notes[assistant])
            self.assertNotIn("Change layout", notes[assistant])
            self.assertNotIn("Restore the dashboard", notes[assistant])
            self.assertNotIn("Redraw dashboard", notes[assistant])
            self.assertIn("### Features\n\n- Restore the dashboard on launch", notes["android-generic"])
            self.assertNotIn("Remember the last microphone setting", notes["android-generic"])
            self.assertNotIn("Keep wake cue local", notes["android-generic"])
            self.assertEqual(dev_plan.assistant_changelog(shared_tip, releases, registry), notes[assistant])
            releases.append(published("android-assistant-r3", shared_tip, *assistant_assets))
            tablet_only = commit("fix(tablet): sharpen dashboard\n\nRelease-Targets: android-generic, biscuit",
                                 "devices/android-generic/Tablet.kt")
            self.assertEqual(dev_plan.plan(tablet_only, releases, registry, armed)["targets"], ["android-generic"])

            sendspin = commit("feat(sendspin): add Sendspin playback\n\nRelease-Targets: android-generic, biscuit",
                              "core/music.go", "devices/biscuit/Music.go", "devices/android-generic/Music.kt")
            result = dev_plan.plan(sendspin, releases, registry, armed)
            self.assertEqual(result["targets"], armed)  # Assistant uses the shared input and still builds.
            for target in ("android-generic", "biscuit"):
                features = result["target_changelogs"][target].partition("### Features\n\n")[2].split("\n\n", 1)[0]
                self.assertIn(f"- Add Sendspin playback ([{sendspin[:10]}]", features)
            self.assertNotIn(f"/commit/{sendspin}", result["target_changelogs"][assistant])

    def test_commit_check_grandfathers_base_but_rejects_invalid_history_at_valid_tip(self):
        resolver = self.repo / "release/biscuit_dev.py"

        def commit(message):
            self.git("commit", "--allow-empty", "-qm", message)
            return self.git("rev-parse", "HEAD").strip()

        def lint(base):
            return subprocess.run([sys.executable, resolver, "--check-commits", base, "HEAD"],
                                  cwd=self.repo, text=True, capture_output=True)

        commit("fix: keep the default route")
        commit("feat(voice)!: replace the voice adapter\n\nBREAKING CHANGE: clients must reconnect")
        result = lint(self.source)
        self.assertEqual(result.returncode, 0, result.stderr)
        invalid = commit("featt(voice): mistyped feature")
        commit("ci: check all newly introduced commits")
        result = lint(self.source)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(invalid[:7], result.stderr)
        self.assertEqual(lint(invalid).returncode, 0)

    def test_release_identity_advances_across_minor_and_major_versions(self):
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
            ("feat(core): next minor", "0.2.0 1000000005"),
            ("test(core): stabilize CI", "0.2.1 1000000006"),
            ("feat(core)!: next major", "1.0.0 1000000007"),
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
