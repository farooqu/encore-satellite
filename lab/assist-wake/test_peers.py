"""Encrypted aioesphomeapi peer fixtures, NOT real HA/verifier integration.

Exercises wire fields, capability denial and malformed host evidence. Temporarily
disables only the wake lab HA entry, restoring it and the configured host on exit.
"""
import asyncio
from pathlib import Path
import struct
import subprocess
import sys
import time

import aiohttp
from aioesphomeapi import APIClient
from aioesphomeapi.api_pb2 import SubscribeVoiceAssistantRequest, VoiceAssistantEventResponse, VoiceAssistantRequest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from test import Proof, ROOT, STATE, login, ready, until


def satellite(phrase):
    subprocess.run(["amp", "orb", "service", "stop", "assist-wake-satellite"], cwd=ROOT, stdout=subprocess.DEVNULL)
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if not subprocess.check_output(["ss", "-ltnH", "sport = :8100"], text=True).strip(): break
        time.sleep(.1)
    else: raise AssertionError("Stopped supervised satellite did not release port")
    time.sleep(.3)  # Let the service manager reap the stopped process group.
    command = "go run -race ./lab/assist-wake/satellite"
    if phrase:
        command = f"ENCORE_WAKE_PHRASE='{phrase}' " + command
    subprocess.run(["amp", "orb", "service", "start", "assist-wake-satellite", "--command", command, "--port", "8100"], cwd=ROOT, check=True, stdout=subprocess.DEVNULL)


async def peer(wake_audio, flags=None):
    client = APIClient("127.0.0.1", 6056, "", noise_psk=(STATE/"noise.psk").read_text())
    await client.connect(login=True)
    starts, audio, requests = [], [], []
    client._connection.add_message_callback(requests.append, (VoiceAssistantRequest,))
    async def start(*args): starts.append(args); return 0
    async def stop(expected): pass
    async def receive(data, data2):
        assert not data2
        audio.append(data)
    if flags is None:
        client.subscribe_voice_assistant(handle_start=start, handle_stop=stop, handle_audio=receive, wake_word_audio=wake_audio)
    else:
        client._connection.send_message(SubscribeVoiceAssistantRequest(subscribe=True, flags=flags))
    return client, starts, audio, requests


async def main():
    async with aiohttp.ClientSession() as http:
        ha = await login(http)
        entry = next(e for e in await ha.command("config_entries/get") if e["domain"] == "esphome" and e["title"] == "Encore wake evidence lab")
        await ha.command("config_entries/disable", entry_id=entry["entry_id"], disabled_by="user")
        p = Proof(http, ha)
        try:
            for enabled in (False, True):
                satellite("okay nabu" if enabled else "")
                await ready(p)
                client = APIClient("127.0.0.1",6056,"",noise_psk=(STATE/"noise.psk").read_text())
                await client.connect(login=True)
                info = await client.device_info()
                assert info.voice_assistant_feature_flags == (135 if enabled else 7), info
                await client.disconnect()
                print(f"PASS peer capability: explicit models={enabled}, advertised={info.voice_assistant_feature_flags}")
                # Wake subscription without API_AUDIO, or without advertised
                # support, must never attach a voice owner.
                client, _, _, _ = await peer(False, flags=8 if enabled else 12)
                async def denied(): return not client.is_connected
                await until(denied, "unsupported subscription denied")
                assert not await p.connected()
                await client.disconnect()
                print("PASS peer unsupported wake subscription denied without ownership")
                if not enabled: continue
                for negotiated in (False, True):
                    client, starts, audio, requests = await peer(negotiated)
                    await until(p.connected, "fixture subscribed")
                    # Asymmetric PCM catches byte swapping/reordering; score
                    # 255 must not become a made-up HA probability of one.
                    samples = [i*47-7000 for i in range(320)]
                    await p.control("capture", samples=samples)
                    await p.control("wake", detection={"wake_word":"fixture_model", "probability":255, "cutoff":200})
                    async def received(): return starts and audio
                    await until(received, "wire start and pre-roll")
                    metadata = starts[0][4] if negotiated else None
                    assert starts[0][3] == "okay nabu" and b"".join(audio) == struct.pack("<320h", *samples), (starts, audio)
                    fields = {f.number for f, value in requests[0].ListFields()}
                    assert not fields & {8,9,10}, fields  # no invented scores/channel2
                    if negotiated:
                        assert fields & {6,7} == {6,7}, fields
                        assert metadata.wake_word_id == "fixture_model" and metadata.audio_bytes == 640 and metadata.audio_bytes2 == 0
                        assert metadata.probability == 0 and metadata.probability_cutoff == 0, metadata
                    else:
                        assert not fields & {6,7}, fields
                    # Real wire rejection fixture exercises generation fencing.
                    client._connection.send_message(VoiceAssistantEventResponse(event_type=13))
                    await until(denied, "rejected connection revoked")
                    assert (await p.get("/state"))["session"]["reason"] == "wake_rejected"
                    await client.disconnect()
                    print(f"PASS peer negotiated={negotiated}: exact640-byte PCM, phrase/model metadata, incompatible scores omitted; rejection fences")
                client, starts, _, _ = await peer(True)
                await until(p.connected, "malformed evidence peer")
                await p.control("capture", samples=[-2, 7, 45])
                await p.control("wake", detection={"wake_word":"fixture_model"})
                await until(denied, "fractional-millisecond evidence rejected")
                assert not starts
                await client.disconnect()
                print("PASS peer malformed host evidence:6 bytes denied before a start reaches peer")
        finally:
            satellite("okay nabu")
            await ready(p)
            await ha.command("config_entries/disable", entry_id=entry["entry_id"], disabled_by=None)
            await ha.ws.close()


if __name__ == "__main__":
    asyncio.run(main())
