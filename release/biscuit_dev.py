"""Derive the shared Encore release identity from first-parent main history."""

from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
# The private dev.5 reservation is the last identity carried over. Never move
# this anchor: already built commits must keep their identity on a rerun.
ANCHOR = "e47198791e746f71c233f0663e2fd5413093827c"
# Last commit in the original height-versioned series (0.1.35). Keep that
# series verifiable forever; only later commits use message-driven SemVer.
SEMVER_BASE = "f6b6fd02bb42913608ff6957c0bc3bdc88bc50d0"
CONVENTIONAL = re.compile(r"([a-z]+)(?:\([^()\n]+\))?(!)?: .+")


def identity(source):
    if not re.fullmatch(r"[0-9a-f]{40}", source):
        raise ValueError("release source must be a full commit SHA")
    history = subprocess.check_output(["git", "rev-list", "--first-parent", source], cwd=ROOT).decode().splitlines()
    if ANCHOR not in history or history[0] == ANCHOR:
        raise ValueError("release source must descend from its first-parent version anchor")
    distance = history.index(ANCHOR)
    revision = 1_000_000_000 + distance
    if SEMVER_BASE not in history or source == SEMVER_BASE:
        return f"0.1.{1 + distance}", revision

    major, minor, patch = 0, 1, 1 + distance - history.index(SEMVER_BASE)
    messages = subprocess.check_output(
        ["git", "log", "-z", "--first-parent", "--reverse", "--format=%B", f"{SEMVER_BASE}..{source}"],
        cwd=ROOT, text=True).split("\0")
    for message in messages[:-1]:
        subject, _, body = message.partition("\n")
        match = CONVENTIONAL.fullmatch(subject)
        if match and (match[2] or re.search(r"^BREAKING[ -]CHANGE: \S", body, re.M)):
            major, minor, patch = major + 1, 0, 0
        elif match and match[1] == "feat":
            minor, patch = minor + 1, 0
        else:
            patch += 1
    return f"{major}.{minor}.{patch}", revision


if __name__ == "__main__":
    release = identity(sys.argv[1])
    # Owner is holding 0.2.0 and beyond. Keep identity calculation independent
    # from this publication hold so a future unlock cannot change old identities.
    if release[0].startswith("0.1."):
        print(*release)
