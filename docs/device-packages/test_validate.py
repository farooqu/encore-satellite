"""Exercise the planning-profile check through its command-line interface."""

import copy
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


class PackageContractTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        script = self.root / "docs/device-packages/validate.py"
        script.parent.mkdir(parents=True)
        shutil.copyfile(ROOT / "docs/device-packages/validate.py", script)
        self.script = script
        self.profiles = {}
        for target in ("biscuit", "android-generic"):
            path = self.root / "devices" / target / "profile.json"
            path.parent.mkdir(parents=True)
            shutil.copyfile(ROOT / "devices" / target / "profile.json", path)
            self.profiles[target] = json.loads(path.read_text())
        (self.root / "release").mkdir()
        # Deliberately minimal registry fixture, independent of the validator.
        self.registry = {"targets": [
            {"id": "biscuit", "status": "planned", "package_revision": 0,
             "release_supported": False},
            {"id": "android-generic", "status": "planned", "package_revision": 0,
             "release_supported": False},
        ]}
        self.write_registry(self.registry)

    def write_registry(self, registry):
        (self.root / "release/targets.json").write_text(json.dumps(registry))

    def run_check(self, valid):
        result = subprocess.run([sys.executable, str(self.script)], cwd=self.root,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0 if valid else 1, result.stderr)
        if valid:
            self.assertIn("2 planned device profiles; no installable packages", result.stdout)
        else:
            self.assertIn("Device package validation failed:", result.stderr)

    def test_planned_profiles_and_registry_extensions(self):
        self.run_check(True)
        # Central release metadata may grow without widening this planning contract.
        self.registry["channels"] = ["dev", "stable"]
        self.registry["targets"][0]["depends_on"] = ["platforms/linux/", "devices/biscuit/"]
        self.registry["targets"].append({"id": "lab-linux-amd64", "status": "test-fixture"})
        self.write_registry(self.registry)
        self.run_check(True)

    def test_rejects_unknown_fields_and_wrong_identity_on_either_target(self):
        for target, original in self.profiles.items():
            path = self.root / "devices" / target / "profile.json"
            for patch in (
                {"target": "android-generic" if target == "biscuit" else "biscuit"},
                {"status": "released"},
                {"schema": True},
                {"schema": 1.0},
                {"schema": 2},
                {"audio_tuning": {"equalizer": "bypass"}},
                {"advertised_capabilities": []},
                {"build": "pretend-build"},
                {"package_revision": 1},
            ):
                with self.subTest(target=target, patch=patch):
                    path.write_text(json.dumps({**original, **patch}))
                    self.run_check(False)
            for key in original:
                with self.subTest(target=target, missing=key):
                    path.write_text(json.dumps({k: v for k, v in original.items() if k != key}))
                    self.run_check(False)
            path.write_text(json.dumps(original))

    def test_rejects_malformed_duplicate_and_missing_profile(self):
        path = self.root / "devices/android-generic/profile.json"
        duplicate = json.dumps(self.profiles["android-generic"])[:-1] + ', "schema": 1}'
        for value in ("{", "[]", "null", duplicate):
            with self.subTest(value=value):
                path.write_text(value)
                self.run_check(False)
        path.unlink()
        self.run_check(False)

    def test_rejects_registry_support_or_identity_drift_on_either_target(self):
        for index in range(2):
            for patch in ({"status": "supported"}, {"release_supported": True},
                          {"release_supported": 0}, {"package_revision": 1},
                          {"package_revision": False}, {"package_revision": 0.0},
                          {"id": "other-target"}):
                with self.subTest(index=index, patch=patch):
                    registry = copy.deepcopy(self.registry)
                    registry["targets"][index].update(patch)
                    self.write_registry(registry)
                    self.run_check(False)
        self.registry["targets"].append(copy.deepcopy(self.registry["targets"][0]))
        self.write_registry(self.registry)
        self.run_check(False)


if __name__ == "__main__":
    unittest.main()
