"""Observable inventory and selection contracts; no external Python dependencies."""

import copy
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import biscuit_dev
import check
from test_fixtures import inventory


class BiscuitDevIdentityTests(unittest.TestCase):
    def test_first_parent_number_does_not_reuse_the_private_anchor(self):
        with self.assertRaisesRegex(ValueError, "first-parent version anchor"):
            biscuit_dev.identity(biscuit_dev.ANCHOR)
        self.assertEqual(biscuit_dev.identity("8f908eebf824ada2a029d1aaae89ea49fb3058cf"),
                         ("0.1.2", 1_000_000_001))
        self.assertEqual(biscuit_dev.identity("a8e1cfde9d04eab438862c5dfbffc2c53a2e2f28"),
                         ("0.1.6", 1_000_000_005))


class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.registry = json.loads((check.ROOT / "release/targets.json").read_text())

    def test_actual_inventory(self):
        check.validate(self.registry)
        check.validate_ledger(json.loads((check.ROOT / "release/ledger.json").read_text()), self.registry)
        self.assertEqual({t["id"] for t in self.registry["targets"]},
                         {"biscuit", "android-generic", "lab-linux-amd64"})

    def test_semver(self):
        for version in ("0.1.0-dev.0", "1.2.3", "1.2.3-rc.1+build.005", "1.0.0-0alpha"):
            with self.subTest(valid=version):
                self.registry["core_version"] = version
                check.validate(self.registry)
        for version in ("v1.2.3", "01.2.3", "1.2", "1.2.3-01", "1.2.3-rc..1", "1.2.3+"):
            with self.subTest(invalid=version):
                self.registry["core_version"] = version
                with self.assertRaises(ValueError):
                    check.validate(self.registry)

    def test_rejects_premature_releases_and_incomplete_dependencies(self):
        for field, value in (("release_supported", True), ("release_supported", 0),
                             ("package_revision", 0), ("package_revision", False),
                             ("status", "supported"), ("build", "make fake-package"),
                             ("depends_on", []), ("depends_on", ["devices/biscuit/"]),
                             ("depends_on", ["platforms/linux/", "../escape/"])):
            with self.subTest(field=field, value=value):
                registry = copy.deepcopy(self.registry)
                registry["targets"][1][field] = value
                with self.assertRaises(ValueError):
                    check.validate(registry)
        for mutate in (
            lambda r: r["targets"].append(copy.deepcopy(r["targets"][0])),
            lambda r: r["targets"].pop(0),
            lambda r: r.update(channels=["stable"]),
            lambda r: r.update(schema=True),
        ):
            registry = copy.deepcopy(self.registry)
            mutate(registry)
            with self.assertRaises(ValueError):
                check.validate(registry)


class LedgerTests(unittest.TestCase):
    def setUp(self):
        self.registry, self.ledger = inventory()

    def test_private_arm64_consumes_biscuit_revision_without_advancing_cohort(self):
        previous = copy.deepcopy(self.ledger)
        record = {"id": "private-arm64", "channel": "dev", "backend": "incubator",
                  "core_version": "0.1.1-dev.5", "revisions": {"biscuit": 19},
                  "private_arm64_zip_sha256": "ab" * 32}
        self.ledger["reservations"].append(record)
        self.registry["targets"][1]["package_revision"] = 19
        check.validate_ledger(self.ledger, self.registry, previous)
        for overrides in ({"private_arm64_zip_sha256": "invalid"}, {"revisions": {"biscuit": 18}},
                          {"revisions": {"biscuit": 19, "android-generic": 6}}):
            with self.subTest(overrides=overrides), self.assertRaises(ValueError):
                self.ledger["reservations"][-1] = {**record, **overrides}
                check.validate_ledger(self.ledger, self.registry, previous)

    def test_append_reserves_each_target_independently(self):
        previous = copy.deepcopy(self.ledger)
        record = copy.deepcopy(self.ledger["reservations"][-1])
        record.update(id="next-biscuit-package", revisions={"biscuit": 19})
        self.ledger["reservations"].append(record)
        self.registry["targets"][1]["package_revision"] = 19
        check.validate_ledger(self.ledger, self.registry, previous)
        for revision in (18, 17, 0, True):
            with self.subTest(revision=revision):
                record["revisions"]["biscuit"] = revision
                self.registry["targets"][1]["package_revision"] = revision
                with self.assertRaises(ValueError):
                    check.validate_ledger(self.ledger, self.registry, previous)

    def test_reservation_scope_and_inventory_must_agree(self):
        for field, value in (("channel", "stable"), ("channel", "beta"),
                             ("backend", "assist"), ("core_version", "0.1.0-dev.0"),
                             ("revisions", {"biscuit": 16}),
                             ("revisions", {"android-generic": 2100000001, "biscuit": 16}),
                             ("revisions", {"unknown": 1})):
            with self.subTest(field=field, value=value):
                ledger = copy.deepcopy(self.ledger)
                ledger["reservations"][0][field] = value
                with self.assertRaises(ValueError):
                    check.validate_ledger(ledger, self.registry)
        # A non-prerelease can be reserved for stable; no feed is published.
        self.registry["core_version"] = "0.1.1"
        self.ledger["reservations"][0].update(core_version="0.1.1", channel="stable")
        self.ledger["reservations"][-1]["core_version"] = "0.1.1"
        check.validate_ledger(self.ledger, self.registry)

    def test_existing_reservations_and_floors_cannot_be_rewritten(self):
        previous = copy.deepcopy(self.ledger)
        self.ledger["reservations"][0]["id"] = "renamed-old-reservation"
        with self.assertRaisesRegex(ValueError, "immutable"):
            check.validate_ledger(self.ledger, self.registry, previous)
        self.ledger = copy.deepcopy(previous)
        self.ledger["retired_revisions"]["biscuit"] = 14
        with self.assertRaisesRegex(ValueError, "immutable"):
            check.validate_ledger(self.ledger, self.registry, previous)


