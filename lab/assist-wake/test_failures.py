"""Real HA with an explicitly FAKE Voice Support boundary for deadline faults.

These cases do not establish real upstream recognition. The real verifier cases
live in test.py. Also checks a real unsupported phrase and HA availability.
"""
import asyncio
import hashlib
from pathlib import Path
import subprocess
import sys

import aiohttp
from aiohttp import web

sys.path.insert(0, str(Path(__file__).resolve().parent))
from test import Proof, ROOT, login, ready, until
from test_peers import satellite


async def configure(ha, port):
    for entry in await ha.command("config_entries/get"):
        if entry["domain"] == "voice_support":
            await ha.api("DELETE", "/api/config/config_entries/entry/" + entry["entry_id"])
    flow = await ha.api("POST", "/api/config/config_entries/flow", {"handler":"voice_support"})
    assert flow["type"] == "form", flow
    flow = await ha.api("POST", "/api/config/config_entries/flow/" + flow["flow_id"], {"host":"127.0.0.1", "port":port})
    assert flow["type"] == "create_entry", flow


async def main():
    observed, connections = [], set()
    mode = "timeout"
    async def info(request):
        return web.json_response({"name":"DEADLINE FIXTURE, NOT upstream", "version":"fixture", "api_version":1, "wake_words":[{"id":"okay_nabu", "phrase":"okay nabu"}]})
    async def run(request):
        ws = web.WebSocketResponse()
        await ws.prepare(request)
        connections.add(ws)
        data, header = bytearray(), None
        try:
            async for message in ws:
                if message.type == aiohttp.WSMsgType.BINARY:
                    data.extend(message.data)
                elif message.type == aiohttp.WSMsgType.TEXT:
                    msg = message.json()
                    if msg["type"] == "run-start":
                        header = msg
                        if mode == "disconnect": await ws.close(); break
                        if mode == "malformed":
                            await ws.send_json({"type":"wake-word-end", "wake_word_output":{"wake_word_audio_ms":-1}})
                    # No verdict or endpoint: exercise HA's actual deadline.
        finally:
            observed.append({"header":header, "bytes":len(data), "sha256":hashlib.sha256(data).hexdigest()})
            connections.discard(ws)
        return ws
    app = web.Application()
    app.router.add_get("/info", info)
    app.router.add_get("/run", run)
    runner = web.AppRunner(app)
    await runner.setup()
    await web.TCPSite(runner, "127.0.0.1", 10901).start()
    positive = (ROOT/".amp/in/orb-backup/wake-assets/positive.pcm").read_bytes()
    positive += bytes((-len(positive))%640)
    async with aiohttp.ClientSession() as http:
        ha = await login(http)
        p = Proof(http, ha)
        await p.listen()
        entry = next(e for e in await ha.command("config_entries/get") if e["domain"] == "esphome" and e["title"] == "Encore wake evidence lab")
        entities = await ha.command("config/entity_registry/list")
        p.entity = next(e["entity_id"] for e in entities if e["config_entry_id"] == entry["entry_id"] and e["entity_id"].startswith("assist_satellite."))
        try:
            await p.labs(True)
            await configure(ha,10900)
            # A real upstream app cannot verify an unsupported installed phrase.
            satellite("not a supported wake phrase")
            await ready(p)
            await until(p.connected, "unsupported phrase reconnect")
            await until(p.available, "unsupported phrase HA entity loaded")
            await ha.command("assist_satellite/set_wake_words", entity_id=p.entity, wake_word_ids=[])
            await ha.command("assist_satellite/set_wake_words", entity_id=p.entity, wake_word_ids=["fixture_model"])
            await until(lambda:p.subscribed(False), "unsupported phrase does not request wake audio")
            p.verify = False
            await p.success(b"",0,manual=True)
            print("PASS real app unsupported phrase: no wake-audio negotiation; ordinary Assist still works")
            satellite("okay nabu")
            await ready(p)
            await until(p.connected, "restored model reconnect")
            await configure(ha,10901)
            p.verify = True
            await p.bootstrap()
            for mode in ("timeout", "malformed", "disconnect"):
                began = asyncio.get_running_loop().time()
                await p.success(positive,len(positive)//32,label="HA fallback (NOT verifier acceptance)")
                assert not connections, "HA left the fixture websocket open after the run"
                print(f"PASS FIXTURE verifier {mode}: HA failed open, NOT verified; satellite boundary860ms preserved; websocket closed; elapsed={asyncio.get_running_loop().time()-began:.2f}s")
            assert any(o["header"]["wake_word_audio_ms"] == 860 and o["bytes"] >= len(positive) for o in observed)
            # Keep the verifier waiting and cancel after the real HA run starts.
            mode = "timeout"
            await p.reset()
            await p.wake(positive)
            await until(lambda:p.has("VOICE_ASSISTANT_RUN_START"), "HA run before forced cancellation")
            await p.control("stop")
            async def closed(): return not connections
            await until(closed,"cancel closes verifier websocket")
            await until(p.connected,"cancel reconnect")
            assert not p.events
            state = await p.get("/state")
            assert not state["session"].get("heard") and not state["session"].get("reply")
            print("PASS FIXTURE pending verifier cancellation: real HA closed run websocket; no STT/captions")
            await p.reset()
            await p.wake(positive)
            async def pending(): return bool(connections)
            await until(pending, "verifier pending before live renegotiation")
            await p.labs(False)
            await until(lambda:p.subscribed(False), "live renegotiation revokes capture")
            await until(closed, "live renegotiation aborts old HA verifier run", timeout=1)
            assert not p.events
            await p.success(b"",0,manual=True)
            await p.labs(True)
            await until(lambda:p.subscribed(True), "live renegotiation retains model configuration")
            print("PASS FIXTURE live renegotiation aborts pending verifier without TCP disconnect; manual and verified subscriptions recover")
            # Actual disconnected satellite, not HA config-entry unload. The
            # manager unloads Assist on disconnect (not Entity.available).
            subprocess.run(["amp","orb","service","stop","assist-wake-satellite"],cwd=ROOT,check=True,stdout=subprocess.DEVNULL)
            await asyncio.sleep(1)
            assert not subprocess.check_output(["ss","-ltnH","sport = :6056"],text=True).strip()
            state = await ha.api("GET","/api/states/"+p.entity)
            assert state["state"] == "unavailable" and state["attributes"].get("restored"), state
            print("OBSERVED real HA availability: satellite listener absent; assist_satellite unavailable/restored by manager platform unload")
        finally:
            satellite("okay nabu")
            await ready(p)
            await p.labs(True)
            await configure(ha,10900)
            await p.ws.close()
            await p.collector
            await ha.ws.close()
            await runner.cleanup()


if __name__ == "__main__":
    asyncio.run(main())
