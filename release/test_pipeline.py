"""Offline public CLI contracts with disposable keys and synthetic source/bundles."""
import copy
import gzip
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
import zipfile

import pipeline


class PipelineTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        shutil.copytree(pipeline.ROOT / "release", self.repo / "release", ignore=shutil.ignore_patterns("__pycache__"))
        packaging = self.repo / "devices/biscuit/packaging"
        shutil.copytree(pipeline.ROOT / "devices/biscuit/packaging", packaging, ignore=shutil.ignore_patterns("__pycache__"))
        (self.repo / ".gitignore").write_text("/build/\n__pycache__/\n")
        # A non-prerelease fixture can exercise both channels without changing
        # the real ledger or pretending the first dev reservation is stable.
        for name in ("targets.json", "ledger.json"):
            path = self.repo / "release" / name
            path.write_text(path.read_text().replace("0.1.1-dev.1", "1.2.0"))
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Pipeline test")
        self.git("add", ".")
        self.git("commit", "-qm", "synthetic release source")
        self.source = self.git("rev-parse", "HEAD").strip()
        self.build = self.repo / "build"
        self.build.mkdir()
        for name in ("kernel-source.tar.gz", "sendspin-source.tar.gz", "alpine-source.tar.gz", "evidence.txt"):
            (self.build / name).write_bytes(b"synthetic fixture, not distributable sources\n")
        # Reuse the device's real loader fixture, not a parallel bundle schema.
        sys.path.insert(0, str(pipeline.ROOT / "devices/biscuit/packaging"))
        from test_package import write_bundle
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w") as tar:
            for name, data in {"sbin/init": b"init", "usr/local/bin/encore-satellite": b"app",
                               "etc/encore-base": b"encore-biscuit-v1\n", "etc/inittab": b"init",
                               "etc/encore-sendspin.json": json.dumps({"artifacts": {"source.tar.gz":
                                   pipeline.digest(self.build / "sendspin-source.tar.gz")}}).encode()}.items():
                info = tarfile.TarInfo(name)
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
        self.bundle = self.build / "bundle.zip"
        write_bundle(self.bundle, gzip.compress(archive.getvalue()), b"ramdisk", 18)
        self.rewrite_bundle(source_commit=self.source, source_dirty=False, core_version="1.2.0")
        self.spec = {"target": "biscuit", "reservation": "first-incubator-dev-biscuit-18",
                     "artifact": str(self.bundle), "evidence": str(self.build / "evidence.txt"),
                     "reproduced_artifact": str(self.build / "second-bundle.zip"),
                     "sources": {role: str(self.build / f"{role}-source.tar.gz") for role in ("kernel", "sendspin", "alpine")},
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

    def rewrite_bundle(self, **values):
        with zipfile.ZipFile(self.bundle) as archive:
            files = {name: archive.read(name) for name in archive.namelist()}
        kernel = json.loads(files["kernel.json"])
        kernel["files"]["source.tar.gz"] = pipeline.digest(self.build / "kernel-source.tar.gz")
        files["kernel.json"] = json.dumps(kernel).encode()
        manifest = json.loads(files["manifest.json"])
        manifest["files"]["kernel.json"] = hashlib.sha256(files["kernel.json"]).hexdigest()
        manifest.update(values)
        files["manifest.json"] = json.dumps(manifest).encode()
        with zipfile.ZipFile(self.bundle, "w") as archive:
            for name, data in files.items():
                archive.writestr(name, data)
        shutil.copyfile(self.bundle, self.build / "second-bundle.zip")

    def prepare(self):
        self.spec_path.write_text(json.dumps(self.spec))
        self.cli("prepare", self.spec_path, "--output", self.output)
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
        self.cli("prepare", self.spec_path, "--output", other)
        for name in ("encore-source.tar.gz", "receipt.json", "bundle.zip"):
            self.assertEqual((self.output / name).read_bytes(), (other / name).read_bytes())
        self.cli("prepare", self.spec_path, "--output", self.output, success=False)
        self.cli("sign-receipt", self.output, "--key", self.key, success=False)
        plan = json.loads(self.verified("publish").stdout)
        self.assertEqual(plan["mode"], "dry-run")
        self.assertIn("biscuit-r18", plan["commands"][0])
        self.assertIn(self.source, plan["commands"][0])
        self.assertNotIn("--clobber", str(plan))
        self.assertNotIn("dev.jws", str(plan))

    def test_tampered_or_incomplete_assets_and_receipts_fail(self):
        self.prepare()
        for name in ("bundle.zip", "kernel-source.tar.gz", "encore-source.tar.gz", "receipt.jws", "receipt.json"):
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
        (self.output / "alpine-source.tar.gz").unlink()
        self.verified("verify", success=False)

    def test_prepare_rejects_dirty_wrong_source_revision_and_missing_sources(self):
        self.spec_path.write_text(json.dumps(self.spec))
        for values in ({"source_dirty": True}, {"source_commit": "b" * 40}, {"package_revision": 17}, {"core_version": "1.1.9"}):
            with self.subTest(values=values):
                self.rewrite_bundle(source_dirty=False, source_commit=self.source, package_revision=18, core_version="1.2.0")
                self.rewrite_bundle(**values)
                self.cli("prepare", self.spec_path, "--output", self.output, success=False)
        (self.repo / "uncommitted").write_text("dirty")
        self.cli("prepare", self.spec_path, "--output", self.output, success=False)
        (self.repo / "uncommitted").unlink()
        self.rewrite_bundle(source_dirty=False, source_commit=self.source, package_revision=18, core_version="1.2.0")
        repeated = self.build / "second-bundle.zip"
        repeated.write_bytes(b"non-reproducible assembly")
        self.cli("prepare", self.spec_path, "--output", self.output, success=False)
        shutil.copyfile(self.bundle, repeated)
        for role in ("kernel", "sendspin"):
            path = self.build / f"{role}-source.tar.gz"
            original = path.read_bytes()
            path.write_bytes(b"different source kit")
            self.cli("prepare", self.spec_path, "--output", self.output, success=False)
            path.write_bytes(original)
        del self.spec["sources"]["alpine"]
        self.spec_path.write_text(json.dumps(self.spec))
        self.cli("prepare", self.spec_path, "--output", self.output, success=False)

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
        self.assertEqual((self.build / "site/biscuit/incubator/stable.jws").read_bytes(), stable.read_bytes())
        self.assertEqual(len(list((self.build / "site/history/biscuit/dev").iterdir())), 2)
        current = self.build / "site/biscuit/incubator/dev.jws"
        current.unlink()
        self.stage(dev, "dev", success=False)  # Deletion cannot reset rollback protection.
        shutil.copyfile(dev, current)
        self.stage(dev, "dev", success=False)  # Nor can restoring an older signed entry.

    def test_signed_wrong_digest_expiry_root_and_downgrade_are_rejected(self):
        self.prepare()
        good = self.feed("dev")
        self.stage(good, "dev")
        original = pipeline.verify_jws(good, pipeline.FEED_TYPES["biscuit"], self.public)
        for changes in ({"sha256": "0" * 64}, {"issued": self.now - 7200, "expires": self.now - 3600},
                        {"issued": self.now + 3600, "expires": self.now + 7200}, {"channel": "stable"},
                        {"revision": 19}, {"url": "http://example.invalid/latest"}):
            with self.subTest(changes=changes):
                invalid = self.build / "invalid.jws"
                changed = {**original, **changes}
                if invalid.exists():
                    invalid.unlink()
                pipeline.sign(changed, pipeline.FEED_TYPES["biscuit"], self.key, invalid)
                self.stage(invalid, "dev", success=False)
        old = self.build / "site/biscuit/incubator/dev.jws"
        pipeline.sign({**original, "revision": 20}, pipeline.FEED_TYPES["biscuit"], self.key, self.build / "newer.jws")
        shutil.copyfile(self.build / "newer.jws", old)
        shutil.copyfile(old, self.build / f"site/history/biscuit/dev/{original['issued']}.jws")
        self.stage(self.feed("dev", self.now + 2), "dev", success=False)
        key = self.root / "wrong.pem"
        subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(key)], check=True, capture_output=True)
        wrong = self.build / "wrong.jws"
        pipeline.sign(original, pipeline.FEED_TYPES["biscuit"], key, wrong)
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
        previous = pipeline.verify_jws(good, pipeline.FEED_TYPES["biscuit"], self.public)
        previous.update(revision=17, core_version="1.10.0", issued=self.now - 1)
        site = self.build / "site/biscuit/incubator"
        site.mkdir(parents=True)
        pipeline.sign(previous, pipeline.FEED_TYPES["biscuit"], self.key, site / "dev.jws")
        history = self.build / "site/history/biscuit/dev"
        history.mkdir(parents=True)
        shutil.copyfile(site / "dev.jws", history / f"{previous['issued']}.jws")
        self.stage(good, "dev", success=False)

    def test_android_inspection_contract_and_signed_feed(self):
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
        self.spec.update(target="android-generic", reservation="first-incubator-dev", artifact=str(apk),
                         reproduced_artifact=str(self.build / "second.apk"),
                         certificate_sha256=cert, sources={"dependencies": str(self.build / "alpine-source.tar.gz")})
        self.spec_path.write_text(json.dumps(self.spec))
        self.cli("prepare", self.spec_path, "--output", self.output, "--aapt", aapt, "--apksigner", signer)
        self.cli("sign-receipt", self.output, "--key", self.key)
        self.sha = pipeline.digest(self.output / "receipt.json")
        self.verified("verify")
        feed = self.feed("dev")
        self.stage(feed, "dev")
        payload = pipeline.verify_jws(feed, pipeline.FEED_TYPES["android-generic"], self.public)
        self.assertEqual((payload["source"], payload["revision"], payload["certificate_sha256"]), (self.source, 5, cert))
        for name, data in (("badging", badging + "application-debuggable\n"),
                           ("manifest", manifest.replace("1.2.0", "1.1.9")),
                           ("cert", f"Signer #1 certificate SHA-256 digest: {'8' * 64}\n")):
            path = self.build / name
            original = path.read_text()
            path.write_text(data)
            self.cli("prepare", self.spec_path, "--output", self.build / "rejected", "--aapt", aapt, "--apksigner", signer, success=False)
            path.write_text(original)

    def test_offline_cohort_handoff_no_rebuild_and_publication_dry_run(self):
        import dev
        # Synthetic SDK output only; no real APK/owner signing keys are used.
        for name in dev.dependency_paths():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(pipeline.ROOT / name, path)
        hashes = json.loads(subprocess.check_output([sys.executable, "release/dev.py", "dependency-hashes"], cwd=self.repo))
        files = {role: {"name": role + ".tar.gz", "size": 1, "sha256": "1" * 64,
                        "url": f"https://github.com/farooqu/encore-satellite/releases/download/fixture-inputs/{role}.tar.gz"}
                 for role in ("base", "kernel", "sendspin", "alpine", "android-dependencies")}
        (self.repo / "release/dev-automation.json").write_text(json.dumps({"schema": 1, "builds_enabled": True,
            "publishing_enabled": False, "metadata_signing": "offline",
            "distribution_review": "synthetic fixture only", "inputs": {"dependency_sha256": hashes, "files": files}}))
        self.git("add", ".")
        self.git("commit", "-qm", "synthetic main change")
        parent = self.git("rev-parse", "HEAD").strip()
        version, reservation = "1.2.1-dev.1", "main-dev-" + parent
        registry = json.loads((self.repo / "release/targets.json").read_text())
        registry["core_version"] = version
        for target in registry["targets"]:
            if target["id"] in dev.TARGETS:
                target["package_revision"] += 1
        ledger = json.loads((self.repo / "release/ledger.json").read_text())
        ledger["reservations"].append({"id": reservation, "core_version": version, "backend": "incubator",
                                       "channel": "dev", "revisions": {"biscuit": 19, "android-generic": 6}, "source_commit": parent})
        (self.repo / "release/targets.json").write_text(json.dumps(registry))
        (self.repo / "release/ledger.json").write_text(json.dumps(ledger))
        changelog = self.repo / f"release/changelogs/{version}.md"
        changelog.parent.mkdir()
        changelog.write_text("# Immutable source changelog fixture\n")
        self.git("add", ".")
        self.git("commit", "-qm", "Prepare cohort\n\nEncore-Dev-Source: " + parent)
        self.source = self.git("rev-parse", "HEAD").strip()
        self.rewrite_bundle(source_commit=self.source, package_revision=19, core_version=version)
        self.spec["reservation"] = reservation
        self.spec_path.write_text(json.dumps(self.spec))
        cohort = self.build / "cohort"
        self.cli("prepare", self.spec_path, "--output", cohort / "biscuit")
        apk = self.build / "fixture.apk"
        apk.write_bytes(b"synthetic APK, not a real signed build")
        shutil.copyfile(apk, self.build / "second.apk")
        aapt, signer = self.build / "aapt", self.build / "apksigner"
        cert = "e" * 64
        badging = "package: name='com.encore.satellite.incubator' versionCode='6' versionName='" + version + "'\nsdkVersion:'26'\nnative-code: 'armeabi-v7a' 'arm64-v8a'\n"
        manifest = 'A: android:name="com.encore.core_version"\n  A: android:value="' + version + '"\n'
        aapt.write_text("#!/usr/bin/env python3\nimport sys\nprint(" + repr(badging) + " if sys.argv[2] == 'badging' else " + repr(manifest) + ")\n")
        signer.write_text("#!/usr/bin/env python3\nprint('Signer #1 certificate SHA-256 digest: " + cert + "')\n")
        aapt.chmod(0o755)
        signer.chmod(0o755)
        self.spec.update(target="android-generic", artifact=str(apk), reproduced_artifact=str(self.build / "second.apk"),
                         certificate_sha256=cert, sources={"dependencies": str(self.build / "alpine-source.tar.gz")})
        self.spec_path.write_text(json.dumps(self.spec))
        self.cli("prepare", self.spec_path, "--output", cohort / "android-generic", "--aapt", aapt, "--apksigner", signer)
        handoff = {"schema": 1, "source": self.source,
                   "receipts": {target: pipeline.digest(cohort / target / "receipt.json") for target in dev.TARGETS}}
        (cohort / "handoff.json").write_bytes(pipeline.canonical(handoff) + b"\n")
        handoff_sha = hashlib.sha256((cohort / "handoff.json").read_bytes()).hexdigest()
        pristine = self.build / "unsigned"
        shutil.copytree(cohort, pristine)
        before = {path.relative_to(cohort): path.read_bytes() for path in cohort.rglob("*") if path.is_file()}
        args = [sys.executable, "release/dev.py", "sign", str(cohort), "--handoff-sha256", handoff_sha,
                "--aapt", str(aapt), "--apksigner", str(signer), "--channel", "dev", "--expires", str(self.now + 86400)]
        retain = [sys.executable, "release/dev.py", "retain", str(cohort), "--handoff-sha256", handoff_sha]
        for target in dev.TARGETS:
            args += [f"--{target}-key", str(self.key), f"--{target}-public", str(self.public)]
            retain += [f"--{target}-public", str(self.public)]
        # Competing implementation: silently recompile or fetch during signing.
        # Make any build/download/publication tool fail and leave evidence.
        blocked = self.build / "blocked"
        blocked.mkdir()
        attempted = self.build / "unexpected-tool"
        for command in ("gh", "curl", "make", "go", "java", "gradle", "sdkmanager"):
            tool = blocked / command
            tool.write_text(f"#!/bin/sh\nprintf '%s' '{command}' >> '{attempted}'\nexit 91\n")
            tool.chmod(0o755)
        environment = {**os.environ, "EXPECTED_CERTIFICATE": cert, "PATH": str(blocked) + os.pathsep + os.environ["PATH"]}
        result = subprocess.run(args, cwd=self.repo, env=environment, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual({name: (cohort / name).read_bytes() for name in before}, before)
        result = subprocess.run(retain, cwd=self.repo, env=environment, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout.count('"mode": "dry-run"'), 2)
        self.assertIn("Immutable source changelog fixture", result.stdout)
        for target, revision in (("biscuit", 19), ("android-generic", 6)):
            self.assertIn(f"{target}-r{revision}", result.stdout)
            payload = pipeline.verify_jws(cohort / f"{target}-dev.jws", pipeline.FEED_TYPES[target], self.public)
            self.assertEqual((payload["channel"], payload["core_version"], payload["revision"]), ("dev", version, revision))
            self.assertFalse((cohort / f"{target}-stable.jws").exists())
        refused = subprocess.run(retain + ["--execute"], cwd=self.repo, env=environment, capture_output=True, text=True)
        self.assertNotEqual(refused.returncode, 0)
        self.assertIn("publishing is disabled", refused.stderr)
        # Tampered second target must prevent even planning the first upload.
        (cohort / "android-generic/fixture.apk").write_bytes(b"tampered APK")
        refused = subprocess.run(retain, cwd=self.repo, env=environment, capture_output=True, text=True)
        self.assertNotEqual(refused.returncode, 0)
        self.assertIn("asset changed: fixture.apk", refused.stderr)
        self.assertNotIn('"commands"', refused.stdout)
        # The offline signer must also reject the entire unsigned cohort before
        # producing any signature, including hash/certificate/channel failures.
        for failure in ("artifact", "handoff", "certificate", "extra", "stable"):
            shutil.rmtree(cohort)
            shutil.copytree(pristine, cohort)
            bad_args, bad_environment = list(args), dict(environment)
            if failure == "artifact":
                (cohort / "android-generic/fixture.apk").write_bytes(b"tampered")
            elif failure == "handoff":
                (cohort / "handoff.json").write_text("{}")
            elif failure == "certificate":
                bad_environment["EXPECTED_CERTIFICATE"] = "f" * 64
            elif failure == "extra":
                (cohort / "unreviewed-file").write_text("do not transfer unrecorded files")
            else:
                bad_args[bad_args.index("dev")] = "stable"
            refused = subprocess.run(bad_args, cwd=self.repo, env=bad_environment, capture_output=True, text=True)
            self.assertNotEqual(refused.returncode, 0, failure)
            self.assertFalse(list(cohort.rglob("*.jws")), failure)
        self.assertFalse(attempted.exists())


if __name__ == "__main__":
    unittest.main()
