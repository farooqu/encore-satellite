"""Real pinned HA -> real upstream Voice Support; explicit audio/model fixtures.

Requires the supervised wake satellite/verifier and provisioned baseline HA.
Only modifies isolated loopback lab entries. Never use against a household HA.
"""
import asyncio
import hashlib
from pathlib import Path
import struct
import sys

import aiohttp

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "lab"))
from client import HA, STATE, login

SAT = "http://127.0.0.1:8100"
VERIFIER = "http://127.0.0.1:10900"
COMMAND = struct.pack("<8000h", *[(i * 997 % 10001) - 5000 for i in range(8000)])


async def until(check, label, timeout=20):
    try:
        async with asyncio.timeout(timeout):
            while True:
                if result := await check():
                    return result
                await asyncio.sleep(.025)
    except TimeoutError:
        raise AssertionError(f"Timed out: {label}") from None


async def ready(proof):
    async def alive():
        try: return await proof.get("/state")
        except aiohttp.ClientError: return None
    await until(alive, "satellite HTTP ready (including cold race build)", timeout=120)


class Proof:
    def __init__(self, http, ha):
        self.http, self.ha, self.events = http, ha, []
        self.mark = 0

    async def get(self, path):
        async with self.http.get(SAT + path) as r:
            return await r.json()

    async def control(self, kind, **kwargs):
        async with self.http.post(SAT + "/command", json={"type": kind, **kwargs}) as r:
            assert r.status == 200, (kind, r.status, await r.text())

    async def trace(self):
        return (await self.get("/trace"))[self.mark:]

    async def has(self, kind):
        return next((t for t in await self.trace() if t["kind"] == kind), None)

    async def connected(self):
        return (await self.get("/state"))["session"]["connected"]

    async def available(self):
        states = await self.ha.api("GET", "/api/states")
        return any(s["entity_id"] == self.entity and s["state"] != "unavailable" and not s["attributes"].get("restored") for s in states)

    async def subscribed(self, expected):
        traces = await self.get("/trace")
        last = next((t for t in reversed(traces) if t["kind"] == "subscribed"), None)
        return await self.connected() and last and last["data"]["wake_audio"] == str(expected).lower()

    async def reset(self):
        await until(self.connected, "connection")
        if self.verify and not await self.subscribed(True):
            await self.bootstrap()
        await asyncio.sleep(2.1)  # HA's real duplicate-wake cooldown.
        await self.ha.api("POST", "/api/services/input_boolean/turn_off", {"entity_id": "input_boolean.encore_lamp"})
        self.mark = len(await self.get("/trace"))
        self.events.clear()
        return (await self.get("/state"))["playback"]

    async def feed(self, pcm):
        for start in range(0, len(pcm), 640):
            block = pcm[start:start+640]
            await self.control("capture", samples=list(struct.unpack(f"<{len(block)//2}h", block)))
            await asyncio.sleep(.02)

    async def wake(self, pcm, model="fixture_model"):
        await self.feed(pcm)
        await self.control("wake", detection={"wake_word": model})

    async def labs(self, enabled):
        await self.ha.command("labs/update", domain="voice_support", preview_feature="next_generation_voice", enabled=enabled)
        self.verify = enabled

    async def bootstrap(self):
        # Pinned HA initially asks for configuration only for ANNOUNCE devices.
        # Use its public configuration path, honestly deactivate then activate
        # an available model. Do not advertise unimplemented ANNOUNCE to get it.
        # Transport subscription precedes HA's async platform/entity addition.
        await until(self.available, "HA Assist entity loaded after transport reconnect")
        await self.ha.command("assist_satellite/set_wake_words", entity_id=self.entity, wake_word_ids=[])
        config = await self.ha.command("assist_satellite/get_configuration", entity_id=self.entity)
        assert config["active_wake_words"] == [] and config["available_wake_words"][0]["id"] == "fixture_model", config
        await self.ha.command("assist_satellite/set_wake_words", entity_id=self.entity, wake_word_ids=["fixture_model"])
        await until(lambda:self.subscribed(True), "native HA configuration bootstrap")

    async def listen(self):
        self.ws = await self.http.ws_connect(HA + "/api/websocket")
        await self.ws.receive_json()
        await self.ws.send_json({"type":"auth", "access_token": self.ha.token})
        assert (await self.ws.receive_json())["type"] == "auth_ok"
        await self.ws.send_json({"id":1, "type":"subscribe_events", "event_type":"encore_fixture_audio"})
        assert (await self.ws.receive_json())["success"]
        async def collect():
            async for m in self.ws:
                if m.type == aiohttp.WSMsgType.TEXT and (event := m.json().get("event")):
                    self.events.append(event["data"])
        self.collector = asyncio.create_task(collect())

    async def success(self, pcm, boundary_ms, manual=False, label="accepted wake"):
        before = await self.reset()
        if manual:
            await self.control("start")
        else:
            await self.wake(pcm)
        await self.feed(COMMAND)
        await self.control("end")
        async def audio(): return self.events and self.events[-1]
        actual = await until(audio, "real STT consumed stream")
        # Independently use the upstream service's alignment, not adapter code.
        expected = (b"" if manual else pcm[(boundary_ms // 10) * 320:]) + COMMAND
        assert actual["bytes"] == len(expected) and actual["sha256"] == hashlib.sha256(expected).hexdigest(), (actual, len(expected), hashlib.sha256(expected).hexdigest())
        await until(lambda:self.has("playback_drained"), "real transport and virtual speaker drain")
        state = await self.get("/state")
        assert state["playback"]["played"] - before["played"] == 16000, state
        assert (await self.ha.api("GET", "/api/states/input_boolean.encore_lamp"))["state"] == "on"
        if not manual: assert await self.has("VOICE_ASSISTANT_WAKE_WORD_END")
        print(f"PASS {'manual' if manual else label}: STT bytes={actual['bytes']} sha256={actual['sha256']}; real intent=on; played=16000; actual drain")

    async def reject(self, pcm):
        before = await self.reset()
        await self.wake(pcm)
        await self.control("end")  # A final negative window, not a stalled stream.
        await until(lambda:self.has("13"), "real HA WAKE_WORD_REJECTED (new enum 13)")
        await until(lambda:self.has("disconnected"), "rejected generation revoked")
        await until(self.connected, "reconnect after rejection")
        state = await self.get("/state")
        assert not self.events and not await self.has("VOICE_ASSISTANT_STT_START"), await self.trace()
        assert state["playback"]["played"] == before["played"]
        assert not state["session"].get("heard") and not state["session"].get("reply")
        assert (await self.ha.api("GET", "/api/states/input_boolean.encore_lamp"))["state"] == "off"
        print("PASS real upstream rejection -> HA enum13 -> no STT/intent/audio; capture revoked and reconnect recovered")


async def upstream_verdict(http, pcm):
    async with http.ws_connect(VERIFIER + "/run") as ws:
        await ws.send_json(dict(type="run-start", satellite_id="independent-fixture-check", wake_word_phrase="okay nabu", wake_word_audio_ms=len(pcm)//32, silence_seconds=None))
        await ws.send_bytes(pcm)
        await ws.send_json({"type":"audio-end"})
        async with asyncio.timeout(10):
            async for m in ws:
                if m.type == aiohttp.WSMsgType.TEXT and m.json()["type"] in ("wake-word-end", "wake-word-rejected"):
                    return m.json()
    raise AssertionError("No real upstream verdict")


async def main():
    positive = (ROOT / ".amp/in/orb-backup/wake-assets/positive.pcm").read_bytes()
    original_bytes = len(positive)
    positive += bytes((-len(positive)) % 640)  # silence padding only, no truncation.
    assert 0 < len(positive) <= 32000, "Full speech does not fit actual 1s pre-roll; report prerequisite"
    async with aiohttp.ClientSession() as http:
        ha = await login(http)
        http_config = await ha.command("http/config")
        if http_config["active_config_type"] == "pending":
            assert http_config["pending"]["error"] is None, "Pending HA HTTP configuration failed validation"
            await ha.command("http/config/promote")
            http_config = await ha.command("http/config")
            assert http_config["pending"] is None and http_config["revert_at"] is None
        proof = Proof(http, ha)
        await ready(proof)
        await proof.listen()
        try:
            await proof.control("stop")
            # Test missing verifier before installing the actual app integration.
            for entry in await ha.command("config_entries/get"):
                if entry["domain"] == "voice_support":
                    await ha.api("DELETE", "/api/config/config_entries/entry/" + entry["entry_id"])
            entries = await ha.command("config_entries/get")
            wake_entry = next((e for e in entries if e["domain"] == "esphome" and e["title"] == "Encore wake evidence lab"), None)
            if not wake_entry:
                flow = await ha.api("POST", "/api/config/config_entries/flow", {"handler":"esphome"})
                flow = await ha.api("POST", "/api/config/config_entries/flow/" + flow["flow_id"], {"host":"127.0.0.1", "port":6056})
                assert flow["step_id"] == "encryption_key", flow
                flow = await ha.api("POST", "/api/config/config_entries/flow/" + flow["flow_id"], {"noise_psk":(STATE/"noise.psk").read_text()})
                assert flow["type"] == "create_entry", flow["type"]
                wake_entry = flow["result"]
            await until(lambda:proof.subscribed(False), "missing verifier disables wake audio")
            async def registered_entity():
                entities = await ha.command("config/entity_registry/list")
                return next((e["entity_id"] for e in entities if e["config_entry_id"] == wake_entry["entry_id"] and e["entity_id"].startswith("assist_satellite.")), None)
            proof.entity = await until(registered_entity, "HA wake entity registered after initial pairing")
            print("PASS missing verifier: HA negotiates ordinary API audio only")
            await proof.labs(True)
            await until(lambda:proof.subscribed(False), "Labs alone still insufficient")
            print("PASS Labs alone does not negotiate wake audio")
            flow = await ha.api("POST", "/api/config/config_entries/flow", {"handler":"voice_support"})
            flow = await ha.api("POST", "/api/config/config_entries/flow/"+flow["flow_id"], {"host":"127.0.0.1", "port":10900})
            assert flow["type"] == "create_entry", flow
            await proof.bootstrap()
            await until(lambda:proof.subscribed(True), "real HA wake-audio negotiation")
            async with http.get(VERIFIER + "/info") as r: info = await r.json()
            assert info["api_version"] == 1 and any(w["id"] == "okay_nabu" for w in info["wake_words"])
            verdict = await upstream_verdict(http, positive)
            assert verdict["type"] == "wake-word-end", verdict
            boundary = verdict["wake_word_output"]["wake_word_audio_ms"]
            print(f"PASS real app-voice Kaldi: untrimmed speech={original_bytes} bytes; transmitted={len(positive)} bytes/{len(positive)//32}ms; boundary={boundary}ms; {verdict['type']}")
            await proof.success(positive, boundary)
            negative = bytes(27520)
            rejected = await upstream_verdict(http, negative)
            assert rejected["type"] == "wake-word-rejected", rejected
            print("PASS independent real verifier negative:", rejected)
            await proof.reject(negative)
            await proof.success(positive, boundary)
            # Negotiated evidence with no buffered audio must not become an
            # ordinary unverified command. Adapter closes before sending start.
            await proof.reset()
            await proof.control("wake", detection={"wake_word":"fixture_model"})
            await until(lambda:proof.has("invalid_wake_evidence"), "empty evidence denied")
            assert not await proof.has("VOICE_ASSISTANT_RUN_START")
            await until(lambda:proof.has("disconnected"), "empty evidence generation revoked")
            await until(proof.connected, "recovery from empty evidence")
            print("PASS empty evidence rejected before HA pipeline; reconnected")
            await proof.reset()
            await proof.wake(positive, "unknown_model")
            await until(lambda:proof.has("invalid_wake_evidence"), "unknown model denied")
            assert not proof.events and not await proof.has("VOICE_ASSISTANT_RUN_START")
            await until(lambda:proof.has("disconnected"), "unknown model generation revoked")
            await until(proof.connected, "recovery from unknown model")
            print("PASS unknown model rejected before HA pipeline; reconnected")
            # Cancel a tentative wake immediately after submitting its evidence.
            # Later fixture cases force the verifier deadline deterministically.
            await proof.reset()
            before = (await proof.get("/state"))["playback"]
            await proof.wake(positive)
            await proof.control("stop")
            await until(proof.connected, "cancel reconnect")
            await asyncio.sleep(2.2)
            state = await proof.get("/state")
            assert not state["session"].get("heard") and not state["session"].get("reply")
            assert state["playback"]["played"] == before["played"]
            print("PASS cancel tentative wake: no late captions/audio; encrypted reconnect")
            await proof.reset()  # Reload configuration lost by HA on TCP reconnect.
            await proof.labs(False)
            await until(lambda:proof.subscribed(False), "live Labs disable and renegotiation")
            await proof.success(b"", 0, manual=True)
            await proof.labs(True)
            await until(lambda:proof.subscribed(True), "live Labs enable and renegotiation")
            await proof.success(positive, boundary)
            print("PASS live verification disable/enable; ordinary Assist and subsequent verified wake recover")
            before = await proof.reset()
            await proof.control("hold_playback", value=True)
            await proof.wake(positive)
            await proof.feed(COMMAND)
            await proof.control("end")
            async def queued(): return (await proof.get("/state"))["playback"]["queued"] > 0
            await until(queued, "held playback before live verification toggle")
            await proof.labs(False)
            await until(lambda:proof.subscribed(False), "resubscribe during held playback")
            await proof.control("hold_playback", value=False)
            await asyncio.sleep(.2)
            state = await proof.get("/state")
            assert state["playback"]["queued"] == 0 and state["playback"]["played"] == before["played"] and state["playback"]["drains"] == before["drains"], state
            await proof.labs(True)
            await until(lambda:proof.subscribed(True), "re-enable after playback revocation")
            await proof.success(positive, boundary)
            print("PASS live wake resubscription during held TTS flushes without playback/false drain and recovers")
        finally:
            await proof.control("hold_playback", value=False)
            await proof.control("stop")
            await proof.ws.close()
            await proof.collector
            await ha.ws.close()


if __name__ == "__main__":
    asyncio.run(main())
