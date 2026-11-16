"""Restart-bound selector through real HA, with virtual audio/BLE and a native peer.

Run after lab provisioning. Only the isolated Encore lab entry/state is changed.
No Incubator speech/model service, physical radio, GATT or Sendspin is simulated.
"""

import asyncio
import hashlib
import json
import struct
import sys

import aiohttp
from aioesphomeapi import APIClient

from client import HA, STATE, login
from test_assist import Lab

ASSIST = "Home Assistant Assist"
INCUBATOR = "Voice Incubator"


async def http_teardown(lab, apply_mode):
    """Hold an admitted body read across HA Apply, not just a TCP connection."""
    before = await lab.state()
    saved = json.loads((STATE / "settings.json").read_text())
    assert not before["session"]["muted"] and not saved["muted"]
    reader, writer = await asyncio.open_connection("127.0.0.1", 8099)
    body = b'{"type":"mute","value":true}'
    try:
        writer.write(
            b"POST /command HTTP/1.1\r\nHost: localhost\r\n"
            b"Expect: 100-continue\r\nConnection: close\r\nContent-Length: "
            + str(len(body)).encode() + b"\r\n\r\n"
        )
        await writer.drain()
        async with asyncio.timeout(5):
            interim = await reader.readuntil(b"\r\n\r\n")
        assert interim == b"HTTP/1.1 100 Continue\r\n\r\n", interim
        # net/http emits 100 only when Decode starts reading the request body.
        replacement = await apply_mode("incubator", INCUBATOR)
        assert replacement["started_at"] != before["started_at"]
        assert not replacement["session"]["muted"]
        try:
            writer.write(body)
            await writer.drain()
            async with asyncio.timeout(5):
                response = await reader.read()
        except (ConnectionResetError, BrokenPipeError):
            response = b""
        assert not response or not 200 <= int(response.split(b" ", 2)[1]) < 300, response
        assert not (await lab.state())["session"]["muted"]
        assert json.loads((STATE / "settings.json").read_text()) == saved, "retired HTTP command changed shared settings"
        await lab.control("mute", value=True)
        assert (await lab.state())["session"]["muted"]
        assert json.loads((STATE / "settings.json").read_text())["muted"]
        await lab.control("mute", value=False)
        assert not (await lab.state())["session"]["muted"]
        assert json.loads((STATE / "settings.json").read_text()) == saved
        print("PASS admitted HTTP body read cannot outlive HA Apply or mutate retired runtime/shared settings; fresh mute works", flush=True)
    finally:
        writer.close()
        try:
            await writer.wait_closed()
        except (ConnectionResetError, BrokenPipeError):
            pass


