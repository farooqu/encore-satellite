"""Onboard ONLY this isolated loopback HA; never reads production credentials."""

import asyncio
import json
import secrets
import base64

import aiohttp

from client import CLIENT, HA, STATE, login


async def pair_satellite(client):
    entries = await client.command("config_entries/get")
    existing = next((e for e in entries if e["domain"] == "esphome" and e["title"] == "Encore virtual satellite"), None)
    if existing:
        return
    # Wait for actual zeroconf discovery, not a fabricated discovery payload.
    for _ in range(150):
        flows = await client.command("config_entries/flow/progress")
        flow = next((f for f in flows if f["handler"] == "esphome" and f.get("context", {}).get("source") == "zeroconf" and f.get("context", {}).get("unique_id") == "02:00:00:ec:00:01"), None)
        if flow:
            break
        await asyncio.sleep(0.1)
    else:
        raise AssertionError("No real Encore mDNS discovery flow within 15s")
    path = "/api/config/config_entries/flow/" + flow["flow_id"]
    result = await client.api("POST", path, {})
    assert result.get("step_id") == "encryption_key", "Expected encrypted discovery pairing"
    wrong = base64.b64encode(secrets.token_bytes(32)).decode()
    result = await client.api("POST", path, {"noise_psk": wrong})
    assert result.get("errors", {}).get("base") == "invalid_psk", "Wrong key was not rejected"
    print("PASS real mDNS discovery/configuration: wrong Noise key rejected")
    result = await client.api("POST", path, {"noise_psk": (STATE / "noise.psk").read_text()})
    assert result["type"] == "create_entry", "Correct key did not complete pairing"
    print("PASS real HA config flow: correct Noise key accepted")


async def provision():
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    async with aiohttp.ClientSession() as session:
        credentials_path = STATE / "credentials.json"
        if not credentials_path.exists():
            password = secrets.token_urlsafe(24)
            async with session.post(HA + "/api/onboarding/users", json={"name": "Encore lab", "username": "encore", "password": password, "client_id": CLIENT, "language": "en"}) as response:
                assert response.status == 200, "Isolated lab already onboarded; use its existing credentials"
                code = (await response.json())["auth_code"]
            async with session.post(HA + "/auth/token", data={"grant_type": "authorization_code", "code": code, "client_id": CLIENT}) as response:
                tokens = await response.json()
                assert response.status == 200
            credentials_path.touch(mode=0o600)
            credentials_path.write_text(json.dumps({"username": "encore", "password": password, "refresh_token": tokens["refresh_token"]}))
        client = await login(session)
        # Pinned HA dev trials migrated HTTP settings for five minutes. Our
        # authenticated connection confirms this isolated instance is reachable;
        # confirm only a healthy config actually running in the pending slot.
        http_config = await client.command("http/config")
        if http_config["active_config_type"] == "pending":
            assert http_config["pending"]["error"] is None, "HTTP trial failed; do not promote it"
            await client.command("http/config/promote")
            confirmed = await client.command("http/config")
            assert confirmed["pending"] is None and confirmed["revert_at"] is None
            print("Confirmed active lab HTTP configuration; automatic rollback cancelled")
        async with session.get(HA + "/api/onboarding") as response:
            steps = await response.json() if response.status == 200 else []
        for step in steps:
            if step["done"] or step["step"] == "user":
                continue
            data = {"client_id": CLIENT, "redirect_uri": CLIENT} if step["step"] == "integration" else {}
            await client.api("POST", "/api/onboarding/" + step["step"], data)

        entries = await client.command("config_entries/get")
        # HA's normal onboarding adds these cloud/demo integrations. They are
        # unrelated to this isolated local protocol test; use only our fixtures.
        for entry in entries:
            if entry["domain"] in {"met", "google_translate", "radio_browser", "shopping_list"} and entry["source"] == "onboarding":
                await client.api("DELETE", "/api/config/config_entries/entry/" + entry["entry_id"])
        await client.command("homeassistant/expose_entity", assistants=["conversation"], entity_ids=["input_boolean.encore_lamp"], should_expose=True)
        await pair_satellite(client)
        print("HA onboarded; Encore ESPHome controls paired; real HA conversation owns the Encore lamp")
        print(f"Local credentials: {credentials_path} (mode 0600; values not printed)")
        await client.ws.close()


if __name__ == "__main__":
    asyncio.run(provision())
