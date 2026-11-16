"""Observable inventory and selection contracts; no external Python dependencies."""

import copy
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import check


class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.registry = json.loads((check.ROOT / "release/targets.json").read_text())

    def test_actual_foundation(self):
        check.validate(self.registry)
        self.assertEqual({t["id"] for t in self.registry["targets"]},
                         {"echo-dot-2", "android-generic", "lab-linux-amd64"})

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
                             ("package_revision", 1), ("package_revision", False),
                             ("status", "supported"), ("build", "make fake-package"),
                             ("depends_on", []), ("depends_on", ["devices/echo-dot-2/"]),
                             ("depends_on", ["platforms/echo/", "../escape/"])):
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
        for path in ("core/audio/audio.go", "backends/assist/server.go", "go.mod", "go.sum",
                     "Makefile", "tools/check_boundaries.py", ".agents/setup", ".amp/services.yaml",
                     ".github/workflows/check.yml", "release/targets.json", "updates/policy.json",
                     "build/toolchain.lock", "scripts/package.sh", "licenses/Techo5-MIT.txt",
                     "ACKNOWLEDGEMENTS.md"):
            with self.subTest(path=path):
                self.assert_impact([path], self.all)

    def test_platform_dependents_include_multiple_targets(self):
        extra = copy.deepcopy(self.registry["targets"][1])
        extra.update(id="future-echo", depends_on=["platforms/echo/", "devices/future-echo/"])
        self.registry["targets"].append(extra)
        check.validate(self.registry)
        self.assert_impact(["platforms/echo/audio.go"], ["echo-dot-2", "future-echo"])
        self.assert_impact(["platforms/android/Host.java"], ["android-generic"])

    def test_device_only_and_union(self):
        self.assert_impact(["devices/echo-dot-2/profile.json"], ["echo-dot-2"])
        self.assert_impact(["devices/android-generic/package.gradle"], ["android-generic"])
        self.assert_impact(["devices/echo-dot-2/profile.json", "platforms/android/Host.java"],
                           ["echo-dot-2", "android-generic"])

    def test_lab_only_and_docs_only_keep_real_lab_check(self):
        self.assert_impact(["cmd/encore-lab/main.go", "lab/uv.lock"], ["lab-linux-amd64"])
        self.assert_impact(["README.md", "docs/foundation.md", "release/README.md",
                            "devices/echo-dot-2/README.md"], [])
        self.assert_impact([], [])

    def test_unknown_unsafe_and_prefix_collisions_broaden(self):
        for path in ("devices/unregistered/profile.json", "platforms/unknown/a.go",
                     "devices/echo-dot-20/profile.json", "Dockerfile", "docs/device-packages/validate.py",
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
            for name in ("check.py", "targets.json"):
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
            old = root / "devices/echo-dot-2/name with\nnewline.json"
            old.parent.mkdir(parents=True)
            old.write_text("{}")
            base = commit()
            new = root / "devices/android-generic/renamed.json"
            new.parent.mkdir(parents=True)
            old.rename(new)
            commit()
            output = root / "github-output"
            result = run(base, "--github-output", str(output))
            self.assertEqual(result["affected_targets"], ["android-generic", "echo-dot-2"])
            self.assertEqual(result["unknown_paths"], [])
            self.assertIn('lab_check=true\n', output.read_text())
            self.assertIn('package_targets=[]\n', output.read_text())
            for missing in ("0" * 40, "not-a-revision"):
                result = run(missing)
                self.assertEqual(result["affected_targets"],
                                 ["android-generic", "echo-dot-2", "lab-linux-amd64"])
                self.assertTrue(result["lab_check"])
                self.assertEqual(result["unknown_paths"], ["<unavailable diff>"])


if __name__ == "__main__":
    unittest.main()
