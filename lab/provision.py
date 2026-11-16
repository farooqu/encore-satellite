"""Onboard ONLY this isolated loopback HA; never reads production credentials."""

import asyncio
import json
import secrets
import base64

import aiohttp

from client import CLIENT, HA, STATE, login


async def pair_satellite(client, *, check_wrong_key=False):
    entries = await client.command("config_entries/get")
    existing = next((e for e in entries if e["domain"] == "esphome" and e["title"] == "Encore virtual satellite"), None)
    if existing:
        return existing["entry_id"]
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
    if check_wrong_key:
        wrong = base64.b64encode(secrets.token_bytes(32)).decode()
        result = await client.api("POST", path, {"noise_psk": wrong})
        assert result.get("errors", {}).get("base") == "invalid_psk", "Wrong key was not rejected"
        print("PASS real mDNS discovery/configuration: wrong Noise key rejected")
    result = await client.api("POST", path, {"noise_psk": (STATE / "noise.psk").read_text()})
    assert result["type"] == "create_entry", "Correct key did not complete pairing"
    print("PASS real HA config flow: correct Noise key accepted")
    return result["result"]["entry_id"]


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
        if not any(e["domain"] == "encore_fixture" for e in entries):
            result = await client.api("POST", "/api/config/config_entries/flow", {"handler": "encore_fixture", "show_advanced_options": False})
            assert result["type"] == "create_entry", result["type"]
        for _ in range(100):
            states = await client.api("GET", "/api/states")
            if any(s["entity_id"] == "tts.encore_fixture_tts" for s in states):
                break
            await asyncio.sleep(0.1)
        else:
            raise AssertionError("Speech fixtures did not load")
        pipelines = await client.command("assist_pipeline/pipeline/list")
        ours = next((p for p in pipelines["pipelines"] if p["name"] == "Encore deterministic lab"), None)
        settings = dict(name="Encore deterministic lab", language="en", conversation_engine="conversation.home_assistant", conversation_language="en", stt_engine="stt.encore_fixture_stt", stt_language="en", tts_engine="tts.encore_fixture_tts", tts_language="en", tts_voice=None, wake_word_entity=None, wake_word_id=None)
        if ours is None:
            ours = await client.command("assist_pipeline/pipeline/create", **settings)
        await client.command("assist_pipeline/pipeline/set_preferred", pipeline_id=ours["id"])
        await client.command("homeassistant/expose_entity", assistants=["conversation"], entity_ids=["input_boolean.encore_lamp"], should_expose=True)
        await pair_satellite(client)
        print("HA onboarded; deterministic STT/TTS pipeline selected; real HA conversation owns the Encore lamp")
        print(f"Local credentials: {credentials_path} (mode 0600; values not printed)")
        await client.ws.close()


if __name__ == "__main__":
    asyncio.run(provision())
