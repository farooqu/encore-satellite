#!/usr/bin/env python3
"""Control only the isolated loopback MA lab; never print authentication secrets."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import sys
from urllib.error import HTTPError
from urllib.request import Request, urlopen

BASE = "http://127.0.0.1:8097"
STATE = Path.home() / ".local/state/encore-sendspin-lab"
AUTH = STATE / "auth.json"


def request(path, body=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = Request(BASE + path, data=json.dumps(body).encode() if body is not None else None,
                  headers=headers)
    try:
        with urlopen(req, timeout=90) as response:
            return json.load(response)
    except HTTPError as error:
        raise RuntimeError(f"HTTP {error.code}: {error.read().decode()}") from None


def api(command, args=None, token=None):
    if token is None:
        token = json.loads(AUTH.read_text())["token"]
    return request("/api", {"command": command, "message_id": "encore-lab", "args": args or {}}, token)


def enforce_encryption():
    config = api("config/providers/get", {"instance_id": "sendspin"})
    if config["values"]["allow_legacy_clients"]["value"]:
        api("config/providers/save", {"provider_domain": "sendspin", "instance_id": "sendspin",
                                      "values": {"allow_legacy_clients": False}})
        api("config/providers/reload", {"instance_id": "sendspin"})


def bootstrap():
    os.umask(0o077)
    STATE.mkdir(parents=True, exist_ok=True)
    if AUTH.exists():
        api("auth/me")
        enforce_encryption()
        print(f"Existing private lab authentication is valid: {AUTH}")
        return
    credentials = STATE / "credentials.json"
    if not credentials.exists():
        credentials.write_text(json.dumps({"username": "encore-lab", "password": secrets.token_urlsafe(32)}))
    creds = json.loads(credentials.read_text())
    try:
        result = request("/setup", {**creds, "display_name": "Encore lab", "device_name": "encore-lab-helper"})
    except RuntimeError as error:
        if "HTTP 409" not in str(error):
            raise
        result = request("/api", {"command": "auth/login", "args": creds})
    token = result["token"]
    long_lived = api("auth/token/create", {"name": "encore-sendspin-isolated-lab"}, token)
    AUTH.write_text(json.dumps({"token": long_lived}))
    api("auth/me")
    try:
        api("config/onboard_complete")
    except RuntimeError as error:
        if "Unknown command" not in str(error) and "already" not in str(error):
            raise
    enforce_encryption()
    print(f"Lab admin and long-lived API token created privately in {STATE}")


def approve(player_id):
    step = api("config/players/setup", {"player_id": player_id})
    if step.get("step_id") != "approve_device":
        raise RuntimeError(
            "No unpaired-consent form: refusing automatic pairing. "
            "An already usable guest needs no operator action; enter_token requests "
            "optional pairing, not guest approval. Check players first. "
            f"Setup step: {step}"
        )
    result = api("config/flows/submit", {
        "flow_id": step["flow_id"], "values": {"connect_method": "unpaired"},
    })
    return result


def redacted(value):
    if isinstance(value, dict):
        return {key: "<redacted>" if any(word in key.lower() for word in ("token", "password", "secret"))
                else redacted(item) for key, item in value.items()}
    if isinstance(value, list):
        return [redacted(item) for item in value]
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("bootstrap")
    sub.add_parser("status")
    sub.add_parser("players")
    approve_parser = sub.add_parser("approve-unpaired", help="Explicitly approve this receiver only")
    approve_parser.add_argument("player_id")
    play_parser = sub.add_parser("play")
    play_parser.add_argument("queue_id")
    generic = sub.add_parser("api")
    generic.add_argument("api_command")
    generic.add_argument("args", nargs="?", default="{}", help="JSON arguments, never paste secrets")
    args = parser.parse_args()
    if args.command == "bootstrap":
        bootstrap()
        return
    if args.command == "status":
        result = {"me": api("auth/me"), "providers": api("providers"),
                  "players": api("players/all"), "queues": api("player_queues/all"),
                  "fixture_sha256": hashlib.sha256((STATE / "fixture.wav").read_bytes()).hexdigest()}
    elif args.command == "players":
        result = api("players/all")
    elif args.command == "approve-unpaired":
        result = approve(args.player_id)
    elif args.command == "play":
        result = api("player_queues/play_media", {"queue_id": args.queue_id,
                     "media": BASE + "/lab/fixture.wav", "option": "replace"})
    else:
        result = api(args.api_command, json.loads(args.args))
    print(json.dumps(redacted(result), indent=2))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, KeyError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
