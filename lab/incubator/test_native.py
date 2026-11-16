"""Real Incubator/HA proof with synthetic capture and a clocked virtual speaker.

The transparent proxy records actual frames, not replacement service decisions.
The optional competing satellite is a named wire fixture; upstream elects.
"""

import asyncio
import hashlib
import io
import json
from pathlib import Path
import socket
import struct
import sys
import time
import wave

import aiohttp
from zeroconf import ServiceInfo
from zeroconf.asyncio import AsyncZeroconf

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "lab"))
from client import login  # noqa: E402

CONTROL = "http://127.0.0.1:8099"
APP = "http://127.0.0.1:8100"
KEY = "020000ec0001"
FIXTURES = Path.home() / ".local/state/encore-incubator/fixtures"
HEADER = struct.Struct(">BH")


async def until(check, description, seconds=30):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = await check()
        if value:
            return value
        await asyncio.sleep(0.05)
    raise AssertionError("Timed out: " + description)


class Wire:
    def __init__(self):
        self.frames = []
        self.writers = set()

    async def copy(self, reader, writer, direction):
        while True:
            header = await reader.readexactly(3)
            kind, size = HEADER.unpack(header)
            body = await reader.readexactly(size)
            self.frames.append((direction, kind, body))
            writer.write(header + body)
            await writer.drain()

    async def accept(self, reader, writer):
        peer = None
        tasks = []
        try:
            source, peer = await asyncio.open_connection("127.0.0.1", 6055)
            self.writers.update((writer, peer))
            tasks = [asyncio.create_task(self.copy(reader, peer, "app")),
                     asyncio.create_task(self.copy(source, writer, "encore"))]
            await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
        finally:
            for task in tasks:
                task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
            for stream in (writer, peer):
                if stream:
                    stream.close()
                    self.writers.discard(stream)

    def bodies(self, direction, kind):
        return [b for d, k, b in self.frames if (d, k) == (direction, kind)]

    def drop(self):
        for writer in list(self.writers):
            writer.close()


class Candidate:
    """A second microphone/speaker wire fixture, never an election fixture."""

    def __init__(self):
        self.writer = None
        self.frames = []

    async def frame(self, kind, body):
        if not isinstance(body, bytes):
            body = json.dumps(body).encode()
        self.writer.write(HEADER.pack(kind, len(body)) + body)
        await self.writer.drain()

    async def accept(self, reader, writer):
        self.writer = writer
        try:
            await self.frame(1, {"protocol": 2, "name": "election-peer-fixture",
                                 "mac": "02:00:00:ec:00:02", "model": "fixture",
                                 "aec": False, "listen": False})
            while True:
                kind, size = HEADER.unpack(await reader.readexactly(3))
                self.frames.append((kind, await reader.readexactly(size)))
                if kind == 0x11:
                    await self.frame(6, {})  # This fixture has no physical sink.
        except (asyncio.IncompleteReadError, ConnectionError):
            pass
        finally:
            writer.close()
            self.writer = None


def advert(name, port, mac):
    return ServiceInfo("_voice-incubator._tcp.local.",
                       name + "._voice-incubator._tcp.local.",
                       addresses=[socket.inet_aton("127.0.0.1")], port=port,
                       properties={"mac": mac}, server=name + ".local.")


