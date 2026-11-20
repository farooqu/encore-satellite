"""Derive Biscuit ARM64 artifact identity from main history, without allocations."""

from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
# The private dev.5 reservation is the last identity carried over. Never move
# this anchor: already built commits must keep their identity on a rerun.
ANCHOR = "e47198791e746f71c233f0663e2fd5413093827c"


def identity(source):
    if not re.fullmatch(r"[0-9a-f]{40}", source):
        raise ValueError("Biscuit dev source must be a full commit SHA")
    history = subprocess.check_output(["git", "rev-list", "--first-parent", source], cwd=ROOT).decode().splitlines()
    if ANCHOR not in history or history[0] == ANCHOR:
        raise ValueError("Biscuit dev source must descend from its first-parent version anchor")
    distance = history.index(ANCHOR)
    # Keep automatic ARM64 builds well above retained legacy/private counters.
    # Dev is a publication channel, not part of the promotable artifact version.
    return f"0.1.{1 + distance}", 1_000_000_000 + distance


if __name__ == "__main__":
    print(*identity(sys.argv[1]))