class ImpactTests(unittest.TestCase):
    def setUp(self):
        self.registry = check.validate(json.loads((check.ROOT / "release/targets.json").read_text()))
        self.all = sorted(t["id"] for t in self.registry["targets"])

    def assert_impact(self, paths, expected, unknown=()):
        self.assertEqual(check.select(paths, self.registry), {
            "affected_targets": sorted(expected), "package_targets": [],
            "unknown_paths": sorted(unknown), "lab_check": True,
        })

    def test_shared_protocol_dependencies_and_infrastructure(self):
        for path in ("core/audio/audio.go", "backends/incubator/server.go", "go.mod", "go.sum",
                     "Makefile", "tools/check_boundaries.py", ".agents/setup", ".amp/services.yaml",
                     ".github/workflows/check.yml", "release/targets.json", "updates/policy.json",
                     "build/toolchain.lock", "scripts/package.sh", "licenses/Techo5-MIT.txt",
                     "ACKNOWLEDGEMENTS.md"):
            with self.subTest(path=path):
                self.assert_impact([path], self.all)

    def test_platform_dependents_include_multiple_targets(self):
        extra = copy.deepcopy(self.registry["targets"][1])
        extra.update(id="future-linux",
                     depends_on=["platforms/linux/", "devices/future-linux/"])
        self.registry["targets"].append(extra)
        check.validate(self.registry)
        self.assert_impact(["platforms/linux/audio.go"], ["biscuit", "future-linux", "lab-linux-amd64"])
        self.assert_impact(["platforms/android/Host.java"], ["android-generic"])

    def test_device_only_and_union(self):
        self.assert_impact(["devices/biscuit/profile.json"], ["biscuit"])
        self.assert_impact(["devices/android-generic/package.gradle"], ["android-generic"])
        self.assert_impact(["devices/biscuit/profile.json", "platforms/android/Host.java"],
                           ["biscuit", "android-generic"])

    def test_lab_only_and_docs_only_keep_real_lab_check(self):
        self.assert_impact(["cmd/encore-lab/main.go", "lab/uv.lock"], ["lab-linux-amd64"])
        self.assert_impact(["README.md", "docs/foundation.md", "release/README.md",
                            "devices/biscuit/README.md"], [])
        self.assert_impact([], [])

    def test_unknown_unsafe_and_prefix_collisions_broaden(self):
        for path in ("devices/unregistered/profile.json", "platforms/unknown/a.go",
                     "devices/biscuit-extra/profile.json", "Dockerfile", "docs/device-packages/validate.py",
                     "../README.md", "/README.md", "a\\README.md"):
            with self.subTest(path=path):
                self.assert_impact([path], self.all, [path])


class GitCLITests(unittest.TestCase):
    def test_real_diff_rename_deletion_odd_names_and_missing_history(self):
        backup = check.ROOT / ".amp/in/orb-backup"
        backup.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=backup) as directory:
            root = Path(directory)
            (root / "release").mkdir()
            for name in ("check.py", "targets.json", "ledger.json"):
                shutil.copy(check.ROOT / "release" / name, root / "release" / name)

            def git(*args):
                return subprocess.check_output(["git", "-c", "user.name=CI Test", "-c",
                                                "user.email=ci@example.invalid", "-c",
                                                "commit.gpgsign=false", *args],
                                               cwd=root, text=True, stderr=subprocess.PIPE).strip()

            def commit():
                git("add", ".")
                git("commit", "-qm", "fixture")
                return git("rev-parse", "HEAD")

            def run(base, *args):
                result = subprocess.run([sys.executable, str(root / "release/check.py"),
                                         "--base", base, *args], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                return json.loads(result.stdout)

            git("init", "-q")
            old = root / "devices/biscuit/name with\nnewline.json"
            old.parent.mkdir(parents=True)
            old.write_text("{}")
            base = commit()
            new = root / "devices/android-generic/renamed.json"
            new.parent.mkdir(parents=True)
            old.rename(new)
            commit()
            output = root / "github-output"
            result = run(base, "--github-output", str(output))
            self.assertEqual(result["affected_targets"], ["android-generic", "biscuit"])
            self.assertEqual(result["unknown_paths"], [])
            self.assertIn('lab_check=true\n', output.read_text())
            self.assertIn('package_targets=[]\n', output.read_text())
            for missing in ("0" * 40, "not-a-revision"):
                result = run(missing)
                self.assertEqual(result["affected_targets"],
                                 ["android-generic", "biscuit", "lab-linux-amd64"])
                self.assertTrue(result["lab_check"])
                self.assertEqual(result["unknown_paths"], ["<unavailable diff>"])
            ledger_path = root / "release/ledger.json"
            ledger = json.loads(ledger_path.read_text())
            ledger["reservations"][0]["id"] = "rewritten-history"
            ledger_path.write_text(json.dumps(ledger))
            rejected = subprocess.run([sys.executable, str(root / "release/check.py"),
                                       "--base", base], capture_output=True, text=True)
            self.assertNotEqual(rejected.returncode, 0)
            self.assertIn("immutable", rejected.stderr)


if __name__ == "__main__":
    unittest.main()
