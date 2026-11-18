"""Main-driven planning/allocation CLI, with isolated Git histories and no uploads."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import dev
import pipeline


class DevTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        shutil.copytree(pipeline.ROOT / "release", self.repo / "release", ignore=shutil.ignore_patterns("__pycache__"))
        for name in dev.dependency_paths():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(pipeline.ROOT / name, path)
        (self.repo / ".gitignore").write_text("/build/\n__pycache__/\n")
        self.git("init", "-q")
        self.git("config", "user.name", "Dev fixture")
        self.git("config", "user.email", "test@example.invalid")
        self.commit("baseline")
        self.base = self.git("rev-parse", "HEAD")
        self.git("tag", "biscuit-r18")
        self.git("tag", "android-generic-r5")
        self.releases = [{"tag_name": "biscuit-r18", "draft": False, "immutable": True},
                         {"tag_name": "android-generic-r5", "draft": False, "immutable": True}]
        self.build = self.repo / "build"
        self.build.mkdir()
        self.registry = json.loads((self.repo / "release/targets.json").read_text())
        self.ledger = json.loads((self.repo / "release/ledger.json").read_text())

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, stderr=subprocess.PIPE, text=True).strip()

    def commit(self, subject):
        self.git("add", ".")
        self.git("commit", "-qm", subject)
        return self.git("rev-parse", "HEAD")

    def change(self, path, subject):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(subject + "\n")
        return self.commit(subject)

    def cli(self, *args, success=True):
        result = subprocess.run([sys.executable, "release/dev.py", *map(str, args)], cwd=self.repo, capture_output=True, text=True)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return result

    def plan(self, runs=None):
        release_file = self.build / "releases.json"
        release_file.write_text(json.dumps(self.releases))
        arguments = ["plan", "--releases", release_file, "--output", self.build / "plan", "--github-output", self.build / "outputs"]
        if runs is not None:
            for name, data in zip(("foundation", "android"), runs):
                (self.build / f"{name}.json").write_text(json.dumps({"workflow_runs": data}))
            arguments += ["--ci-runs", self.build / "foundation.json", self.build / "android.json"]
        self.cli(*arguments)
        return json.loads((self.build / "plan/plan.json").read_text())

    def enable_fixture(self):
        hashes = json.loads(self.cli("dependency-hashes").stdout)
        files = {role: {"name": role + ".tar.gz", "size": 1, "sha256": "1" * 64,
                        "url": f"https://github.com/farooqu/encore-satellite/releases/download/fixture-inputs/{role}.tar.gz"}
                 for role in ("base", "kernel", "sendspin", "alpine", "android-dependencies")}
        policy = {"schema": 1, "builds_enabled": True, "publishing_enabled": False, "metadata_signing": "offline",
                  "distribution_review": "synthetic fixture, no distribution",
                  "inputs": {"dependency_sha256": hashes, "files": files}}
        (self.repo / "release/dev-automation.json").write_text(json.dumps(policy))
        self.commit("approve synthetic setup")

    def test_disabled_preview_target_changelog_and_no_real_allocation(self):
        android = self.change("platforms/android/example.kt", "Android fix [link]")
        self.change("README.md", "docs only")
        self.change("lab/example.py", "lab only")
        value = self.plan()
        self.assertTrue(value["eligible"])
        self.assertEqual(value["core_version"], "0.1.1-dev.2")
        self.assertEqual(value["revisions"], {"biscuit": 19, "android-generic": 6})
        self.assertEqual(value["changes"]["biscuit"], [])
        self.assertEqual(value["changes"]["android-generic"], [{"source": android, "subject": "Android fix [link]"}])
        self.assertIn(r"Android fix \[link\]", (self.build / "plan/changelog.md").read_text())
        self.assertIn("enabled=false", (self.build / "outputs").read_text())
        self.cli("apply", self.build / "plan/plan.json", success=False)
        self.assertEqual(json.loads((self.repo / "release/ledger.json").read_text()), self.ledger)
        self.assertFalse((self.repo / "release/changelogs").exists())

    def test_docs_lab_skip_and_failed_unpublished_changes_are_not_lost(self):
        self.change("docs/info.md", "docs")
        self.change("cmd/encore-lab/example.go", "lab")
        self.assertFalse(self.plan()["eligible"])
        source = self.change("core/example.go", "shared fix from failed candidate")
        self.git("tag", "biscuit-r19")
        self.releases += [{"tag_name": "biscuit-r19", "draft": True, "immutable": False}]
        self.change("README.md", "docs after failed candidate")
        value = self.plan()
        for target in dev.TARGETS:
            self.assertEqual(value["changes"][target][0]["source"], source)
            self.assertEqual(value["previous"][target], self.base)

    def test_append_only_allocation_rejects_stale_plans_and_avoids_bot_loop(self):
        self.enable_fixture()
        value = self.plan()
        self.assertFalse(value["publishing_enabled"])
        self.assertTrue(value["builds_enabled"])
        self.cli("apply", self.build / "plan/plan.json")
        registry = json.loads((self.repo / "release/targets.json").read_text())
        ledger = json.loads((self.repo / "release/ledger.json").read_text())
        self.assertEqual(ledger["reservations"][:-1], self.ledger["reservations"])
        self.assertEqual(ledger["retired_revisions"], self.ledger["retired_revisions"])
        self.assertEqual(ledger["reservations"][-1]["revisions"], {"biscuit": 19, "android-generic": 6})
        self.assertEqual(registry["core_version"], "0.1.1-dev.2")
        self.assertEqual(registry["targets"][0], self.registry["targets"][0])
        self.assertTrue(all(not target["release_supported"] for target in registry["targets"]))
        self.assertEqual((self.repo / "release/changelogs/0.1.1-dev.2.md").read_text(), (self.build / "plan/changelog.md").read_text())
        self.commit("Prepare main dev cohort\n\nEncore-Dev-Source: " + value["source"])
        self.assertFalse(self.plan()["eligible"])
        self.cli("apply", self.build / "plan/plan.json", success=False)
        # A spoofed trailer cannot hide actual code changes.
        head = self.git("rev-parse", "HEAD")
        self.change("devices/biscuit/example.go", "code plus spoofed trailer\n\nEncore-Dev-Source: " + head)
        self.assertTrue(self.plan()["eligible"])
        # A source change after planning must never inherit the old reservation.
        self.change("README.md", "concurrent main push")
        self.cli("apply", self.build / "plan/plan.json", success=False)

    def test_ci_gate_demands_latest_success_on_exact_same_repository_main_push(self):
        self.enable_fixture()
        head = self.git("rev-parse", "HEAD")
        good = {"head_sha": head, "event": "push", "head_branch": "main", "head_repository": {"full_name": pipeline.REPOSITORY},
                "run_number": 5, "run_attempt": 1, "status": "completed", "conclusion": "success"}
        self.plan([[good], [good]])
        self.assertTrue((self.build / "outputs").read_text().endswith("enabled=true\n"))
        for field, wrong in (("head_sha", self.base), ("event", "pull_request"), ("head_branch", "feature"),
                             ("head_repository", {"full_name": "fork/repo"}), ("status", "in_progress"), ("conclusion", "failure")):
            bad = {**good, field: wrong}
            self.plan([[good], [bad]])
            self.assertTrue((self.build / "outputs").read_text().endswith("enabled=false\n"), field)
        retry = {**good, "run_attempt": 2, "conclusion": "cancelled"}
        self.plan([[good], [good, retry]])
        self.assertTrue((self.build / "outputs").read_text().endswith("enabled=false\n"))
        self.plan([[], [good]])
        self.assertTrue((self.build / "outputs").read_text().endswith("enabled=false\n"))

    def test_changed_dependency_sources_and_final_release_line_fail_closed(self):
        self.enable_fixture()
        self.change("go.sum", "new dependency requires corresponding sources")
        self.plan()
        self.cli("apply", self.build / "plan/plan.json", success=False)
        self.assertEqual(json.loads((self.repo / "release/ledger.json").read_text()), self.ledger)
        # No guessed next patch/minor after a final candidate: explicitly choose
        # the next dev line using the same inventory/ledger protocol.
        for name in ("targets.json", "ledger.json"):
            path = self.repo / "release" / name
            path.write_text(path.read_text().replace("0.1.1-dev.1", "0.1.1"))
        self.commit("final core fixture")
        self.cli("plan", "--output", self.build / "final", success=False)

    def test_initial_plan_uses_existing_split_reservations_and_exact_clean_source(self):
        self.enable_fixture()
        for target, revision in (("biscuit", 18), ("android-generic", 5)):
            self.git("tag", "-d", f"{target}-r{revision}")
        source_tree = self.build / "source"
        self.git("worktree", "add", "--detach", str(source_tree), self.base)
        descriptor = {"schema": 1, "source": self.base, "core_version": "0.1.1-dev.1",
                      "revisions": {"biscuit": 18, "android-generic": 5},
                      "reservations": {"biscuit": "first-incubator-dev-biscuit-18", "android-generic": "first-incubator-dev"},
                      "expected_artifact_sha256": {"biscuit": "e" * 64, "android-generic": None}}
        descriptor_path = self.repo / "release/initial-dev.json"
        descriptor_path.write_text(json.dumps(descriptor))
        self.commit("pin existing initial reservations")
        good = {"head_sha": self.base, "event": "push", "head_branch": "main",
                "head_repository": {"full_name": pipeline.REPOSITORY}, "run_number": 7,
                "run_attempt": 1, "status": "completed", "conclusion": "success"}
        releases = self.build / "initial-releases.json"
        releases.write_text("[]")
        runs = [self.build / f"initial-{name}.json" for name in ("foundation", "android")]
        for path in runs:
            path.write_text(json.dumps({"workflow_runs": [good]}))
        output = self.build / "initial-plan"
        args = ["initial-plan", "--source-tree", source_tree, "--releases", releases,
                "--ci-runs", *runs, "--output", output]
        self.cli(*args)
        value = json.loads((output / "plan.json").read_text())
        for key in descriptor:
            self.assertEqual(value[key], descriptor[key])
        self.assertEqual(value["builder_source"], self.git("rev-parse", "HEAD"))
        self.assertNotEqual(value["builder_source"], value["source"])
        self.assertIn("Revision: 18", (output / "changelog.md").read_text())
        self.assertIn("Revision: 5", (output / "changelog.md").read_text())
        self.assertEqual(json.loads((self.repo / "release/ledger.json").read_text()), self.ledger)
        self.assertFalse(json.loads((self.repo / "release/dev-automation.json").read_text())["publishing_enabled"])
        shutil.rmtree(output)
        # Competing implementations could build new tooling source, accept a
        # dirty selected tree, use a stale CI attempt, or reuse published bytes.
        self.cli(*[self.repo if arg == source_tree else arg for arg in args], success=False)
        changed = source_tree / "go.sum"
        original = changed.read_bytes()
        changed.write_bytes(original + b"\nchanged source")
        self.cli(*args, success=False)
        changed.write_bytes(original)
        runs[1].write_text(json.dumps({"workflow_runs": [good, {**good, "run_attempt": 2, "conclusion": "failure"}]}))
        self.cli(*args, success=False)
        runs[1].write_text(json.dumps({"workflow_runs": [good]}))
        releases.write_text(json.dumps([{"tag_name": "android-generic-r5", "draft": True}]))
        self.cli(*args, success=False)
        releases.write_text("[]")
        descriptor["reservations"]["biscuit"] = "first-incubator-dev"
        descriptor_path.write_text(json.dumps(descriptor))
        self.commit("invalid initial reservation fixture")
        self.cli(*args, success=False)
        descriptor["reservations"]["biscuit"] = "first-incubator-dev-biscuit-18"
        descriptor_path.write_text(json.dumps(descriptor))
        self.commit("restore initial reservation fixture")
        self.change("go.sum", "tooling-only dependency change must not relabel pinned sources")
        self.cli(*args)
        self.assertEqual(json.loads((output / "plan.json").read_text())["source"], self.base)


if __name__ == "__main__":
    unittest.main()
