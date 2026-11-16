"""Actual pinned HA ESPHome/Bluetooth integration against the virtual radio.

Run after building the fixture, with lab/.venv/bin/python. HA owns its real
config flow, switch entity, scanner, allocations and Bleak routing. Only the
radio/GATT peer is virtual; this is not RF, BlueZ or bonding qualification.
"""
import asyncio
import base64
import json
import os
from pathlib import Path
import socket
import sys
import tempfile

import aiohttp
from aioesphomeapi import APIClient
from aioesphomeapi.api_pb2 import BluetoothDeviceConnectionResponse
from aioesphomeapi.core import BluetoothGATTAPIError
from habluetooth import get_manager
from homeassistant import loader
from homeassistant.bootstrap import async_from_config_dict
from homeassistant.core import HomeAssistant

from test_bluetooth import radio_state, until


async def main():
    key = base64.b64encode(os.urandom(32)).decode()
    with tempfile.TemporaryDirectory(dir=".amp/in/orb-backup") as directory:
        state = Path(directory) / "enabled"
        proc = await asyncio.create_subprocess_exec(
            sys.argv[1], stdout=asyncio.subprocess.PIPE,
            env={**os.environ, "BLE_TEST_PSK": key, "BLE_TEST_STATE": str(state)},
        )
        endpoints = json.loads(await asyncio.wait_for(proc.stdout.readline(), 10))
        host, port = endpoints["api"].split(":")
        control = endpoints["control"]
        client = APIClient(host, int(port), "", noise_psk=key)
        hass = HomeAssistant(directory)
        hass.config.skip_pip = True
        loader.async_setup(hass)
        # HA rejects port 0 in config. Pick a disposable loopback listener;
        # this test never uses or modifies the persistent lab HA instance.
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            http_port = sock.getsockname()[1]
        try:
            assert await async_from_config_dict({
                "bluetooth": {},
                "http": {"server_host": "127.0.0.1", "server_port": http_port},
            }, hass) is hass
            await hass.async_start()
            flow = await hass.config_entries.flow.async_init(
                "esphome", context={"source": "user"}, data={"host": host, "port": int(port)},
            )
            assert flow["type"] == "form" and flow["step_id"] == "encryption_key"
            flow = await hass.config_entries.flow.async_configure(flow["flow_id"], {"noise_psk": key})
            assert flow["type"] == "create_entry", flow["type"]
            entry = flow["result"]
            entity = "switch.encore_ble_fixture_bluetooth_proxy"
            await until(lambda: hass.states.get(entity) and hass.states.get(entity).state == "off")
            switches = [s.entity_id for s in hass.states.async_all("switch") if s.entity_id.startswith("switch.encore_ble_fixture")]
            assert switches == [entity], switches
            data = entry.runtime_data
            assert data.device_info.bluetooth_proxy_feature_flags == 47
            assert data.device_info.voice_assistant_feature_flags == 0
            async with aiohttp.ClientSession() as http:
                await radio_state(http, control, scans=0, opens=0, closes=0, blocked=0)
                # This independent encrypted client controls only the switch.
                # It must update HA's entity, without taking HA's GATT lease.
                await client.connect(login=True)
                entities, _ = await client.list_entities_services()
                states = []
                client.subscribe_states(states.append)
                await until(lambda: states)
                client.switch_command(entities[0].key, True)
                await until(lambda: hass.states.get(entity).state == "on")
                await until(lambda: data.bluetooth_device.ble_connections_free == 3)
                assert state.read_text() == "true"
                manager = get_manager()
                await until(lambda: manager.async_discovered_devices(True))
                device = manager.async_discovered_devices(True)[0]
                assert device.address == "C0:FF:EE:12:34:56"
                assert device.details["address_type"] == 1
                service_info = manager.async_last_service_info(device.address, True)
                assert service_info.rssi == -73 and service_info.manufacturer_data == {0x1234: b""}
                # Import after HA installs its real multi-source Bleak router.
                from bleak import BleakClient

                async with BleakClient(device) as peer:
                    assert peer.is_connected and peer.mtu_size == 77
                    assert peer.services.get_service("11223344-5566-7788-99aa-bbccddeeff00").handle == 0x10
                    assert await peer.read_gatt_char(0x15) == b"\x19\x82\xe3"
                    assert await peer.read_gatt_descriptor(0x17) == b"descriptor"
                    await peer.write_gatt_descriptor(0x17, b"ha-descriptor")
                    assert await peer.read_gatt_descriptor(0x17) == b"ha-descriptor"
                    notifications = []
                    await peer.start_notify(0x15, lambda ch, value: notifications.append((ch.handle, bytes(value))))
                    await peer.write_gatt_char(0x15, b"\x86\x31", response=True)
                    await until(lambda: notifications)
                    assert notifications[-1] == (0x15, b"\x86\x31")
                    await peer.stop_notify(0x15)
                    count = len(notifications)
                    await peer.write_gatt_char(0x15, b"command", response=False)
                    assert await peer.read_gatt_char(0x15) == b"command"
                    assert len(notifications) == count
                    await peer.pair()
                    # A second client cannot operate on HA's active link.
                    try:
                        await client.bluetooth_gatt_read(0xc0ffee123456, 0x15)
                        raise AssertionError("non-owner read succeeded")
                    except BluetoothGATTAPIError:
                        pass
                    rejected = []
                    remove = client._get_connection().add_message_callback(rejected.append, (BluetoothDeviceConnectionResponse,))
                    try:
                        client.bluetooth_device_disconnect_no_wait(0xc0ffee123456)
                        await until(lambda: rejected)
                        assert (rejected[0].address, rejected[0].connected, rejected[0].error) == (0xc0ffee123456, True, 128)
                        assert peer.is_connected
                        assert await peer.read_gatt_char(0x15) == b"command"
                    finally:
                        remove()
                    refused_slots = []
                    client.subscribe_bluetooth_connections_free(lambda *s: refused_slots.append(s))
                    await until(lambda: refused_slots)
                    assert refused_slots[-1][0] == 0
                    for handle in (0, 65536):
                        try:
                            await data.client.bluetooth_gatt_read(0xc0ffee123456, handle)
                            raise AssertionError("invalid ATT handle accepted")
                        except BluetoothGATTAPIError:
                            pass
                    await peer.write_gatt_char(0x15, b"a" * 512, response=True)
                    assert await peer.read_gatt_char(0x15) == b"a" * 512
                    try:
                        await data.client.bluetooth_gatt_write(0xc0ffee123456, 0x15, b"b" * 513, True)
                        raise AssertionError("oversized ATT write accepted")
                    except BluetoothGATTAPIError:
                        pass
                    assert await peer.read_gatt_char(0x15) == b"a" * 512
                await until(lambda: data.bluetooth_device.ble_connections_free == 3)
                async with http.get(control + "/gatt-stats") as response:
                    before = await response.json()
                # Real bleak-esphome must use cached services on reconnect,
                # while reads/notifications still resolve the cached handles.
                await peer.connect(dangerous_use_bleak_cache=True)
                try:
                    assert await peer.read_gatt_char(0x15) == b"a" * 512
                    async with http.get(control + "/gatt-stats") as response:
                        after = await response.json()
                    assert after["services"] == before["services"], (before, after)
                    assert after["connects"] == before["connects"] + 1, (before, after)
                    await peer.unpair()
                    await until(lambda: not peer.is_connected)
                finally:
                    await peer.disconnect()
                await until(lambda: data.bluetooth_device.ble_connections_free == 3)
                print("PASS real HA config flow/Noise/single default-OFF switch; cross-client switch ownership/state; HA raw discovery and Bleak routing; read/write/descriptor/CCCD/notify/pair/unpair; cached reconnect; ownership/handle/512-byte boundaries")

                await peer.connect(dangerous_use_bleak_cache=True)
                await http.get(control + "/pause?on=true")
                await until(lambda: not peer.is_connected and data.bluetooth_device.ble_connections_free == 0)
                assert hass.states.get(entity).state == "on" and state.read_text() == "true"
                await http.get(control + "/pause?on=false")
                await until(lambda: data.bluetooth_device.ble_connections_free == 3)
                await peer.connect(dangerous_use_bleak_cache=True)
                client.switch_command(entities[0].key, False)
                await until(lambda: not peer.is_connected and hass.states.get(entity).state == "off")
                assert state.read_text() == "false"
                await peer.disconnect()
                await hass.services.async_call("switch", "turn_on", {"entity_id": entity}, blocking=True)
                await until(lambda: states[-1].state and data.bluetooth_device.ble_connections_free == 3)
                # Failed persistence must not publish a false OFF to HA or
                # cancel its existing radio lease.
                await http.get(control + "/fail-save?on=true")
                snapshot = await radio_state(http, control, blocked=0)
                client.switch_command(entities[0].key, False)
                await until(lambda: not client.is_connected)
                assert hass.states.get(entity).state == "on" and state.read_text() == "true"
                await radio_state(http, control, opens=snapshot["opens"], closes=snapshot["closes"])
                await http.get(control + "/fail-save?on=false")
                # Unload/reload HA's real entry tears down one lease, then
                # reconnects the same entity and scanner to the saved ON state.
                assert await hass.config_entries.async_reload(entry.entry_id)
                await until(lambda: entry.runtime_data.available and entry.runtime_data.bluetooth_device and entry.runtime_data.bluetooth_device.ble_connections_free == 3)
                await until(lambda: hass.states.get(entity).state == "on")
                snapshot = await radio_state(http, control, blocked=0)
                assert snapshot["opens"] == snapshot["closes"] + 1, snapshot
                await hass.services.async_call("switch", "turn_off", {"entity_id": entity}, blocking=True)
                await until(lambda: hass.states.get(entity).state == "off")
                await radio_state(http, control, closes=snapshot["opens"])
                print("PASS HA pause/resume and OFF disconnect; HA switch service; failed-save isolation; real config-entry unload/reconnect; all central leases closed")
        finally:
            await client.disconnect()
            await hass.async_stop(force=True)
            proc.terminate()
            await asyncio.wait_for(proc.wait(), 10)
            assert proc.returncode == 0, proc.returncode


if __name__ == "__main__":
    asyncio.run(main())
