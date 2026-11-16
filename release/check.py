"""Validate the foundation inventory and select conservative CI impact (stdlib only).

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
    """This intentionally validates only the unreleased foundation contract."""
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
        if target.get("status") not in {"planned", "test-fixture"}:
            raise ValueError(f"{name}: only planned targets and test fixtures exist")
        if target.get("release_supported") is not False:
            raise ValueError(f"{name}: no release builders, signing trust or updater exist")
        if type(target.get("package_revision")) is not int or target["package_revision"] != 0:
            raise ValueError(f"{name}: unreleased foundation requires revision zero")
        dependencies = target.get("depends_on")
        if (not isinstance(dependencies, list) or not dependencies
                or any(not path_is_safe(p) or not p.endswith("/") for p in dependencies)
                or len(set(dependencies)) != len(dependencies)
                or f"platforms/{platform}/" not in dependencies):
            raise ValueError(f"{name}: explicit safe directory dependencies including platform required")
        if target["status"] == "planned":
            if f"devices/{name}/" not in dependencies or "build" in target:
                raise ValueError(f"{name}: planned device needs its own path and no invented build")
        elif (name != "lab-linux-amd64" or target.get("build") != "go build ./cmd/encore-lab"
              or target.get("goos") != "linux" or target.get("goarch") != "amd64"):
            raise ValueError("only lab-linux-amd64 has a runnable fixture build")
    if not any(t["id"] == "lab-linux-amd64" and t["status"] == "test-fixture" for t in targets):
        raise ValueError("the non-release lab fixture is required")
    # Overlap is valid: multiple targets may depend on the same platform.
    return registry


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
