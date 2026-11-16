"""Real HA/Noise/Assist proof, with explicitly virtual audio and STT/TTS fixtures.

Runs only against the fixed loopback lab. Re-pairs the Encore lab entry and
restarts its supervised virtual satellite; does not contact production HA.
"""

import asyncio
import hashlib
import json
import math
import struct
import subprocess
import time

import aiohttp
from aioesphomeapi import APIClient, InvalidEncryptionKeyAPIError
from aioesphomeapi.api_pb2 import VoiceAssistantAudio, VoiceAssistantResponse

from client import HA, STATE, login
from provision import pair_satellite, provision

SATELLITE = "http://127.0.0.1:8099"
PCM = [(i * 997 % 10001) - 5000 for i in range(8000)]
PCM_BYTES = struct.pack("<8000h", *PCM)
INPUT_SHA = hashlib.sha256(PCM_BYTES).hexdigest()
# Independent expected waveform: compare every byte, not just "audio arrived".
TONE = b"".join(struct.pack("<h", round(6000 * math.sin(i * 2 * math.pi * 440 / 16000))) for i in range(16000))


class Lab:
    def __init__(self, http, ha):
        self.http, self.ha = http, ha
        self.mark = ""
        self.events = []
        self.digest = hashlib.sha256()

    async def state(self):
        async with self.http.get(SATELLITE + "/state") as response:
            return await response.json()

    async def trace(self):
        async with self.http.get(SATELLITE + "/trace") as response:
            return [t for t in await response.json() if t["time"] > self.mark]

    async def control(self, kind, *, status=200, **kwargs):
        async with self.http.post(SATELLITE + "/command", json={"type": kind, **kwargs}) as response:
            assert response.status == status, f"{kind}: HTTP {response.status}: {await response.text()}"
            if status == 200:
                return await response.json()

    async def until(self, predicate, label, timeout=15):
        try:
            async with asyncio.timeout(timeout):
                while True:
                    value = await predicate()
                    if value:
                        return value
                    await asyncio.sleep(0.025)
        except TimeoutError:
            raise AssertionError(f"Timed out waiting for {label}") from None

    async def connected(self):
        return (await self.state())["session"]["connected"]

    async def has_trace(self, kind):
        return next((t for t in await self.trace() if t["kind"] == kind), None)

    async def event(self, kind):
        return next((e["data"] or {"observed": True} for e in self.events if e["event_type"] == kind), None)

    async def listen(self):
        self.events_ws = await self.http.ws_connect(HA + "/api/websocket")
        await self.events_ws.receive_json()
        await self.events_ws.send_json({"type": "auth", "access_token": self.ha.token})
        assert (await self.events_ws.receive_json())["type"] == "auth_ok"
        await self.events_ws.send_json({"id": 1, "type": "subscribe_events"})
        assert (await self.events_ws.receive_json())["success"]

        async def collect():
            async for message in self.events_ws:
                if message.type == aiohttp.WSMsgType.TEXT:
                    event = json.loads(message.data).get("event")
                    if event and event["event_type"].startswith("encore_fixture_"):
                        self.events.append(event)

        self.collector = asyncio.create_task(collect())

    async def prepare(self, scenario="success"):
        await self.control("hold_playback", value=False)
        await self.until(self.connected, "HA reconnect")
        assert (await self.state())["session"]["phase"] == "idle"
        await self.ha.api("POST", "/api/services/input_boolean/turn_off", {"entity_id": "input_boolean.encore_lamp"})
        await self.ha.api("POST", "/api/services/input_select/select_option", {"entity_id": "input_select.encore_scenario", "option": scenario})
        await self.ha.api("POST", "/api/services/tts/clear_cache", {})
        self.events.clear()
        traces = await self.trace()
        if traces:
            self.mark = traces[-1]["time"]
        return (await self.state())["playback"]

    async def begin(self):
        await self.control("start")
        await self.until(lambda: self.has_trace("VOICE_ASSISTANT_STT_START"), "real STT start")

    async def feed(self):
        for i in range(0, len(PCM), 320):
            await self.control("capture", samples=PCM[i:i + 320])
            await asyncio.sleep(0.02)

    async def finish_input(self):
        await self.control("end")
        event = await self.until(lambda: self.event("encore_fixture_audio"), "HA consumed audio")
        assert event["bytes"] == len(PCM_BYTES) and event["sha256"] == INPUT_SHA, event
        return event

    async def lamp(self):
        return (await self.ha.api("GET", "/api/states/input_boolean.encore_lamp"))["state"]

    async def idle(self):
        state = await self.state()
        return state if state["session"]["connected"] and state["session"]["phase"] == "idle" else None

    async def success(self, name="turn"):
        before = await self.prepare()
        await self.begin()
        await self.feed()
        audio = await self.finish_input()
        await self.until(lambda: self.has_trace("playback_drained"), "actual virtual drain")
        after = await self.until(self.idle, "idle after playback")
        p, state = after["playback"], after["session"]
        assert p["received"] - before["received"] == 16000
        assert p["played"] - before["played"] == 16000 and p["queued"] == 0
        self.digest.update(TONE)
        assert p["sha256"] == self.digest.hexdigest(), "TTS PCM changed in transit"
        assert state["heard"] == "Turn on the Encore lamp" and state["reply"] == "Turned on the switch", state
        assert await self.lamp() == "on", "Real HA intent did not change its helper"
        print(f"PASS {name}: HA received {audio['bytes']} bytes sha256={INPUT_SHA}; real intent=on; played=16000; drained=1; output sha256={p['sha256']}")

    async def drain(self):
        before = await self.prepare("short_tts")
        await self.control("hold_playback", value=True)
        await self.begin()
        await self.feed()
        await self.finish_input()
        await self.until(lambda: self.has_trace("VOICE_ASSISTANT_TTS_STREAM_END"), "stream end reached blocked drain")
        await asyncio.sleep(0.15)
        blocked = (await self.state())["playback"]
        assert blocked["queued"] == 3200 and blocked["played"] == before["played"]
        assert blocked["drains"] == before["drains"] and not await self.has_trace("playback_drained")
        await self.control("hold_playback", value=False)
        await self.until(lambda: self.has_trace("playback_drained"), "drain after release")
        after = (await self.until(self.idle, "idle"))["playback"]
        assert after["played"] - before["played"] == 3200 and after["drains"] == before["drains"] + 1
        self.digest.update(TONE[:6400])
        assert after["sha256"] == self.digest.hexdigest()
        print("PASS drain: 3200 samples received, 0 played and no completion while held; 3200 consumed then exactly one completion")

    async def cancel(self, phase):
        before = await self.prepare("slow_stt" if phase == "recognition" else "success")
        if phase == "playback":
            await self.control("hold_playback", value=True)
        await self.begin()
        await self.feed()
        if phase != "capture":
            await self.finish_input()
        if phase == "playback":
            async def queued():
                return (await self.state())["playback"]["queued"] > 0
            await self.until(queued, "blocked playback write")
        start = time.monotonic()
        await self.control("stop")
        assert time.monotonic() - start < 0.5, "Stop blocked behind playback/network"
        await self.until(self.connected, "reconnect after cancellation")
        await asyncio.sleep(0.2)
        after = await self.state()
        p = after["playback"]
        assert p["queued"] == 0 and p["drains"] == before["drains"]
        assert not after["session"].get("heard") and not after["session"].get("reply")
        assert not await self.has_trace("playback_drained")
        if phase != "playback":
            assert await self.lamp() == "off", "Cancelled request still ran an intent"
        else:
            received = p["received"] - before["received"]
            self.digest.update(TONE[:received * 2])
            assert p["played"] == before["played"] and p["sha256"] == self.digest.hexdigest()
        if phase == "recognition":
            await self.until(lambda: self.event("encore_fixture_cancelled"), "real HA cancelled delayed STT")
        print(f"PASS cancel {phase}: local queue=0, no false drain/late captions; encrypted reconnect recovered")

    async def error(self, scenario, code):
        before = await self.prepare(scenario)
        await self.begin()
        await self.feed()
        await self.finish_input()
        event = await self.until(lambda: self.has_trace("VOICE_ASSISTANT_ERROR"), "upstream error")
        assert event["data"]["code"] == code, event
        await self.until(self.connected, "recover after error")
        p = (await self.state())["playback"]
        assert p["received"] == before["received"] and p["drains"] == before["drains"]
        print(f"PASS {scenario}: real HA emitted {code}; no output/false completion; reconnected")

    async def empty_tts(self):
        before = await self.prepare("tts_error")
        await self.begin()
        await self.feed()
        await self.finish_input()
        await self.until(lambda: self.event("encore_fixture_tts_error"), "TTS failure actually exercised")
        await self.until(lambda: self.has_trace("VOICE_ASSISTANT_RUN_END"), "HA ended empty TTS turn")
        after = (await self.until(self.idle, "idle after empty TTS"))["playback"]
        assert after["received"] == before["received"] and after["drains"] == before["drains"]
        assert not await self.has_trace("playback_drained")
        assert not await self.has_trace("VOICE_ASSISTANT_ERROR"), "Pinned HA changed its empty-TTS behavior; review compatibility"
        print("PASS TTS failure: HA logged fixture failure but supplied normal empty stream/end, no ERROR; Encore made no playback claim or invented backend error")

    async def privacy(self):
        await self.prepare()
        await self.begin()
        await self.control("mute", value=True)
        state = (await self.state())["session"]
        assert state["muted"] and not state["connected"]
        await self.control("capture", samples=[32767] * 320)
        await self.control("start", status=409)
        await self.control("mute", value=False)
        await self.until(self.connected, "unmute reconnect")
        print("PASS privacy: mute revoked transport, ignored new capture and rejected start; unmute reconnected")

    async def peers(self):
        key = (STATE / "noise.psk").read_text()
        wrong = APIClient("127.0.0.1", 6053, "", noise_psk="YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXphYmNkZWY=")
        try:
            async with asyncio.timeout(5):
                await wrong.connect(login=True)
        except InvalidEncryptionKeyAPIError:
            pass
        else:
            raise AssertionError("Wrong-key encrypted connection accepted")
        finally:
            await wrong.disconnect()
        client = APIClient("127.0.0.1", 6053, "", noise_psk=key)
        async with asyncio.timeout(5):
            await client.connect(login=True)
            info = await client.device_info()
            entities, services = await client.list_entities_services()
        assert int(info.voice_assistant_feature_flags) == 7, info
        assert not entities and not services, "Invented entities/services advertised"
        await client.disconnect()
        for audio in (False, True):
            closed = asyncio.Event()
            client = APIClient("127.0.0.1", 6053, "", noise_psk=key)
            async def on_stop(expected):
                closed.set()
            async def start(*args):
                return 0
            async def stop(*args):
                pass
            async def data(*args):
                pass
            async with asyncio.timeout(5):
                await client.connect(login=True, on_stop=on_stop)
                client.subscribe_voice_assistant(handle_start=start, handle_stop=stop, handle_audio=data if audio else None)
                await closed.wait()
            await client.disconnect()
            assert await self.connected(), "Rejected peer displaced real HA"
        print("PASS peers: wrong key denied; capabilities exactly 7/API-audio mono; entity list completes; unsupported/competing subscribers rejected without displacing HA")

    async def wire_errors(self, entry_id):
        # Real Noise transport, with the pinned upstream API client injecting
        # invalid backend messages. This section is a peer fixture, not HA
        # itself emitting malformed messages.
        await self.ha.command("config_entries/disable", entry_id=entry_id, disabled_by="user")
        try:
            for name, message in (
                ("odd PCM", VoiceAssistantAudio(data=b"\x01")),
                ("oversized PCM", VoiceAssistantAudio(data=b"\x00" * 4098)),
                ("UDP-only response", VoiceAssistantResponse(port=1234)),
                ("rejected start", VoiceAssistantResponse(error=True)),
            ):
                closed = asyncio.Event()
                async def on_stop(expected):
                    closed.set()
                async def start(*args):
                    return 0
                async def noop(*args):
                    pass
                client = APIClient("127.0.0.1", 6053, "", noise_psk=(STATE / "noise.psk").read_text())
                try:
                    async with asyncio.timeout(5):
                        await client.connect(login=True, on_stop=on_stop)
                        client.subscribe_voice_assistant(handle_start=start, handle_stop=noop, handle_audio=noop)
                        await self.until(self.connected, "fixture peer subscribed")
                        # Deliberate low-level message injection; the client and
                        # protobuf schema are both locked to this HA baseline.
                        client._get_connection().send_message(message)
                        await closed.wait()
                    assert not await self.connected(), name
                    print(f"PASS encrypted peer fixture: {name} rejected/connection revoked")
                finally:
                    await client.disconnect()
        finally:
            await self.ha.command("config_entries/disable", entry_id=entry_id, disabled_by=None)
            await self.until(self.connected, "HA restored after negative wire tests")

    async def switch(self):
        before = await self.prepare()
        await self.control("hold_playback", value=True)
        await self.begin()
        await self.feed()
        await self.finish_input()
        async def queued():
            return (await self.state())["playback"]["queued"] > 0
        await self.until(queued, "blocked response before backend switch")
        await self.control("backend", backend="incubator")
        after = await self.state()
        assert after["session"]["backend"] == "incubator" and not after["session"]["connected"]
        assert after["playback"]["queued"] == 0 and after["playback"]["drains"] == before["drains"]
        received = after["playback"]["received"] - before["received"]
        self.digest.update(TONE[:received * 2])
        assert after["playback"]["sha256"] == self.digest.hexdigest()
        await self.control("start", status=409)  # No silent fallback to HA.
        reader, writer = await asyncio.open_connection("127.0.0.1", 6055)
        try:
            async def frame():
                async with asyncio.timeout(3):
                    header = await reader.readexactly(3)
                    size = int.from_bytes(header[1:], "big")
                    return header[0], await reader.readexactly(size)
            kind, payload = await frame()
            hello = json.loads(payload)
            assert kind == 1 and hello["protocol"] == 2 and not hello["aec"] and not hello["monitor"]
            await self.control("start")
            kind, payload = await frame()
            assert kind == 2 and json.loads(payload)["wake_word"] == "manual"
            await self.control("backend", backend="assist")
            assert await reader.read() == b"", "Old native transport survived switch"
        finally:
            writer.close()
            await writer.wait_closed()
            await self.control("backend", backend="assist")
            await self.control("hold_playback", value=False)
        await self.until(self.connected, "real HA reattached after native peer fixture")
        print("PASS backend selection: blocked Assist output flushed without completion; no fallback; native peer attached; old native transport revoked; HA reconnected")


