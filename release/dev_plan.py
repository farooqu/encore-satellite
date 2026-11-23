"""Plan Dev packages from one snapshot of published releases."""

import argparse
import json
from pathlib import Path
import re
import subprocess

from biscuit_dev import ANCHOR, CONVENTIONAL, identity
from check import ROOT, changed_paths, select
from product_versions import check_train_change, dev_tag_target, product_identity

ASSISTANT = "android-assistant"
TARGETS = {"android-generic": "Android", "biscuit": "Biscuit", ASSISTANT: "Android Assistant"}
REPOSITORY = "farooqu/encore-satellite"
CHANGELOG_GROUPS = ("Breaking changes", "Features", "Fixes", "Performance", "Other changes")


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def affected(paths, registry):
    if paths is None:
        raise ValueError("cannot resolve release diff")
    result = set()
    common = []
    for path in paths:
        if path in {"release/dev-automation.json", "release/dev-trains.json"}:
            continue  # Policy can arm a target; it does not change its package.
        if path == ".github/workflows/android-assistant.yml" or path.startswith("devices/android-assistant/"):
            if not path.endswith(".md"):
                result.add(ASSISTANT)
            continue
        # Assistant uses the Android Go host, AEC build, two shared Kotlin
        # files, and voice resources, not the tablet UI or Android OTA client.
        if path in {"release/android_dev.py", "LICENSE", "go.mod", "go.sum"} or (
            not path.endswith(".md") and (path.startswith((
                "core/", "backends/incubator/", "backends/internal/outbox/", "licenses/",
                "platforms/android/voice-ui/", "platforms/android/src/main/cpp/",
            )) or re.fullmatch(r"platforms/android/(?:[^/]+\.go|build-aec|src/.*/(?:AudioIo|VoiceReference)\.kt)", path))
        ):
            result.add(ASSISTANT)
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


def published_baselines(history, releases):
    earlier = {sha: index for index, sha in enumerate(history[1:])}
    baselines = {}
    for release in releases:
        if not release["immutable"] or release["draft"]:
            continue
        tag = release["tag_name"]
        target = dev_tag_target(tag)
        if not target or not release.get("prerelease", True):
            continue
        names = {asset["name"] for asset in release["assets"]}
        expected = {"encore-satellite-assistant.apk", "SHA256SUMS"} if target == ASSISTANT else {"receipt.json", "receipt.jws"}
        if not expected <= names:
            continue
        commit = git("rev-parse", "--verify", f"refs/tags/{tag}^{{commit}}")
        # Tags alone (including recovery tags), the current build, and later
        # runs which finished out of order cannot establish a baseline here.
        if commit in earlier and (target not in baselines or earlier[commit] < earlier[baselines[target]]):
            baselines[target] = commit
    return baselines


def target_changes(target, history, base, registry):
    changes = {group: [] for group in CHANGELOG_GROUPS}
    commits = history[:history.index(base)]
    for commit, parent in reversed(list(zip(commits, history[1:]))):
        paths = changed_paths(parent, commit)
        if target not in affected(paths, registry):
            continue
        subject, body, audience, override = git(
            "show", "-s", "--format=%s%x00%b%x00%(trailers:key=Release-Targets,valueonly,unfold)"
            f"%x00%(trailers:key=Release-Note-{target},valueonly,unfold)", commit,
        ).split("\0", 3)
        if audience and target not in {name.strip() for name in re.split(r"[,\n]", audience)}:
            continue
        match = CONVENTIONAL.fullmatch(subject)
        group = None
        if match:
            kind, breaking, summary = match.groups()
            group = {"feat": "Features", "fix": "Fixes", "perf": "Performance", "revert": "Fixes"}.get(kind)
            if breaking or re.search(r"^BREAKING[ -]CHANGE: \S", body, re.M):
                group = "Breaking changes"
        summaries = [line.strip() for line in override.splitlines() if line.strip()]
        if not summaries:
            if not group:
                continue
            # A mixed commit's subject can describe a different product. Link
            # to the change without claiming that the whole subject applies.
            impacts = (affected([path], registry) & TARGETS.keys() for path in paths)
            if not audience and any(products and target not in products for products in impacts):
                group = "Other changes"
                summary = "Changes spanning multiple products"
            summaries = [summary]
        for summary in summaries:
            summary = summary[0].upper() + summary[1:]
            changes[group or "Other changes"].append(
                f"- {summary} ([{commit[:10]}](https://github.com/{REPOSITORY}/commit/{commit}))")
    return changes


