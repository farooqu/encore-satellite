"""Real MA + C++ Sendspin + Voice Incubator, with virtual capture/output.

Run with lab/.venv/bin/python. No production credentials or physical audio.
Each measurement checks rendered PCM RMS, not just a requested gain field.
"""

import argparse
import asyncio
import importlib.util
import json
from pathlib import Path
import statistics
import struct
import sys

import aiohttp

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "lab"))
from client import login

sys.path.insert(0, str(ROOT / "lab/incubator"))
from test_native import Wire, advert, FIXTURES, until
from zeroconf.asyncio import AsyncZeroconf

spec = importlib.util.spec_from_file_location(
    "ma_lab", Path(__file__).parent / "server/control.py"
)
ma_lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ma_lab)


class Proof:
    def __init__(self, http, ha):
        self.http, self.ha = http, ha
        self.player = None
        self.measurements = []

    async def request(self, method, path, body=None, status=200, **kwargs):
        async with self.http.request(
            method, "http://127.0.0.1:8099" + path, json=body, **kwargs
        ) as response:
            assert response.status == status, (
                path,
                response.status,
                await response.text(),
            )
            if status == 200:
                return await response.json()

    async def ma(self, command, **args):
        return await asyncio.to_thread(ma_lab.api, command, args)

    async def music(self):
        return await self.request("GET", "/music/state")

    async def state(self):
        return await self.request("GET", "/state")

    async def control(self, kind, **data):
        return await self.request("POST", "/command", dict(type=kind, **data))

    async def connected(self):
        return (await self.state())["session"]["connected"]

    async def idle(self):
        state = await self.state()
        voice = state["session"]
        return state if voice["connected"] and voice["phase"] == "idle" else None

    async def lamp(self):
        return (await self.ha.api("GET", "/api/states/input_boolean.encore_lamp"))["state"]

    async def measure(self, label, expected, voice=False):
        # Exclude the lab's bounded 20 ms ramp before checking steady state.
        await asyncio.sleep(0.08)
        frame = (await self.music())["played_frames"]
        await asyncio.sleep(0.4)
        blocks = [
            b
            for b in await self.request("GET", "/music/blocks")
            if b["frame"] >= frame and b["input_rms"] > 500
        ]
        assert len(blocks) >= 12, (
            label,
            "not enough real non-silent rendered music",
            len(blocks),
        )
        ratios = [b["output_rms"] / b["input_rms"] for b in blocks]
        actual = statistics.median(ratios)
        assert abs(actual - expected) < 0.003, (label, expected, actual)
        if voice:
            assert any(b["voice_samples"] > 0 for b in blocks), (
                label,
                "no concurrent voice PCM",
            )
        result = {
            "case": label,
            "expected_gain": expected,
            "measured_gain": round(actual, 6),
            "blocks": len(blocks),
            "frames": sum(b["frames"] for b in blocks),
        }
        self.measurements.append(result)
        print("PASS " + json.dumps(result), flush=True)

    async def prepare(self):
        await self.request("POST", "/music/enable", {"enabled": True})

        async def player():
            return next(
                (
                    p
                    for p in await self.ma("players/all")
                    if p["name"] == "Encore Sendspin lab"
                    and p["available"]
                    and "volume_set" in p["supported_features"]
                ),
                None,
            )

        info = await until(
            player, "actual MA receiver registration and activated player role"
        )
        self.player = info["player_id"]
        await self.ma(
            "config/players/save",
            player_id=self.player,
            values={"volume_normalization": False},
        )
        await self.ma("players/cmd/volume_set", player_id=self.player, volume_level=100)
        await self.ma("players/cmd/volume_mute", player_id=self.player, muted=False)
        await self.request("POST", "/music/play")
        await self.ma(
            "player_queues/play_media",
            queue_id=self.player,
            media=ma_lab.BASE + "/lab/fixture.wav",
            option="replace",
        )
        initial = (await self.music())["played_frames"]

        async def streaming():
            blocks = await self.request("GET", "/music/blocks") or []
            return (
                sum(
                    b["frames"]
                    for b in blocks
                    if b["frame"] >= initial and b["input_rms"] > 500
                )
                > 24000
            )

        await until(
            streaming, "MA/FFmpeg startup and actual consumed Sendspin PCM"
        )
        await self.measure("MA PCM playback at user gain 1.0", 1)

    async def safety(self):
        await self.ma("players/cmd/volume_mute", player_id=self.player, muted=True)
        await self.measure("MA user mute", 0)
        await self.ma("players/cmd/volume_set", player_id=self.player, volume_level=62)
        await self.measure("volume change cannot clear user mute", 0)
        await self.ma("players/cmd/volume_mute", player_id=self.player, muted=False)
        await self.measure("explicit user unmute uses current volume", 0.62)
        await self.control("stop")
        await self.measure("physical stop latches local music silent", 0)
        await self.request("POST", "/music/play")
        await self.measure("explicit play releases stop latch", 0.62)
        await self.control("mute", value=True)
        await self.measure("privacy immediately silences music", 0)
        await self.control("mute", value=False)
        await self.measure("privacy release does not autoplay music", 0)
        await self.request("POST", "/music/play")
        await self.measure("explicit play after privacy", 0.62)

        old = await self.music()
        await self.request("POST", "/music/enable", {"enabled": False})
        current = await self.music()
        await asyncio.sleep(0.25)
        after = await self.music()
        assert (
            not after["enabled"] and after["gain"] == 0 and after["queued_frames"] == 0
        )
        assert after["played_frames"] == current["played_frames"]
        # An old process/stream cannot revive playback after disabling.
        await self.request(
            "POST",
            "/music/pcm",
            status=409,
            data=b"\x01\x00\x02\x00",
            headers={
                "X-Encore-Generation": str(old["generation"]),
                "X-Encore-Stream": str(old["stream"]),
            },
        )
        print(
            "PASS feature off removes music output and rejects old-generation PCM",
            flush=True,
        )
        await self.request("POST", "/music/enable", {"enabled": True})
        assert (await self.music())["generation"] > old["generation"]
        await self.request(
            "POST",
            "/music/pcm",
            status=409,
            data=b"\x01\x00\x02\x00",
            headers={
                "X-Encore-Generation": str(old["generation"]),
                "X-Encore-Stream": str(old["stream"]),
            },
        )
        # MA can send a NEW stream on rejoining its still-playing queue. Old
        # process PCM must remain rejected, not mistaken for that fresh stream.
        await self.prepare()

    async def native(self):
        wire = Wire()
        proxy = await asyncio.start_server(wire.accept, "127.0.0.1", 6057)
        zc = AsyncZeroconf()
        info = advert("encore-incubator-proof", 6057, "02:00:00:ec:00:01")

        async def app(method, path, body=None):
            async with self.http.request(
                method, "http://127.0.0.1:8100" + path, json=body
            ) as response:
                assert response.status == 200, (path, await response.text())
                return await response.json()

        async def send(pcm, realtime=True):
            for offset in range(0, len(pcm), 2048):
                part = pcm[offset : offset + 2048]
                await self.control(
                    "capture",
                    samples=list(struct.unpack("<" + "h" * (len(part) // 2), part)),
                )
                if realtime:
                    await asyncio.sleep(len(part) / 32000)

        async def wake():
            pre = (FIXTURES / "wake.pcm").read_bytes()
            await send(bytes(32000 - len(pre)) + pre, False)
            await self.control(
                "wake",
                detection={
                    "wake_word": "okay_nabu",
                    "probability": 173,
                    "max_probability": 231,
                    "cutoff": 51,
                },
            )

        audio = bytes(6400) + (FIXTURES / "command.pcm").read_bytes() + bytes(64000)
        spec = {
            "description": "turn on the lamp",
            "examples": ["turn on the lamp"],
            "slots": {"entity": ["input_boolean.encore_lamp"]},
            "intent": "HassTurnOn",
            "response": "The lamp is on.",
        }
        try:
            await zc.async_register_service(info)
            await until(
                self.connected, "real Incubator mDNS/native handshake", seconds=90
            )
            await app("PUT", "/api/settings", {"keep_audio": True, "sounds": False})
            await app("PUT", "/api/commands/encore_lamp", {"spec": spec})
            await self.ha.api(
                "POST",
                "/api/services/input_boolean/turn_off",
                {"entity_id": "input_boolean.encore_lamp"},
            )
            await self.control("hold_playback", value=True)
            wire.frames.clear()
            await wake()
            await self.measure("Incubator wake and microphone listening", 0.2)
            await self.ma("players/cmd/volume_set", player_id=self.player, volume_level=35)
            await self.measure("MA user volume changed during listening", 0.07)
            await send(audio)

            async def output_ended():
                return bool(wire.bodies("app", 0x11))

            await until(
                output_ended, "real Kaldi/CTC/HA/Piper response", seconds=40
            )
            assert await self.lamp() == "on"
            assert not wire.bodies("encore", 6)
            await self.measure("Incubator held Piper response before drain", 0.07)
            mark = (await self.music())["played_frames"]
            await self.control("hold_playback", value=False)
            await self.measure("Incubator concurrent Piper and music", 0.07, voice=True)
            await until(self.idle, "native HANGUP after actual drain")
            blocks = [
                b
                for b in await self.request("GET", "/music/blocks")
                if b["frame"] >= mark and b["voice_samples"]
            ]
            assert blocks and all(b["ducking"] for b in blocks)
            await self.measure("Incubator restores the new user volume after HANGUP", 0.35)
            assert len(wire.bodies("encore", 6)) == 1
            queue = await self.ma("player_queues/get", queue_id=self.player)
            assert queue["state"] == "playing", "local voice must not pause MA queue"
            await self.ma("players/cmd/volume_set", player_id=self.player, volume_level=100)

            spec.update(
                confirm=True,
                response={"confirm": "Turn on the lamp?", "done": "The lamp is on."},
            )
            await app("PUT", "/api/commands/encore_lamp", {"spec": spec})
            await self.ha.api(
                "POST",
                "/api/services/input_boolean/turn_off",
                {"entity_id": "input_boolean.encore_lamp"},
            )
            wire.frames.clear()
            await wake()
            await send(audio)

            async def followup():
                return any(
                    json.loads(b)["wake_word"] == "follow_up"
                    for b in wire.bodies("encore", 2)
                )

            await until(followup, "real upstream confirmation/follow-up")
            await self.measure("Incubator follow-up microphone stays ducked", 0.2)
            assert await self.lamp() == "off"
            await send(
                bytes(6400) + (FIXTURES / "follow.pcm").read_bytes() + bytes(64000)
            )
            await until(self.idle, "real follow-up recognition and response drain")
            assert await self.lamp() == "on"
            await self.measure("Incubator follow-up completes and restores", 1)

            # Reconnect the HA control socket, which has no idle reader.
            await self.ha.ws.close()
            await self.ha.connect()
            links = await self.ha.command("voice_incubator/satellites/list")
            timer = next(
                s["timer_list"]
                for s in links["satellites"]
                if s["id"] == "020000ec0001"
            )
            await self.ha.api(
                "POST",
                "/api/services/voice_timer_list/create_timer",
                {"entity_id": timer, "duration": 1, "name": "Sendspin ducking proof"},
            )

            async def ringing():
                return (await self.state())["session"]["alert"]

            await until(ringing, "actual HA timer and Incubator alert")
            await self.measure(
                "Incubator timer foreground ducks music", 0.2, voice=True
            )
            await self.control("stop")

            async def timer_stopped():
                return any(
                    json.loads(b)["type"] == "timer_stopped"
                    for b in wire.bodies("app", 0x12)
                )

            await until(timer_stopped, "real upstream timer cancellation")
            await self.measure("timer Stop cannot restore locally stopped music", 0)
            await self.request("POST", "/music/play")
            await self.measure("explicit play after timer Stop", 1)

            spec.update(confirm=False, response="The lamp is on.")
            await app("PUT", "/api/commands/encore_lamp", {"spec": spec})
            await self.ha.api(
                "POST",
                "/api/services/input_boolean/turn_off",
                {"entity_id": "input_boolean.encore_lamp"},
            )
            wire.frames.clear()
            await wake()
            await self.measure("next wake after timer Stop ducks music", 0.2)
            await send(audio)
            await until(self.idle, "real next turn after timer Stop")
            assert await self.lamp() == "on"
            assert wire.bodies("app", 0x10) and len(wire.bodies("encore", 6)) == 1
            assert not wire.bodies("encore", 1), "Timer recovery must not reconnect"
            await self.measure("post-timer voice completes and restores music", 1)
            print(
                "PASS real timer Stop → next wake/recognition/HA intent/Piper drain, same native transport, music ducks/restores",
                flush=True,
            )

            await self.ha.api("POST", "/api/services/input_boolean/turn_off", {"entity_id": "input_boolean.encore_lamp"})
            before = (await self.state())["playback"]["played"]
            wire.frames.clear()
            await wake()
            await self.measure("Incubator listening before feature-off", 0.2)
            await self.request("POST", "/music/enable", {"enabled": False})
            stopped = await self.music()
            await send(audio)
            after = await until(self.idle, "Incubator completes with Sendspin off")
            output_samples = sum(len(b) for b in wire.bodies("app", 0x10)) // 2
            assert after["playback"]["played"] - before == output_samples > 0
            assert len(wire.bodies("encore", 6)) == 1 and await self.lamp() == "on"
            music = await self.music()
            assert not music["enabled"] and music["gain"] == 0 and music["queued_frames"] == 0
            assert music["played_frames"] == stopped["played_frames"]
            print("PASS disabling Sendspin during a native turn preserves HA intent and full Piper drain", flush=True)
        finally:
            await self.control("hold_playback", value=False)
            await self.control("stop")
            await zc.async_unregister_service(info)
            await zc.async_close()
            wire.drop()
            proxy.close()
            await proxy.wait_closed()


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--output", type=Path, default=ROOT / ".amp/in/orb-backup/sendspin-proof"
    )
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    async with aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=60)) as http:
        ha = await login(http)
        proof = Proof(http, ha)
        try:
            await proof.prepare()
            await proof.safety()
            await proof.request("POST", "/music/record")
            await proof.native()
            async with http.get("http://127.0.0.1:8099/music.wav") as response:
                (args.output / "incubator-ducking.wav").write_bytes(
                    await response.read()
                )
            (args.output / "measurements.json").write_text(
                json.dumps(proof.measurements, indent=2) + "\n"
            )
            print(
                "PASS Sendspin + real Incubator (virtual mixer; no physical audio/AEC/sync claim)",
                flush=True,
            )
        finally:
            await proof.control("hold_playback", value=False)
            await proof.control("mute", value=False)
            if proof.player:
                await proof.ma("player_queues/stop", queue_id=proof.player)
            await proof.request("POST", "/music/enable", {"enabled": False})
            await ha.ws.close()


if __name__ == "__main__":
    asyncio.run(main())