async def main(http_teardown_only=False):
    async with aiohttp.ClientSession() as http:
        ha = await login(http)
        lab = Lab(http, ha)
        ble_ws = None
        writer = None
        original_ble = (await lab.state())["ble"]["enabled"]
        key = (STATE / "noise.psk").read_text()
        try:
            await lab.activate("assist")
            await lab.control("mute", value=False)
            await lab.until(lab.connected, "Assist subscription")
            entries = await ha.command("config_entries/get")
            entry = next(e for e in entries if e["domain"] == "esphome" and e["title"] == "Encore virtual satellite")

            async def registry():
                return [e for e in await ha.command("config/entity_registry/list") if e["config_entry_id"] == entry["entry_id"]]

            entities = await registry()
            names = ("Voice mode (restart required)", "Running voice mode", "Apply and restart", "Bluetooth proxy")
            controls = {name: next(e for e in entities if e["original_name"] == name) for name in names}
            selector, running, apply, bluetooth = (controls[n]["entity_id"] for n in names)
            assist_ids = {e["entity_id"] for e in entities if e["entity_id"].startswith("assist_satellite.")}
            assert len(assist_ids) == 1
            voice_ids = {e["entity_id"] for e in entities} - {e["entity_id"] for e in controls.values()}

            async def entity_state(entity, expected):
                async def matches():
                    state = await ha.api("GET", "/api/states/" + entity)
                    return state if state["state"] == expected else None
                return await lab.until(matches, f"{entity}={expected}", timeout=30)

            async def choose(label):
                await ha.api("POST", "/api/services/select/select_option", {"entity_id": selector, "option": label})
                await entity_state(selector, label)

            async def apply_mode(mode, label):
                before = await lab.state()
                await ha.api("POST", "/api/services/button/press", {"entity_id": apply})

                async def restarted():
                    try:
                        state = await lab.state()
                    except aiohttp.ClientError:
                        return None
                    return state if state["started_at"] != before["started_at"] and state["mode"]["running"] == mode else None

                result = await lab.until(restarted, "new runtime after HA Apply")
                assert not result["mode"]["restart_required"]
                await entity_state(running, label)
                lab.mark, lab.digest = "", hashlib.sha256()
                return result

            async def capabilities(voice):
                client = APIClient("127.0.0.1", 6053, "", noise_psk=key)
                try:
                    await client.connect(login=True)
                    info = await client.device_info()
                    listed, services = await client.list_entities_services()
                    assert info.name == "encore-lab" and info.mac_address.lower() == "02:00:00:ec:00:01"
                    assert int(info.voice_assistant_feature_flags) == voice
                    assert int(info.bluetooth_proxy_feature_flags) == 33 and not services
                    assert {e.object_id for e in listed} == {"voice_mode", "running_voice_mode", "apply_voice_mode", "bluetooth_proxy"}
                    return next(e.key for e in listed if e.object_id == "voice_mode")
                finally:
                    await client.disconnect()

            ble_ws = await http.ws_connect(HA + "/api/websocket")
            await ble_ws.receive_json()
            await ble_ws.send_json({"type": "auth", "access_token": ha.token})
            assert (await ble_ws.receive_json())["type"] == "auth_ok"
            await ble_ws.send_json({"id": 1, "type": "bluetooth/subscribe_advertisements"})
            assert (await ble_ws.receive_json())["success"]
            ads = []

            async def collect():
                async for message in ble_ws:
                    if message.type == aiohttp.WSMsgType.TEXT:
                        for adv in json.loads(message.data).get("event", {}).get("add", []):
                            if adv["address"].upper() == "C0:FF:EE:12:34:56":
                                ads.append(adv)

            collector = asyncio.create_task(collect())

            async def advertisements():
                count = len(ads)
                await lab.until(lambda: ready(len(ads) > count + 2), "changing BLE received inside HA")

            async def ble(on):
                await ha.api("POST", "/api/services/switch/turn_" + ("on" if on else "off"), {"entity_id": bluetooth})
                await entity_state(bluetooth, "on" if on else "off")
                if on:
                    await advertisements()
                else:
                    await asyncio.sleep(0.4)  # Fence a previously transmitted batch.
                    count = len(ads)
                    await asyncio.sleep(0.4)
                    assert len(ads) == count, "disabled proxy still sends advertisements"

            await ble(True)
            mode_key = await capabilities(7)
            before = await lab.state()
            await choose(INCUBATOR)
            pending = await lab.state()
            assert pending["started_at"] == before["started_at"] and pending["mode"]["running"] == "assist"
            assert pending["mode"]["configured"] == "incubator" and pending["mode"]["restart_required"]
            assert pending["session"]["connected"] and pending["ble"] == before["ble"]
            await entity_state(running, ASSIST)
            await advertisements()
            print("PASS HA selection saves only; Assist and BLE continue without reconnect", flush=True)

            await lab.control("mute", value=True)
            await advertisements()
            await lab.control("mute", value=False)
            await advertisements()
            unmuted = await lab.state()
            assert unmuted["started_at"] == before["started_at"] and unmuted["ble"] == before["ble"]
            assert unmuted["mode"] == pending["mode"]
            print("PASS Assist mute/unmute preserves pending mode and uninterrupted HA BLE scan", flush=True)

            await http_teardown(lab, apply_mode)
            if http_teardown_only:
                return  # Run this regression without the later process-restart test.
            await capabilities(0)
            await advertisements()
            assert (await lab.state())["ble"]["enabled"]
            await lab.control("start", status=409)  # No fallback when Incubator is offline.
            current = {e["entity_id"] for e in await registry()}
            assert not voice_ids & current, "inactive voice entities retained on this pinned HA baseline"

            reader, writer = await asyncio.open_connection("127.0.0.1", 6055)

            async def frame():
                async with asyncio.timeout(5):
                    head = await reader.readexactly(3)
                    return head[0], await reader.readexactly(int.from_bytes(head[1:], "big"))

            kind, payload = await frame()
            assert kind == 1 and json.loads(payload)["protocol"] == 2
            await lab.until(lab.connected, "native peer attached")
            await lab.control("start")
            kind, _ = await frame()
            assert kind == 2
            pcm = struct.pack("<320h", *([-31, 67] * 160))
            listening = json.dumps({"type": "listening"}).encode()
            writer.write(struct.pack(">BH", 0x12, len(listening)) + listening)
            writer.write(struct.pack(">BH", 0x10, len(pcm)) + pcm + struct.pack(">BH", 0x11, 0))
            await writer.drain()
            kind, _ = await frame()
            assert kind == 6
            playback = (await lab.state())["playback"]
            assert playback["played"] == 320 and playback["sha256"] == hashlib.sha256(pcm).hexdigest()
            await choose(ASSIST)
            assert (await lab.state())["session"]["backend"] == "incubator"
            await lab.control("mute", value=True)
            kind, payload = await frame()
            assert kind == 4 and json.loads(payload)["reason"] == "cancelled"
            async with asyncio.timeout(5):
                assert await reader.read() == b"", "native cancellation did not retire its voice connection"
            await apply_mode("assist", ASSIST)
            assert await reader.read() == b"", "old native connection survived restart"
            writer.close()
            await writer.wait_closed()
            writer = None
            await lab.until(lab.connected, "muted Assist subscription after switch back")
            assert (await lab.state())["session"]["muted"]
            await lab.control("start", status=409)
            await advertisements()
            await capabilities(7)
            restored = await registry()
            for old in controls.values():
                new = next(e for e in restored if e["entity_id"] == old["entity_id"])
                assert (new["id"], new["device_id"], new["unique_id"]) == (old["id"], old["device_id"], old["unique_id"])
            assert voice_ids <= {e["entity_id"] for e in restored}
            assert (STATE / "noise.psk").read_text() == key
            print("PASS both HA Apply directions: capabilities 7→0→7; stable common entities/key; Assist restored; BLE and mute preserved; native peer PCM/drain verified", flush=True)

            before = await lab.state()
            await ha.api("POST", "/api/services/button/press", {"entity_id": apply})
            await lab.control("mode", backend="not-a-mode", status=409)
            bad = APIClient("127.0.0.1", 6053, "", noise_psk=key)
            try:
                await bad.connect(login=True)
                bad.select_command(mode_key, "not-a-mode")
                await lab.until(lambda: ready(not bad.is_connected), "invalid encrypted selector rejected")
            finally:
                await bad.disconnect()
            await asyncio.sleep(0.4)
            after = await lab.state()
            assert before["started_at"] == after["started_at"] and before["mode"] == after["mode"] and before["ble"] == after["ble"]
            print("PASS same-mode Apply is a no-op; invalid HTTP/encrypted selections leave saved/running mode and BLE unchanged", flush=True)

            # Fail the atomic rename without injecting a fake persistence API.
            await lab.control("mute", value=False)
            path, backup = STATE / "settings.json", STATE / "settings-mode-test-backup.json"
            assert not backup.exists()
            saved = path.read_bytes()
            path.rename(backup)
            path.mkdir()
            failed = APIClient("127.0.0.1", 6053, "", noise_psk=key)
            try:
                await failed.connect(login=True)
                failed.select_command(mode_key, INCUBATOR)
                await lab.until(lambda: ready(not failed.is_connected), "failed save rejected")
                assert (await lab.state())["mode"] == before["mode"]
                await lab.control("mute", value=True, status=409)
                assert (await lab.state())["session"]["muted"]
                assert backup.read_bytes() == saved
            finally:
                await failed.disconnect()
                path.rmdir()
                backup.rename(path)
            await advertisements()
            assert (await lab.state())["ble"] == before["ble"]
            print("PASS failed settings save preserves prior disk/live mode and HA BLE connection", flush=True)

            # A local voice bind failure must not take away the recovery selector.
            blocked = await asyncio.start_server(lambda r, w: w.close(), "127.0.0.1", 6055)
            try:
                await choose(INCUBATOR)
                unavailable = await apply_mode("incubator", INCUBATOR)
                assert unavailable["mode"]["backend_error"] and not unavailable["session"]["connected"]
                assert unavailable["session"]["muted"] and json.loads(path.read_text())["muted"]
                await lab.control("start", status=409)
                await capabilities(0)
                await advertisements()
                await choose(ASSIST)
                await apply_mode("assist", ASSIST)
            finally:
                blocked.close()
                await blocked.wait_closed()
            print("PASS failed mute save → recovered storage → HA Apply retains/persists mute; failed native startup keeps BLE/management reachable without fallback", flush=True)

            await choose(INCUBATOR)
            process = await asyncio.create_subprocess_exec("amp", "orb", "service", "restart", "encore")
            assert await process.wait() == 0
            await entity_state(running, INCUBATOR)
            assert (await lab.state())["mode"]["running"] == "incubator" and (await lab.state())["session"]["muted"]
            await advertisements()
            await ble(False)
            await choose(ASSIST)
            await apply_mode("assist", ASSIST)
            assert not (await lab.state())["ble"]["enabled"] and (await lab.state())["ble"]["starts"] == 0
            print("PASS actual process restart loads pending mode/mute/BLE; saved BLE OFF stays OFF across switch back", flush=True)

            await lab.control("mute", value=False)
            await lab.listen()
            await lab.success("real Assist turn after restart-bound mode round trips")
            await lab.switch()
            await lab.success("real Assist turn after lab selector interrupts held playback")
        finally:
            if writer is not None:
                writer.close()
                await writer.wait_closed()
            await lab.activate("assist")
            await lab.control("mute", value=False)
            if "bluetooth" in locals():
                await entity_state(bluetooth, "off" if not (await lab.state())["ble"]["enabled"] else "on")
                await ble(original_ble)
            if ble_ws is not None:
                await ble_ws.close()
                if "collector" in locals():
                    await collector
            if hasattr(lab, "events_ws"):
                await lab.events_ws.close()
                await lab.collector
            await ha.ws.close()
        print("PASS real HA restart-bound mode selector and independent BLE suite", flush=True)


async def ready(value):
    return value


if __name__ == "__main__":
    asyncio.run(main(http_teardown_only="--http-teardown-only" in sys.argv))
