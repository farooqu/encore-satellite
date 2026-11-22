"""Plan both Dev packages from one snapshot of published releases."""

import argparse
import json
from pathlib import Path
import re
import subprocess

from biscuit_dev import ANCHOR, identity
from check import ROOT, changed_paths, select

TARGETS = {"android-generic": "Android", "biscuit": "Biscuit"}
REPOSITORY = "farooqu/encore-satellite"


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def affected(paths, registry):
    if paths is None:
        raise ValueError("cannot resolve release diff")
    result = set()
    common = []
    for path in paths:
        if path in {".github/workflows/android.yml", "release/android_dev.py", "release/android-feed.py"}:
            result.add("android-generic")
        elif (path.startswith(("platforms/linuxble/", "platforms/linuxsendspin/", "platforms/linuxupdate/"))
              and not path.endswith(".md")) or path in {
                  ".github/workflows/biscuit-arm64-dev.yml", ".github/workflows/biscuit-kernel.yml",
                  ".github/workflows/biscuit-sign.yml", "devices/biscuit/packaging/README.md",
                  "devices/biscuit/packaging/kernel61/README.md",  # Pre-move release history.
              }:
            # The kernel recipe embeds this README in the runtime's license directory.
            result.add("biscuit")
        else:
            common.append(path)
    return result.union(select(common, registry)["affected_targets"])


def plan(source, releases, registry, armed):
    version, revision = identity(source)
    history = git("rev-list", "--first-parent", source).splitlines()
    earlier = {sha: index for index, sha in enumerate(history[1:])}
    baselines = {}
    for release in releases:
        if not release["immutable"] or release["draft"]:
            continue
        tag = release["tag_name"]
        if re.fullmatch(r"android-generic-r[1-9][0-9]*", tag):
            target = "android-generic"
        elif re.fullmatch(r"biscuit-[0-9]+\.[0-9]+\.[0-9]+", tag):
            target = "biscuit"
        else:
            continue
        names = {asset["name"] for asset in release["assets"]}
        if not {"receipt.json", "receipt.jws"} <= names:
            continue
        commit = git("rev-parse", "--verify", f"refs/tags/{tag}^{{commit}}")
        # Tags alone (including recovery tags), the current build, and later
        # runs which finished out of order cannot establish a baseline here.
        if commit in earlier and (target not in baselines or earlier[commit] < earlier[baselines[target]]):
            baselines[target] = commit

    selected = []
    notes = [f"# Encore {version}", "", f"[Source commit](https://github.com/{REPOSITORY}/commit/{source})", ""]
    for target, name in TARGETS.items():
        notes += [f"## {name}", ""]
        baseline = baselines.get(target)
        if not version.startswith("0.1."):
            reason = "this version is on hold"
        elif target not in armed:
            reason = "automatic Dev is disabled"
        elif baseline and target not in affected(changed_paths(baseline, source), registry):
            reason = "no package changes since the last published release"
        else:
            selected.append(target)
            base = baseline or ANCHOR
            commits = history[:history.index(base)]
            changes = []
            for commit, parent in reversed(list(zip(commits, history[1:]))):
                if target in affected(changed_paths(parent, commit), registry):
                    subject = git("show", "-s", "--format=%s", commit)
                    changes.append(f"- {subject} ([{commit[:10]}](https://github.com/{REPOSITORY}/commit/{commit}))")
            notes += changes or ["- Initial automatic Dev build; no prior published baseline."]
            notes.append("")
            continue
        notes += [f"- No build — {reason}.", ""]
    return {"source": source, "version": version, "revision": revision,
            "targets": selected, "changelog": "\n".join(notes)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source")
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    pages = json.loads(subprocess.check_output([
        "gh", "api", "--paginate", "--slurp", f"repos/{REPOSITORY}/releases?per_page=100",
    ], cwd=ROOT))
    # A producer may have published after checkout, but before the API listing.
    git("fetch", "--quiet", "--tags", "origin")
    registry = json.loads((ROOT / "release/targets.json").read_text())
    armed = json.loads((ROOT / "release/dev-automation.json").read_text())["automatic_dev_targets"]
    result = plan(args.source, (release for page in pages for release in page), registry, armed)
    args.output.mkdir()
    (args.output / "plan.json").write_text(json.dumps(result) + "\n")
    (args.output / f"{result['version']}.md").write_text(result["changelog"])


if __name__ == "__main__":
    main()
