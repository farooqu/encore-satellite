"""Synthetic release identities, independent of main's advancing reservations."""
import json

import check


def inventory(core_version="0.1.1-dev.1"):
    registry = json.loads((check.ROOT / "release/targets.json").read_text())
    registry["core_version"] = core_version
    for target in registry["targets"]:
        if target["id"] in ("biscuit", "android-generic"):
            target["package_revision"] = {"biscuit": 18, "android-generic": 5}[target["id"]]
    ledger = {
        "schema": 1,
        "retired_revisions": {"biscuit": 15, "android-generic": 4},
        "reservations": [
            {"id": "first-incubator-dev", "core_version": core_version,
             "backend": "incubator", "channel": "dev",
             "revisions": {"biscuit": 16, "android-generic": 5}},
            {"id": "first-incubator-dev-biscuit-18", "core_version": core_version,
             "backend": "incubator", "channel": "dev", "revisions": {"biscuit": 18}},
        ],
    }
    return registry, ledger