def assistant_changelog(source, releases, registry):
    history = git("rev-list", "--first-parent", source).splitlines()
    baseline = published_baselines(history, releases).get(ASSISTANT)
    return "\n".join(["## What's changed", "", *target_section(ASSISTANT, history, baseline, registry), ""])


def target_section(target, history, baseline, registry):
    notes = []
    if target == ASSISTANT and baseline is None:
        # Do not include shared commits from before this package existed.
        added = git("log", "--first-parent", "--reverse", "--diff-filter=A", "--format=%H",
                    history[0], "--", "devices/android-assistant/build-android").splitlines()
        previous = history[history.index(added[0]) + 1:] if added else []
        baseline = previous[0] if previous else None
        notes += ["Initial Assistant release.", ""]
    elif baseline is None:
        baseline = ANCHOR
    if baseline:
        changes = target_changes(target, history, baseline, registry)
        for group, entries in changes.items():
            if entries:
                notes += [f"### {group}", "", *entries, ""]
        if not notes:
            notes += ["No release highlights listed.", ""]
        notes.append(f"[Full source comparison](https://github.com/{REPOSITORY}/compare/{baseline}...{history[0]})")
    return notes


def plan(source, releases, registry, armed):
    version, revision = identity(source)
    history = git("rev-list", "--first-parent", source).splitlines()
    check_train_change(source, history[1])
    baselines = published_baselines(history, releases)

    selected = []
    identities = {}
    target_changelogs = {}
    notes = ["# Encore Dev release plan", "", f"[Source commit](https://github.com/{REPOSITORY}/commit/{source})", ""]
    for target, name in TARGETS.items():
        section = [f"## {name}", ""]
        baseline = baselines.get(target)
        current = product_identity(source, target)
        new_train = (baseline and current["version"] != current["core_version"] and
                     current["version"].split("-dev.")[0] != product_identity(baseline, target)["version"].split("-dev.")[0])
        if target not in armed:
            reason = "automatic Dev is disabled"
        elif baseline and not new_train and target not in affected(changed_paths(baseline, source), registry):
            reason = "no package changes since the last published release"
        else:
            selected.append(target)
            identities[target] = current
            highlights = target_section(target, history, baseline, registry)
            target_changelogs[target] = "\n".join(["## What's changed", "", *highlights, ""])
            notes += [*section, f"**{current['version']}**", "", *highlights, ""]
            continue
        notes += [*section, f"- No build — {reason}.", ""]
    return {"source": source, "version": version, "revision": revision,
            "targets": selected, "identities": identities, "changelog": "\n".join(notes), "target_changelogs": target_changelogs}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source")
    parser.add_argument("output", type=Path)
    parser.add_argument("--assistant-notes", action="store_true", help="write notes for an Assistant sideload release")
    args = parser.parse_args()
    pages = json.loads(subprocess.check_output([
        "gh", "api", "--paginate", "--slurp", f"repos/{REPOSITORY}/releases?per_page=100",
    ], cwd=ROOT))
    # A producer may have published after checkout, but before the API listing.
    git("fetch", "--quiet", "--tags", "origin")
    registry = json.loads((ROOT / "release/targets.json").read_text())
    releases = (release for page in pages for release in page)
    if args.assistant_notes:
        args.output.write_text(assistant_changelog(args.source, releases, registry), encoding="utf-8")
        return
    armed = json.loads((ROOT / "release/dev-automation.json").read_text())["automatic_dev_targets"]
    result = plan(args.source, releases, registry, armed)
    args.output.mkdir()
    (args.output / "plan.json").write_text(json.dumps(result) + "\n")
    (args.output / f"{result['version']}.md").write_text(result["changelog"])


if __name__ == "__main__":
    main()
