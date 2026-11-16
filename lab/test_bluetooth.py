"""HA's pinned aioesphomeapi 46.30.0 against a virtual BLE radio over real Noise.

Run: go build -o .amp/in/orb-backup/ble-fixture ./lab/bluetooth/satellite
     lab/.venv/bin/python lab/test_bluetooth.py .amp/in/orb-backup/ble-fixture
No production HA, RF, real GATT/bonding or Dot hardware is exercised.
"""
import asyncio
import base64
import json
import os
from pathlib import Path
import sys
import tempfile

import aiohttp
from aioesphomeapi import APIClient, InvalidEncryptionKeyAPIError
from aioesphomeapi.core import BluetoothGATTAPIError


async def until(predicate):
    async with asyncio.timeout(5):
        while not predicate():
            await asyncio.sleep(0.01)


async def radio_state(http, control, **expected):
    async with asyncio.timeout(5):
        while True:
            async with http.get(control+"/state") as response:
                state = await response.json()
            if all(state[k] == v for k,v in expected.items()):
                return state
            await asyncio.sleep(0.01)


async def main():
    key = base64.b64encode(os.urandom(32)).decode()
    with tempfile.TemporaryDirectory(dir=".amp/in/orb-backup") as directory:
        state = Path(directory) / "enabled"

        async def start():
            proc = await asyncio.create_subprocess_exec(sys.argv[1], stdout=asyncio.subprocess.PIPE,
                env={**os.environ, "BLE_TEST_PSK": key, "BLE_TEST_STATE": str(state)})
            endpoints = json.loads(await asyncio.wait_for(proc.stdout.readline(), 10))
            host, port = endpoints["api"].split(":")
            return proc, APIClient(host, int(port), "", noise_psk=key), endpoints["control"]

        proc, client, control = await start()
        try:
            host,port = client.address,client.port
            bad = APIClient(host,port,"",noise_psk=base64.b64encode(os.urandom(32)).decode())
            try:
                await bad.connect(login=True)
                raise AssertionError("wrong Noise key accepted")
            except InvalidEncryptionKeyAPIError:
                pass
            finally:
                await bad.disconnect()
            await client.connect(login=True)
            info = await client.device_info()
            assert info.bluetooth_proxy_feature_flags == 47, info  # Scan, active, cache, pair, raw; NOT clear/mode/params.
            entities,_ = await client.list_entities_services()
            assert len(entities)==1 and entities[0].object_id=="bluetooth_proxy",entities
            switch = entities[0].key
            states,slots,ads,connections,notifications = [],[],[],[],[]
            client.subscribe_states(states.append)
            client.subscribe_bluetooth_connections_free(lambda free,limit,allocated: slots.append((free,limit,allocated)))
            client.subscribe_bluetooth_le_raw_advertisements(ads.append)
            await until(lambda: states and slots)
            assert states[-1].state is False and slots[-1][0]==0
            async with aiohttp.ClientSession() as http:
                async with http.get(control+"/state") as response:
                    assert await response.json()=={"scans":0,"opens":0,"closes":0,"blocked":0}
                client.switch_command(switch,True)
                await until(lambda: states[-1].state and slots[-1][0]==3 and ads)
                assert state.read_text()=="true"
                ad = ads[-1].advertisements[0]
                assert (ad.address,ad.address_type,ad.rssi,bytes(ad.data))==(0xc0ffee123456,1,-73,b"\x02\x01\x06\x03\xff\x34\x12"),ad
                address = ad.address
                cancel_connect = await client.bluetooth_device_connect(address,lambda *args: connections.append(args),feature_flags=info.bluetooth_proxy_feature_flags,address_type=1)
                assert connections[-1]==(True,77,0),connections
                await until(lambda: slots[-1][0]==2)
                services = await client.bluetooth_gatt_get_services(address)
                svc = services.services[0]
                assert svc.uuid=="11223344-5566-7788-99aa-bbccddeeff00" and svc.handle==0x10,svc
                ch = svc.characteristics[0]
                assert ch.handle==0x15 and ch.properties==26 and ch.descriptors[0].handle==0x17,ch
                assert await client.bluetooth_gatt_read(address,0x15)==b"\x19\x82\xe3"
                assert await client.bluetooth_gatt_read_descriptor(address,0x17)==b"descriptor"
                await client.bluetooth_gatt_write_descriptor(address,0x17,b"changed")
                assert await client.bluetooth_gatt_read_descriptor(address,0x17)==b"changed"
                stop_notify,_ = await client.bluetooth_gatt_start_notify(address,0x15,lambda h,d: notifications.append((h,bytes(d))))
                await client.bluetooth_gatt_write(address,0x15,b"\x93\x02",True)
                await until(lambda: notifications)
                assert notifications[-1]==(0x15,b"\x93\x02")
                assert (await client.bluetooth_device_pair(address)).paired
                try:
                    await client.bluetooth_gatt_read(address,0x66)
                    raise AssertionError("failed GATT read succeeded")
                except BluetoothGATTAPIError:
                    pass
                await stop_notify()
                assert (await client.bluetooth_device_unpair(address)).success
                await until(lambda: slots[-1][0]==3)
                cancel_connect()
                callbacks=[]
                for peer in (address,address+1,address+2):
                    callbacks.append(await client.bluetooth_device_connect(peer,lambda *args: None,feature_flags=47,address_type=1))
                await until(lambda: slots[-1][0]==0)
                refused=[]
                unsub=await client.bluetooth_device_connect(address+3,lambda *args: refused.append(args),feature_flags=47,address_type=1)
                assert refused==[(False,0,128)],refused
                unsub()
                for peer,unsub in zip((address,address+1,address+2),callbacks):
                    await client.bluetooth_device_disconnect(peer)
                    unsub()
                await until(lambda: slots[-1][0]==3)
                blocked=asyncio.create_task(client.bluetooth_device_connect(0xaabbccddeeff,lambda *args: None,feature_flags=47,address_type=1))
                await radio_state(http,control,blocked=1)
                blocked.cancel()
                try:
                    await blocked
                except asyncio.CancelledError:
                    pass
                await radio_state(http,control,blocked=0)
                await http.get(control+"/pause?on=true")
                await until(lambda: slots[-1][0]==0)
                await http.get(control+"/pause?on=false")
                await until(lambda: slots[-1][0]==3)
                client.switch_command(switch,False)
                await until(lambda: states[-1].state is False and slots[-1][0]==0)
                assert state.read_text()=="false"
                count=len(ads)
                await asyncio.sleep(0.4)
                assert len(ads)==count,"advertisements continued while OFF"
                client.switch_command(switch,True)
                await until(lambda: states[-1].state and slots[-1][0]==3)
                before=len(states)
                for _ in range(10):
                    client.switch_command(switch,False)
                    client.switch_command(switch,True)
                await until(lambda: len(states)>=before+20 and states[-1].state and slots[-1][0]==3)
                await asyncio.sleep(0.1)
                assert slots[-1][0]==3,"rapid toggle lost the fresh connection lease"
            print("PASS Noise rejection; default OFF/no radio; one persistent switch; exact raw ads; GATT UUID/handles/read/write/descriptor/notify/error; pair/unpair; 3-slot bound; cancelled connect; pause/resume/OFF slots")
        finally:
            await client.disconnect()
            proc.terminate()
            await asyncio.wait_for(proc.wait(),10)
        proc,client,control = await start()
        try:
            await client.connect(login=True)
            states=[]
            client.subscribe_states(states.append)
            await until(lambda: states)
            assert states[-1].state is True,"saved proxy enable did not survive process restart"
            print("PASS persistent enable survives process restart")
            async with aiohttp.ClientSession() as http:
                await http.get(control+"/fail-save?on=true")
                client.switch_command(switch,False)
                await until(lambda: not client.is_connected)
                assert state.read_text()=="true","failed save changed persisted state"
                await http.get(control+"/fail-save?on=false")
                await client.connect(login=True)
                states=[]
                client.subscribe_states(states.append)
                await until(lambda: states)
                assert states[-1].state is True,"failed save changed live state"
            print("PASS failed persistence changes neither saved nor live switch state")
        finally:
            await client.disconnect()
            proc.terminate()
            await asyncio.wait_for(proc.wait(),10)


asyncio.run(main())
