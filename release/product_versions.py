"""Resolve product release identities from the exact source, not moving tags."""

import argparse
import json
import re
import subprocess

import biscuit_dev

PREFIXES = {"android-generic": "android-tablet", "biscuit": "biscuit", "android-assistant": "android-assistant"}
PLAIN = r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
DEV = PLAIN + r"-dev\.[1-9][0-9]*"
LEGACY_TAGS = {"android-generic": "android-" + PLAIN, "biscuit": "biscuit-" + PLAIN,
               "android-assistant": r"android-assistant-r[1-9][0-9]*"}


def resolve(legacy, train):
    core, revision = legacy
    if train is None:
        return {"core_version": core, "version": core, "revision": revision}
    base, first, major = (train[key] for key in ("base", "first_revision", "ordering_major"))
    if (set(train) != {"base", "first_revision", "ordering_major"} or not re.fullmatch(PLAIN, base)
            or type(first) is not int or not 1_000_000_000 < first <= revision <= 2_100_000_000
            or type(major) is not int or major < 1):
        raise ValueError("invalid source-pinned Dev train")
    return {"core_version": f"{major}.0.{revision}", "version": f"{base}-dev.{revision - first + 1}", "revision": revision}


def trains(source):
    # Older source trees lack this file; pending products are absent from it.
    entries = subprocess.check_output(["git", "ls-tree", source, "--", "release/dev-trains.json"], cwd=biscuit_dev.ROOT)
    if not entries:
        return {}
    policy = json.loads(subprocess.check_output(["git", "show", f"{source}:release/dev-trains.json"], cwd=biscuit_dev.ROOT))
    if not isinstance(policy, dict) or set(policy) - PREFIXES.keys() or not all(isinstance(v, dict) for v in policy.values()):
        raise ValueError("invalid product Dev train policy")
    return policy


def product_identity(source, target):
    if target not in PREFIXES:
        raise ValueError(f"unknown product {target}")
    return resolve(biscuit_dev.identity(source), trains(source).get(target))


def check_train_change(source, parent):
    previous, current = trains(parent), trains(source)
    if previous.keys() - current.keys():
        raise ValueError("cannot return a cutover product to legacy ordering")
    if previous == current:
        return
    legacy, revision = biscuit_dev.identity(source)
    for target, train in current.items():
        if previous.get(target) == train:
            continue
        resolve((legacy, revision), train)
        if train["first_revision"] != revision:
            raise ValueError(f"{target}: the train must start at this source's revision")
        old = previous.get(target)
        if old:
            if train["ordering_major"] != old["ordering_major"] or tuple(map(int, train["base"].split("."))) <= tuple(map(int, old["base"].split("."))):
                raise ValueError(f"{target}: advance the train base without changing its ordering major")
        elif train["base"] != "0.10.0" or train["ordering_major"] <= int(legacy.split(".")[0]):
            raise ValueError(f"{target}: initial train must start at 0.10.0 above legacy signed ordering")


def dev_tag_target(tag):
    return next((t for t, p in PREFIXES.items() if re.fullmatch(f"{p}-v{DEV}|{LEGACY_TAGS[t]}", tag)), None)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source")
    parser.add_argument("target", choices=PREFIXES)
    args = parser.parse_args()
    value = product_identity(args.source, args.target)
    print(*[value[k] for k in ("core_version", "revision", "version")])
