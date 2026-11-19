"""Validate release reservations and select conservative CI impact (stdlib only).

No build commands, updates, signatures or publication are executed here.
"""

import argparse
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
SEMVER = re.compile(
    r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-((?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
    r"(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?"
)
ID = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*")
# These inputs affect every target, independently of target-local dependencies.
SHARED_PREFIXES = (
    "core/", "backends/", ".github/", ".agents/", "tools/", "release/",
    "updates/", "build/", "scripts/", "licenses/",
)
SHARED_FILES = {"go.mod", "go.sum", "go.work", "go.work.sum", "Makefile",
                ".amp/services.yaml", "ACKNOWLEDGEMENTS.md", "LICENSE", "NOTICE"}


def path_is_safe(value):
    return (isinstance(value, str) and bool(value) and "\\" not in value
            and not PurePosixPath(value).is_absolute()
            and all(part not in {"", ".", ".."} for part in value.rstrip("/").split("/")))


def validate(registry):
    """Experimental reservations do not enable automatic release builders."""
    if type(registry.get("schema")) is not int or registry["schema"] != 1:
        raise ValueError("expected inventory schema 1")
    if not isinstance(registry.get("core_version"), str) or not SEMVER.fullmatch(registry["core_version"]):
        raise ValueError("core_version must be SemVer without a v prefix")
    if registry.get("channels") != ["dev", "stable"]:
        raise ValueError("channels must explicitly be dev and stable")
    targets = registry.get("targets")
    if not isinstance(targets, list) or not targets:
        raise ValueError("targets must be nonempty")
    seen = set()
    for target in targets:
        name = target.get("id")
        if not isinstance(name, str) or not ID.fullmatch(name) or name in seen:
            raise ValueError("target IDs must be unique lowercase slugs")
        seen.add(name)
        platform = target.get("platform")
        if not isinstance(platform, str) or not ID.fullmatch(platform):
            raise ValueError(f"{name}: invalid platform")
        if target.get("status") not in {"planned", "test-fixture", "experimental"}:
            raise ValueError(f"{name}: invalid target status")
        if target.get("release_supported") is not False:
            raise ValueError(f"{name}: automatic release builds remain disabled")
        revision = target.get("package_revision")
        if (type(revision) is not int or revision < 0
                or (target["status"] == "experimental") != (revision > 0)):
            raise ValueError(f"{name}: only experimental targets reserve positive revisions")
        dependencies = target.get("depends_on")
        if (not isinstance(dependencies, list) or not dependencies
                or any(not path_is_safe(p) or not p.endswith("/") for p in dependencies)
                or len(set(dependencies)) != len(dependencies)
                or f"platforms/{platform}/" not in dependencies):
            raise ValueError(f"{name}: explicit safe directory dependencies including platform required")
        if target["status"] != "test-fixture":
            if f"devices/{name}/" not in dependencies or "build" in target:
                raise ValueError(f"{name}: device needs its own path and no invented build")
        elif (name != "lab-linux-amd64" or target.get("build") != "go build ./cmd/encore-lab"
              or target.get("goos") != "linux" or target.get("goarch") != "amd64"):
            raise ValueError("only lab-linux-amd64 has a runnable fixture build")
    if not any(t["id"] == "lab-linux-amd64" and t["status"] == "test-fixture" for t in targets):
        raise ValueError("the non-release lab fixture is required")
    # Overlap is valid: multiple targets may depend on the same platform.
    return registry


def validate_ledger(ledger, registry, previous=None):
    """Reservations are append-only and consume revisions, not publish artifacts."""
    if type(ledger.get("schema")) is not int or ledger["schema"] != 1:
        raise ValueError("expected ledger schema 1")
    targets = {t["id"]: t for t in registry["targets"]}
    floors = ledger.get("retired_revisions")
    if (not isinstance(floors, dict)
            or any(name not in targets or targets[name]["status"] == "test-fixture"
                   or type(revision) is not int or revision < 0
                   for name, revision in floors.items())):
        raise ValueError("invalid retired revision floors")
    records = ledger.get("reservations")
    if not isinstance(records, list):
        raise ValueError("reservations must be a list")
    if previous is not None:
        old = previous["reservations"]
        if (ledger["schema"] != previous["schema"]
                or floors != previous["retired_revisions"] or records[:len(old)] != old):
            raise ValueError("existing reservations and retired floors are immutable")
    highest = dict(floors)
    ids = set()
    latest_core = {}
    for record in records:
        name = record.get("id")
        core = record.get("core_version")
        if not isinstance(name, str) or not ID.fullmatch(name) or name in ids:
            raise ValueError("reservation IDs must be unique slugs")
        ids.add(name)
        if not isinstance(core, str) or not SEMVER.fullmatch(core):
            raise ValueError(f"{name}: invalid core SemVer")
        if (record.get("backend") != "incubator" or record.get("channel") not in {"dev", "stable"}
                or (record["channel"] == "stable" and "-" in core.split("+", 1)[0])):
            raise ValueError(f"{name}: invalid backend/channel or stable prerelease")
        revisions = record.get("revisions")
        if not isinstance(revisions, dict) or not revisions:
            raise ValueError(f"{name}: explicit target revisions required")
        private = "private_arm64_zip_sha256" in record
        if private:
            pin = record["private_arm64_zip_sha256"]
            if (set(revisions) != {"biscuit"} or record["channel"] != "dev"
                    or not isinstance(pin, str) or not re.fullmatch(r"[0-9a-f]{64}", pin)):
                raise ValueError(f"{name}: private ARM64 requires a Biscuit-only dev reservation and exact ZIP SHA-256")
        for target, revision in revisions.items():
            if (target not in targets or targets[target]["status"] != "experimental"
                    or type(revision) is not int or revision <= highest.get(target, 0)
                    or (targets[target]["platform"] == "android" and revision > 2100000000)):
                raise ValueError(f"{name}: revision must increase for each experimental target")
            highest[target] = revision
            # Private installers consume the same revision space without
            # advancing the shared build cohort or any other target.
            if not private:
                latest_core[target] = core
    for target in registry["targets"]:
        if target["status"] == "experimental" and (
                target["id"] not in latest_core
                or highest[target["id"]] != target["package_revision"]
                or latest_core[target["id"]] != registry["core_version"]):
            raise ValueError(f"{target['id']}: inventory must match its latest reservation")
    return ledger


def select(paths, registry):
    """Unknown/unsafe inputs broaden impact, never silently disappear."""
    all_targets = {t["id"] for t in registry["targets"]}
    affected = set()
    unknown = []
    for path in paths:
        if not path_is_safe(path):
            unknown.append(path)
            affected.update(all_targets)
        elif path in SHARED_FILES:
            affected.update(all_targets)
        # Only Markdown is automatically documentation, not code hidden in docs/.
        elif path.endswith(".md"):
            continue
        elif path.startswith(SHARED_PREFIXES):
            affected.update(all_targets)
        else:
            dependents = {t["id"] for t in registry["targets"]
                          if any(path.startswith(p) for p in t["depends_on"])}
            if dependents:
                affected.update(dependents)
            else:
                unknown.append(path)
                affected.update(all_targets)
    return {
        "affected_targets": sorted(affected),
        "package_targets": sorted(t["id"] for t in registry["targets"]
                                  if t["id"] in affected and t["release_supported"]),
        "unknown_paths": sorted(set(unknown)),
        # Always keep actual Go boundary/vet/race/build checks, even with no devices.
        "lab_check": True,
    }


def changed_paths(base, head):
    if not base or set(base) == {"0"}:
        return None
    try:
        merge_base = subprocess.check_output(
            ["git", "merge-base", base, head], cwd=ROOT, text=True, stderr=subprocess.PIPE
        ).strip()
        raw = subprocess.check_output(
            ["git", "diff", "--name-only", "--no-renames", "-z", merge_base, head, "--"],
            cwd=ROOT, stderr=subprocess.PIPE,
        )
    except subprocess.CalledProcessError:
        return None
    return [p.decode("utf-8", errors="surrogateescape") for p in raw.split(b"\0") if p]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", help="base Git revision; missing/unavailable means all targets")
    parser.add_argument("--head", default="HEAD")
    parser.add_argument("--github-output", type=Path)
    args = parser.parse_args()
    registry = validate(json.loads((ROOT / "release/targets.json").read_text()))
    paths = changed_paths(args.base, args.head)
    previous = None
    if paths is not None:
        base = subprocess.check_output(["git", "merge-base", args.base, args.head],
                                       cwd=ROOT, text=True).strip()
        old = subprocess.run(["git", "show", f"{base}:release/ledger.json"], cwd=ROOT,
                             capture_output=True, text=True)
        if old.returncode == 0:
            previous = json.loads(old.stdout)
    validate_ledger(json.loads((ROOT / "release/ledger.json").read_text()), registry, previous)
    result = select(paths if paths is not None else ["<unavailable diff>"], registry)
    print(json.dumps(result, indent=2))
    if args.github_output:
        with args.github_output.open("a") as output:
            for key, value in result.items():
                output.write(f"{key}={json.dumps(value, separators=(',', ':'))}\n")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, AttributeError) as error:
        sys.exit(f"Invalid release inventory: {error}")