async def main():
    wire = Wire()
    proxy = await asyncio.start_server(wire.accept, "127.0.0.1", 6057)
    zc = AsyncZeroconf()
    info = advert("encore-incubator-proof", 6057, "02:00:00:ec:00:01")
    async with aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=60)) as http:
        async def request(method, url, body=None):
            async with http.request(method, url, json=body) as response:
                text = await response.text()
                assert response.status == 200, (method, url, response.status, text)
                return json.loads(text)

        async def command(kind, **data):
            return await request("POST", CONTROL + "/command", dict(type=kind, **data))

        async def state():
            return await request("GET", CONTROL + "/state")

        async def connected():
            return (await state())["session"]["connected"]

        async def idle():
            s = await state()
            return s if s["session"]["connected"] and s["session"]["phase"] == "idle" else None

        async def send(pcm, realtime=True):
            for offset in range(0, len(pcm), 2048):
                part = pcm[offset:offset + 2048]
                await command("capture", samples=list(struct.unpack("<" + "h" * (len(part) // 2), part)))
                if realtime:
                    await asyncio.sleep(len(part) / 32000)

        async def latest():
            calls = (await request("GET", APP + "/api/calls"))["calls"]
            return await request("GET", APP + "/api/calls/" + calls[0]["id"])

        wake = (FIXTURES / "wake.pcm").read_bytes()
        assert len(wake) <= 32000, "Wake fixture exceeds actual one-second pre-roll"
        pre = bytes(32000 - len(wake)) + wake
        audio = bytes(6400) + (FIXTURES / "command.pcm").read_bytes() + bytes(64000)

        async def start(pre_roll=pre, word="okay_nabu"):
            await send(pre_roll, realtime=False)
            await command("wake", detection={"wake_word": word, "probability": 173,
                                             "max_probability": 231, "cutoff": 51})

        ha = await login(http)
        try:
            await command("backend", backend="incubator")
            await zc.async_register_service(info)
            await until(connected, "real upstream native handshake")
            hello = json.loads(wire.bodies("encore", 1)[-1])
            assert hello["protocol"] == 2 and hello["mac"] == "02:00:00:ec:00:01"
            assert not hello["aec"] and "wake_format" not in hello
            await request("PUT", APP + "/api/settings", {"keep_audio": True, "sounds": False})
            await request("PUT", APP + "/api/commands/encore_lamp", {"spec": {
                "title": "Encore lab lamp", "description": "turn on the lamp",
                "examples": ["turn on the lamp"], "slots": {"entity": ["input_boolean.encore_lamp"]},
                "intent": "HassTurnOn", "response": "The lamp is on.",
            }})
            print("PASS real mDNS/native HELLO: protocol 2, stable MAC, honest single-capture capabilities", flush=True)

            await ha.api("POST", "/api/services/input_boolean/turn_off", {"entity_id": "input_boolean.encore_lamp"})
            before = (await state())["playback"]
            wire.frames.clear()
            await start()
            await send(audio)
            result = await until(idle, "recognized turn, actual playback drain, upstream HANGUP")
            record = await latest()
            assert (await ha.api("GET", "/api/states/input_boolean.encore_lamp"))["state"] == "on"
            call_start = json.loads(wire.bodies("encore", 2)[0])
            assert call_start == {"wake_word": "okay_nabu", "preroll_bytes": 32000,
                                  "aec_preroll_bytes": 0, "probability": 173,
                                  "max_probability": 231, "cutoff": 51}
            stages = {s["stage"]: s for s in record["stages"]}
            assert stages["wake_kaldi"]["accepted"], stages
            assert stages["recognition"]["command"] == "encore_lamp", stages["recognition"]
            output = b"".join(wire.bodies("app", 0x10))
            played = result["playback"]["played"] - before["played"]
            assert played == len(output) // 2 > 0
            assert result["playback"]["drains"] == before["drains"] + 1
            assert len(wire.bodies("encore", 6)) == 1
            captured = b"".join(wire.bodies("encore", 3))
            assert captured == pre + audio
            print(f"PASS real Kaldi acceptance → CTC/TDT recognition → HA HassTurnOn → Piper PCM → drain: {played} samples", flush=True)

            # Independent upstream recording must contain the exact supplied
            # prefix, not a synthetic STT stub or duplicated second capture.
            async with http.get(APP + "/api/calls/" + record["id"] + "/audio/call") as response:
                assert response.status == 200
                with wave.open(io.BytesIO(await response.read())) as wav:
                    stored = wav.readframes(wav.getnframes())
            assert len(stored) >= len(pre) + len(audio) - 64000
            assert captured.startswith(stored)
            print("PASS exact original candidate scores, 32000-byte pre-roll and upstream WAV PCM prefix: "
                  + hashlib.sha256(stored).hexdigest(), flush=True)

            links = await ha.command("voice_incubator/satellites/list")
            link = next(s for s in links["satellites"] if s["id"] == KEY)
            entities = await ha.command("config/entity_registry/list")
            native_entities = [e["entity_id"] for e in entities if e.get("device_id") == link["device_id"]]
            assert native_entities == [link["timer_list"]], native_entities
            assert not any(e.startswith("assist_satellite.") for e in native_entities)
            timer_before = await ha.api("GET", "/api/states/" + link["timer_list"])

            # A short held reply distinguishes receipt from actual consumption.
            before = (await state())["playback"]
            wire.frames.clear()
            await command("hold_playback", value=True)
            await start()
            await send(audio)

            async def output_ended():
                return bool(wire.bodies("app", 0x11))

            await until(output_ended, "real upstream AUDIO_OUT_END with held speaker")
            await asyncio.sleep(0.25)
            held = (await state())["playback"]
            assert held["played"] == before["played"] and held["queued"] > 0
            assert not wire.bodies("encore", 6) and not wire.bodies("app", 0x13)
            await command("hold_playback", value=False)
            await until(idle, "upstream hangup after released playback")
            assert len(wire.bodies("encore", 6)) == 1
            kinds = [(d, k) for d, k, _ in wire.frames]
            assert kinds.index(("encore", 6)) < kinds.index(("app", 0x13))
            print("PASS held real Piper reply: no early PLAYBACK_DRAINED/HANGUP; release drains then upstream hangs up", flush=True)

            before = (await state())["playback"]
            wire.frames.clear()
            await command("hold_playback", value=True)
            await start()
            await send(audio)
            await until(output_ended, "held cancellable real reply")
            await command("stop")
            await asyncio.sleep(0.3)
            cancelled = (await state())["playback"]
            assert cancelled["queued"] == 0 and cancelled["played"] == before["played"]
            assert cancelled["drains"] == before["drains"] and not wire.bodies("encore", 6)
            assert json.loads(wire.bodies("encore", 4)[-1])["reason"] == "cancelled"
            await command("hold_playback", value=False)
            hellos = len(wire.bodies("encore", 1))
            wire.drop()
            async def reattached():
                return len(wire.bodies("encore", 1)) > hellos and await connected()
            await until(reattached, "new HELLO after cancelled real output", seconds=90)
            print("PASS cancel held real reply: immediate flush, no false drain, real upstream CALL_END(cancelled)", flush=True)

            # False candidate audio is still sent to the real verifier; no
            # local confidence filtering or locally invented rejection.
            await until(idle, "idle after cancellation reconnect")
            wire.frames.clear()
            await start(bytes(32000))
            await send(bytes(16000))
            async def hung_up():
                return bool(wire.bodies("app", 0x13))
            await until(hung_up, "real wake verifier rejection")
            assert json.loads(wire.bodies("app", 0x13)[-1])["reason"] == "wake_rejected"
            assert (await latest())["outcome"] == "wake rejected"
            rejected = await until(idle, "quiet idle after rejected candidate", seconds=1)
            assert rejected["session"]["reason"] == "wake_rejected"
            assert not wire.bodies("encore", 1), "Rejection must not reconnect healthy transport"
            await ha.api("POST", "/api/services/input_boolean/turn_off", {"entity_id": "input_boolean.encore_lamp"})
            await start()
            await send(audio)
            await until(idle, "immediate next real turn on the same connection")
            assert not wire.bodies("encore", 1)
            assert wire.bodies("app", 0x10) and len(wire.bodies("encore", 6)) == 1
            assert (await ha.api("GET", "/api/states/input_boolean.encore_lamp"))["state"] == "on"
            print("PASS real Kaldi rejection → quiet idle → immediate successful next turn, same native transport", flush=True)

            # Selection is core-owned: only inspect the real app and HA lifecycle.
            await command("backend", backend="assist")
            async def native_offline():
                satellites = (await request("GET", APP + "/api/satellites"))["satellites"]
                return any(s["key"] == KEY and not s["connected"] for s in satellites)
            await until(native_offline, "Incubator notices switched-away transport")
            timer_after = await ha.api("GET", "/api/states/" + link["timer_list"])
            assert timer_after["state"] == timer_before["state"] != "unavailable"
            assert next(s for s in (await ha.command("voice_incubator/satellites/list"))["satellites"]
                        if s["id"] == KEY)["device_id"] == link["device_id"]
            await command("backend", backend="incubator")
            await until(connected, "real app reconnect after selection", seconds=90)
            await until(idle, "idle after selection")
            print("PASS native disconnect/switch/reconnect: persistent HA timer entity stays available, not voice availability", flush=True)

            wire.frames.clear()
            await start()
            await send(bytes(6400) + (FIXTURES / "unknown.pcm").read_bytes() + bytes(64000))
            unknown = await until(idle, "real no-recognition response")
            assert unknown["session"].get("reason") == "not_recognized", unknown
            assert wire.bodies("app", 0x10) and len(wire.bodies("encore", 6)) == 1
            print("PASS real CTC/TDT no-recognition reply and playback completion after reconnect", flush=True)

            candidate = Candidate()
            peer_server = await asyncio.start_server(candidate.accept, "127.0.0.1", 6056)
            peer_info = advert("encore-election-fixture", 6056, "02:00:00:ec:00:02")
            try:
                await zc.async_register_service(peer_info)
                async def peer_connected():
                    return candidate.writer is not None
                await until(peer_connected, "real app discovers second satellite fixture")
                for probability, winner in ((241, "fixture"), (17, "encore")):
                    wire.frames.clear()
                    candidate.frames.clear()
                    await candidate.frame(2, {"wake_word": "okay_nabu", "preroll_bytes": 32000,
                                              "probability": probability, "max_probability": 249,
                                              "cutoff": 51})
                    await start()
                    # Both calls exist before either verifier can choose; the
                    # identical PCM makes probability the upstream tie-break.
                    for offset in range(0, len(pre + audio), 2048):
                        await candidate.frame(3, (pre + audio)[offset:offset + 2048])
                    await send(audio)
                    await until(idle, "real upstream arbitration finishes Encore candidate", seconds=90)
                    async def peer_hangup():
                        return next((json.loads(b) for k, b in candidate.frames if k == 0x13), None)
                    peer_end = await until(peer_hangup, "real upstream completes election fixture")
                    encore_end = json.loads(wire.bodies("app", 0x13)[-1])
                    assert not wire.bodies("encore", 1), "Election must not reconnect healthy transport"
                    if winner == "fixture":
                        assert encore_end["reason"] == "answered_elsewhere", encore_end
                        assert peer_end["reason"] != "answered_elsewhere", peer_end
                        assert not wire.bodies("app", 0x10)
                    else:
                        assert peer_end["reason"] == "answered_elsewhere", peer_end
                        assert encore_end["reason"] != "answered_elsewhere", encore_end
                        assert wire.bodies("app", 0x10)
                    print(f"PASS real upstream election: fixture probability {probability}/255, Encore 173/255 → {winner}; loser answered_elsewhere", flush=True)
                    await asyncio.sleep(1.1)  # Outside upstream's one-second utterance window.

                # Typed command is an explicit boundary fixture for the sender;
                # the target gets the real app's chime, Piper and native events.
                await request("PUT", APP + "/api/commands/encore_announce", {"spec": {
                    "description": "announce dinner", "examples": ["announce dinner"],
                    "announce": "Dinner is ready.",
                }})
                wire.frames.clear()
                before = (await state())["playback"]
                tried = await request("POST", APP + "/api/try", {
                    "text": "announce dinner", "satellite": "020000ec0002", "run_actions": True,
                })
                assert tried["outcome"] != "not recognized", tried
                async def announced():
                    return any(json.loads(b) == {"type": "announcement", "data": {"active": False}}
                               for b in wire.bodies("app", 0x12))
                await until(announced, "real out-of-call announcement ends")
                output = b"".join(wire.bodies("app", 0x10))
                after = (await state())["playback"]
                assert after["played"] - before["played"] == len(output) // 2 > 0
                assert after["queued"] == 0
                assert len(wire.bodies("encore", 6)) == len(wire.bodies("app", 0x11)) == 2
                assert not wire.bodies("app", 0x13)  # No invented local call.
                print("PASS real idle announcement: active true → chime drain → Piper drain → active false, no fake assistant", flush=True)
            finally:
                await zc.async_unregister_service(peer_info)
                if candidate.writer:
                    candidate.writer.close()
                peer_server.close()
                await peer_server.wait_closed()

            await request("PUT", APP + "/api/commands/encore_lamp", {"spec": {
                "description": "turn on the lamp", "examples": ["turn on the lamp"],
                "slots": {"entity": ["input_boolean.encore_lamp"]}, "intent": "HassTurnOn",
                "confirm": True, "response": {"confirm": "Turn on the lamp?", "done": "The lamp is on."},
            }})
            await ha.api("POST", "/api/services/input_boolean/turn_off", {"entity_id": "input_boolean.encore_lamp"})
            wire.frames.clear()
            await start()
            await send(audio)
            async def following_up():
                return next((json.loads(b) for b in wire.bodies("encore", 2)
                             if json.loads(b)["wake_word"] == "follow_up"), None)
            follow = await until(following_up, "real upstream confirmation opens follow-up")
            assert follow["preroll_bytes"] == follow["aec_preroll_bytes"] == 0
            assert (await ha.api("GET", "/api/states/input_boolean.encore_lamp"))["state"] == "off"
            await send(bytes(6400) + (FIXTURES / "follow.pcm").read_bytes() + bytes(64000))
            await until(idle, "real upstream handles follow-up yes")
            assert (await ha.api("GET", "/api/states/input_boolean.encore_lamp"))["state"] == "on"
            assert any(json.loads(b).get("listen") is True for b in wire.bodies("app", 0x13))
            print("PASS upstream asks confirmation, follow_up has zero pre-roll, real recognition of yes triggers HA action", flush=True)

            wire.frames.clear()
            before = (await state())["playback"]
            await ha.api("POST", "/api/services/voice_timer_list/create_timer", {
                "entity_id": link["timer_list"], "duration": 1, "name": "Encore proof",
            })
            async def ringing_and_played():
                s = await state()
                return s["session"]["alert"] and s["playback"]["played"] > before["played"]
            await until(ringing_and_played, "real HA timer finishes and upstream rings idle satellite")
            await command("stop")
            async def stopped_timer():
                return any(json.loads(b)["type"] == "timer_stopped" for b in wire.bodies("app", 0x12))
            await until(stopped_timer, "upstream stops timer after device stop event")
            assert any(json.loads(b)["type"] == "stop_timer" for b in wire.bodies("encore", 9))
            stopped = await state()
            assert not stopped["session"]["alert"] and stopped["playback"]["queued"] == 0
            print("PASS real HA duration timer → upstream timer_ringing/audio → immediate device Stop → upstream timer_stopped", flush=True)
        finally:
            await command("hold_playback", value=False)
            await command("stop")
            await ha.ws.close()
            await zc.async_unregister_service(info)
            await zc.async_close()
            wire.drop()
            proxy.close()
            await proxy.wait_closed()


if __name__ == "__main__":
    asyncio.run(main())