async def main():
    await provision()
    async with aiohttp.ClientSession() as http:
        ha = await login(http)
        # Re-test actual discovery/configuration every run, for this named lab
        # entry only. Other entries and HA authentication are left intact.
        for entry in await ha.command("config_entries/get"):
            if entry["domain"] == "esphome" and entry["title"] == "Encore virtual satellite":
                await ha.api("DELETE", "/api/config/config_entries/entry/" + entry["entry_id"])
        subprocess.run(["amp", "orb", "service", "restart", "encore"], check=True)
        entry_id = await pair_satellite(ha, check_wrong_key=True)
        lab = Lab(http, ha)
        await lab.until(lab.connected, "initial encrypted HA subscription")
        await lab.listen()
        try:
            await lab.peers()
            await lab.wire_errors(entry_id)
            await lab.success("initial full turn")
            await lab.drain()
            for phase in ("capture", "recognition", "playback"):
                await lab.cancel(phase)
            await lab.privacy()
            await lab.error("stt_error", "stt-stream-failed")
            await lab.empty_tts()
            await lab.switch()
            await lab.prepare()
            await lab.control("stop")  # An idle stop must not wedge the next turn.
            await lab.success("turn after idle stop/errors/cancellations")
            await ha.api("POST", f"/api/config/config_entries/entry/{entry_id}/reload", {})
            await lab.until(lab.connected, "HA integration reload/reconnect")
            await lab.success("turn after HA integration reload")
            print("PASS real HA Assist integration suite (no physical audio or real speech models claimed)")
        finally:
            await lab.control("hold_playback", value=False)
            await lab.control("mute", value=False)
            await lab.events_ws.close()
            await lab.collector
            await ha.ws.close()


if __name__ == "__main__":
    asyncio.run(main())
