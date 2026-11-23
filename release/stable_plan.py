"""Preview a Stable release from explicit first-parent baselines; never publish."""

import argparse
import json
from pathlib import Path
import re

from biscuit_dev import CONVENTIONAL
from check import ROOT, changed_paths
from dev_plan import affected, git, target_section
from product_versions import PLAIN, PREFIXES, product_identity

LEVELS = ("none", "patch", "minor", "major")


def version_parts(value):
    if not re.fullmatch(PLAIN, value):
        raise ValueError("Stable requires plain SemVer")
    return tuple(map(int, value.split(".")))


def preview(target, source, baseline, registry, *, previous=None, initial=None, decisions=None):
    if target not in PREFIXES or not all(re.fullmatch(r"[0-9a-f]{40}", s) for s in (source, baseline)):
        raise ValueError("expected a product and exact source/baseline SHAs")
    if (previous is None) == (initial is None):
        raise ValueError("choose the previous Stable version or an approved initial version")
    history = git("rev-list", "--first-parent", source).splitlines()
    if baseline not in history[1:]:
        raise ValueError("Stable baseline must be an earlier first-parent commit")
    rank = 0
    review = []
    decisions = {} if decisions is None else decisions
    used = set()
    for commit, parent in zip(history[:history.index(baseline)], history[1:]):
        subject, body, overrides = git("show", "-s", "--format=%s%x00%b%x00%(trailers:key=Version-Bumps,valueonly,unfold)", commit).split("\0", 2)
        paths = changed_paths(parent, commit)
        if paths is None:
            raise ValueError(f"cannot resolve release diff at {commit}")
        shipped = [p for p in paths if not p.startswith((".github/", "release/", "tools/", "lab/", "docs/"))
                   and not p.endswith(("_test.go", "Test.kt")) and not re.search(r"(?:^|/)(?:tests?|testdata|androidTest)/|(?:^|/)test_[^/]+\.py$", p)]
        impacts = [affected([path], registry) & PREFIXES.keys() for path in shipped]
        products = set().union(*impacts)
        if overrides:
            explicit = {}
            for item in re.split(r"[,\n]", overrides):
                name, sep, level = item.strip().partition("=")
                if not sep or name not in products or level not in LEVELS[1:] or name in explicit:
                    raise ValueError(f"invalid Version-Bumps entry in {commit}: {item}")
                explicit[name] = level
            rank = max(rank, LEVELS.index(explicit.get(target, "none")))
            continue
        if target not in products:
            continue
        match = CONVENTIONAL.fullmatch(subject)
        ambiguous = (not match or any(p and p != products for p in impacts) or
                     (match[1] in {"docs", "test", "ci"} and not any(p.endswith(".md") for p in shipped)))
        if ambiguous:
            if commit not in decisions:
                review.append(commit)
                continue
            level = decisions[commit]
            if level not in LEVELS:
                raise ValueError(f"invalid reviewed bump at {commit}")
            used.add(commit)
            rank = max(rank, LEVELS.index(level))
        elif match[2] or re.search(r"^BREAKING[ -]CHANGE: \S", body, re.M):
            rank = 3
        else:
            rank = max(rank, 2 if match[1] == "feat" else 1)
    if set(decisions) != used:
        raise ValueError("review decisions must refer to ambiguous commits in this product's candidate")
    if initial is not None:
        if version_parts(initial) < (0, 10, 0):
            raise ValueError("initial Stable version must be at least 0.10.0")
        version = initial
    else:
        parts = version_parts(previous)
        index = 3 - rank
        version = ".".join(str(n if i < index else n + 1 if i == index else 0) for i, n in enumerate(parts)) if rank else None
    next_dev = None
    if version:
        major, minor, patch = version_parts(version)
        next_dev = f"{major}.{minor}.{patch + 1}"
    return {"target": target, "source": source, "baseline": baseline, "previous": previous, "decisions": decisions,
            "bump": LEVELS[rank], "version": version, "review_commits": review, "next_dev_base": next_dev,
            "candidate": product_identity(source, target),
            "changelog": "\n".join(["## What's changed", "", *target_section(target, history, baseline, registry), ""])}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("target", choices=PREFIXES)
    parser.add_argument("source")
    parser.add_argument("baseline")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--previous")
    mode.add_argument("--initial")
    parser.add_argument("--decisions", type=Path, help="reviewed JSON map of ambiguous commit SHAs to bump levels for this product")
    args = parser.parse_args()
    print(json.dumps(preview(args.target, args.source, args.baseline, json.loads((ROOT / "release/targets.json").read_text()),
                             previous=args.previous, initial=args.initial,
                             decisions=json.loads(args.decisions.read_bytes()) if args.decisions else None), indent=2))
