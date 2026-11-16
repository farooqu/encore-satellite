"""Validate closed, planned-only device metadata; never build or install a target."""

import json
from pathlib import Path
import sys


TARGETS = ("echo-dot-2", "android-generic")


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def validate(root):
    registry_path = root / "release/targets.json"
    registry = json.loads(registry_path.read_text(), object_pairs_hook=unique_object)
    if not isinstance(registry, dict) or not isinstance(registry.get("targets"), list):
        raise ValueError("release/targets.json: expected a targets array")
    if any(not isinstance(entry, dict) for entry in registry["targets"]):
        raise ValueError("release/targets.json: target entries must be objects")

    for target in TARGETS:
        path = root / "devices" / target / "profile.json"
        profile = json.loads(path.read_text(), object_pairs_hook=unique_object)
        expected = {
            "schema": 1,
            "target": target,
            "status": "planned",
        }
        # This contract intentionally admits no runtime settings/capability claims.
        # JSON booleans/floats must not compare equal to the integer schema version.
        if (profile != expected or not isinstance(profile, dict)
                or type(profile.get("schema")) is not int):
            raise ValueError(f"devices/{target}/profile.json: expected the closed schema-1 "
                             "planned profile containing only schema, target and status")

        entries = [entry for entry in registry["targets"] if entry.get("id") == target]
        if len(entries) != 1:
            raise ValueError(f"release/targets.json: expected exactly one {target} entry")
        entry = entries[0]
        if (entry.get("status") != "planned"
                or entry.get("release_supported") is not False
                or type(entry.get("package_revision")) is not int
                or entry["package_revision"] != 0):
            raise ValueError(f"release/targets.json: {target} must remain planned, "
                             "release_supported=false, package_revision=0")


if __name__ == "__main__":
    try:
        validate(Path(__file__).resolve().parents[2])
    except (OSError, UnicodeError, ValueError) as error:
        print(f"Device package validation failed: {error}", file=sys.stderr)
        sys.exit(1)
    print("Validated 2 planned device profiles; no installable packages")
