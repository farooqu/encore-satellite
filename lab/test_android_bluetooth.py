"""Real Noise + pinned HA Bluetooth client; native Android callbacks simulated.

go build -race -o .amp/in/orb-backup/android-ble-fixture ./lab/androidbluetooth
lab/.venv/bin/python lab/test_android_bluetooth.py .amp/in/orb-backup/android-ble-fixture
The mobile runtime listens on 6053/6055; those ports must be unused locally.
"""
import asyncio
import json
import sys

import aiohttp
from aioesphomeapi import APIClient, InvalidEncryptionKeyAPIError
from bleak_esphome.connect import connect_scanner
from habluetooth import BluetoothManager, set_manager


async def until(predicate):
    async with asyncio.timeout(5):
        while not predicate():
            await asyncio.sleep(0.01)


async def scenario(binary, sdk, hardware=True):
    proc = await asyncio.create_subprocess_exec(binary, f"-sdk={sdk}", f"-hardware={str(hardware).lower()}",
        stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE)
    client = None
    cleanup = None
    try:
        meta = json.loads(await asyncio.wait_for(proc.stdout.readline(), 10))
        wrong = APIClient("127.0.0.1", meta["port"], "", noise_psk="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
        try:
            await wrong.connect(login=True)
            raise AssertionError("Wrong encryption key accepted")
        except InvalidEncryptionKeyAPIError:
            pass
        finally:
            await wrong.disconnect()
        client = APIClient("127.0.0.1", meta["port"], "", noise_psk=meta["key"])
        await client.connect(login=True)
        info = await client.device_info()
        assert info.bluetooth_proxy_feature_flags == (33 if hardware else 0), info
        entities, _ = await client.list_entities_services()
        proxy, = [e for e in entities if e.object_id == "bluetooth_proxy"]
        entity_ids = {e.object_id for e in entities}
        assert {"dashboard_url", "update_channel"} <= entity_ids, entities
        assert "apk_update" not in entity_ids, entities  # The fixture has no updater.
        states = {}
        client.subscribe_states(lambda state: states.update({state.key: state}))
        await until(lambda: proxy.key in states)
        assert not states[proxy.key].state
        slots = []
        client.subscribe_bluetooth_connections_free(lambda *args: slots.append(args))
        await until(lambda: slots)
        assert slots[-1][0] == 0
        manager = BluetoothManager()
        set_manager(manager)
        client_data = connect_scanner(client, info, True) if hardware else None
        scanner = client_data.scanner if client_data else None
        if scanner:
            assert not scanner.connectable
            cleanup = scanner.async_setup()
        async with aiohttp.ClientSession() as http:
            async def get(path):
                async with http.get(meta["control"] + path) as response:
                    response.raise_for_status()
                    if path == "/state":
                        return await response.json()
            assert not (await get("/state"))["scanning"]
            client.switch_command(proxy.key, True)
            await until(lambda: states[proxy.key].state)
            # Permission/radio/activity gate remains closed despite saved ON.
            assert not (await get("/state"))["scanning"]
            await get("/pause?value=false")
            if not hardware:
                await asyncio.sleep(0.3)
                assert (await get("/state"))["starts"] == 0
                print(f"PASS API {sdk} BLE=False: no scan capability, zero slots, saved control only")
                return
            async def scanning(expected):
                async with asyncio.timeout(5):
                    while (await get("/state"))["scanning"] != expected:
                        await asyncio.sleep(0.01)
            await scanning(True)
            await get("/report?stale=true")
            await asyncio.sleep(0.3)
            assert not scanner.discovered_devices
            await get("/report")
            await until(lambda: scanner.discovered_devices)
            device, adv = scanner.discovered_devices_and_advertisement_data["C0:FE:19:82:E3:45"]
            assert device.details["address_type"] == (1 if sdk >= 35 else 255), device.details
            assert adv.local_name == "A7L" and adv.rssi == -73
            assert adv.manufacturer_data == {0x1234: b"\x82\xe3"}, adv
            # Local UI and encrypted switch use one state/persistence owner.
            await get("/enable?value=false")
            await until(lambda: not states[proxy.key].state)
            await scanning(False)
            client.switch_command(proxy.key, True)
            await scanning(True)
            await get("/pause?value=true")
            await scanning(False)
            assert (await get("/state"))["enabled"]
            assert (await client.device_info()).bluetooth_proxy_feature_flags == 33
            await get("/pause?value=false")
            await scanning(True)
            await client.disconnect()
            await scanning(False)
            await client.connect(login=True)
            info2 = await client.device_info()
            assert info2.bluetooth_proxy_feature_flags == 33
            recovered = []
            client.subscribe_bluetooth_le_raw_advertisements(lambda response: recovered.extend(response.advertisements))
            await scanning(True)
            await get("/report")
            await until(lambda: recovered)
            assert recovered[0].data == bytes([2, 1, 6, 4, 9, 65, 55, 76, 5, 255, 52, 18, 130, 227])
        print(f"PASS API {sdk} BLE={hardware}: Noise, capabilities, single switch, raw parsing, unknown type, lifecycle")
    finally:
        if cleanup:
            cleanup()
        if client:
            await client.disconnect()
        proc.stdin.close()
        await asyncio.wait_for(proc.wait(), 10)


async def main():
    for sdk, hardware in ((26, True), (29, True), (34, True), (35, True), (34, False)):
        await scenario(sys.argv[1], sdk, hardware)


asyncio.run(main())
