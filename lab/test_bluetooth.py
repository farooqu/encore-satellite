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
from aioesphomeapi.api_pb2 import BluetoothDeviceConnectionResponse
from aioesphomeapi.core import BluetoothConnectionDroppedError, BluetoothGATTAPIError


async def until(predicate):
    async with asyncio.timeout(5):
        while not predicate():
            await asyncio.sleep(0.01)


async def radio_state(http, control, path="/state", **expected):
    async with asyncio.timeout(5):
        while True:
            async with http.get(control+path) as response:
                state = await response.json()
            if all(state[k] == v for k,v in expected.items()):
                return state
            await asyncio.sleep(0.01)


async def generation_handoffs(start, key):
    failures = []
    for mode in ("off-on", "new-client", "overflow", "close-error"):
        proc, client, control = await start()
        second, tasks, subscriptions = None, [], []
        try:
            await client.connect(login=True)
            entities, _ = await client.list_entities_services()
            slots = []
            client.subscribe_bluetooth_connections_free(lambda free, limit, allocated: slots.append((free, limit, allocated)))
            await until(lambda: slots and slots[-1][0] == 3)
            address = 0xc0ffee123456
            subscriptions.append(await client.bluetooth_device_connect(address, lambda *args: None, feature_flags=47, address_type=1))
            async with aiohttp.ClientSession() as http:
                if mode == "off-on":
                    tasks.append(asyncio.create_task(client.bluetooth_device_connect(0xaabbccddeeff, lambda *args: None, feature_flags=47, address_type=1)))
                    await radio_state(http, control, blocked=1)
                    tasks.extend(asyncio.create_task(client.bluetooth_gatt_read(address, 0x200+i)) for i in range(31))
                    await asyncio.sleep(0)
                    # Fill entry 32 with an old slot refresh, leaving no room
                    # for ON's successor refresh to conceal a stale overwrite.
                    client.switch_command(entities[0].key, True)
                    await client.list_entities_services()
                    async with http.get(control+"/hold-close?on=true"):
                        pass
                    client.switch_command(entities[0].key, False)
                    await radio_state(http, control, path="/links", closing=True)
                    rejected = []
                    remove = client._get_connection().add_message_callback(rejected.append, (BluetoothDeviceConnectionResponse,))
                    try:
                        client.bluetooth_device_disconnect_no_wait(address)
                        await until(lambda: rejected)
                        assert rejected[-1].connected and rejected[-1].error == 128, rejected
                    finally:
                        remove()
                    client.switch_command(entities[0].key, True)
                    tasks.append(asyncio.create_task(client.bluetooth_device_disconnect(address, timeout=3)))
                    await asyncio.sleep(0)
                    await client.list_entities_services()
                    slot_cutoff = len(slots)
                    async with http.get(control+"/hold-close?on=false"):
                        pass
                    results = await asyncio.gather(*tasks, return_exceptions=True)
                    assert all(isinstance(result, Exception) for result in results[1:32]), results
                    # Old Close, old refresh, and successor cleanup each
                    # publish once. The successor must be the final state.
                    await until(lambda: len(slots) >= slot_cutoff+3)
                    assert slots[-1][:2] == (3, 3), slots[slot_cutoff:]
                    subscriptions.append(await client.bluetooth_device_connect(address, lambda *args: None, feature_flags=47, address_type=1))
                    assert await client.bluetooth_gatt_read(address, 0x15) == b"\x19\x82\xe3"
                    await client.bluetooth_device_disconnect(address)
                    await until(lambda: slots[-1][0] == 3)
                elif mode == "new-client":
                    async with http.get(control+"/hold-disconnect?on=true&delay-cancel=true"):
                        pass
                    tasks.append(asyncio.create_task(client.bluetooth_device_disconnect(address, timeout=3)))
                    await radio_state(http, control, path="/links", disconnecting=True)
                    await client.disconnect()
                    await radio_state(http, control, path="/links", disconnect_cancelled=True)
                    second = APIClient(client.address, client.port, "", noise_psk=key)
                    await second.connect(login=True)
                    tasks.append(asyncio.create_task(second.bluetooth_device_disconnect(address, timeout=3)))
                    await asyncio.sleep(0)
                    await second.list_entities_services()
                    async with http.get(control+"/hold-disconnect?on=false"):
                        pass
                    await tasks[-1]
                elif mode == "close-error":
                    replies = []
                    remove = client._get_connection().add_message_callback(replies.append, (BluetoothDeviceConnectionResponse,))
                    try:
                        for endpoint in ("/fail-close?on=true", "/hold-close?on=true"):
                            async with http.get(control+endpoint):
                                pass
                        client.switch_command(entities[0].key, False)
                        await radio_state(http, control, path="/links", closing=True)
                        tasks.append(asyncio.create_task(client.bluetooth_device_disconnect(address, timeout=1)))
                        await asyncio.sleep(0)
                        await client.device_info()
                        async with http.get(control+"/hold-close?on=false"):
                            pass
                        result = await asyncio.gather(tasks[-1], return_exceptions=True)
                        assert isinstance(result[0], Exception), ("failed Close falsely acknowledged disconnect", replies)
                        await until(lambda: not client.is_connected)
                        assert replies and all(reply.connected for reply in replies), replies
                    finally:
                        remove()
                    await radio_state(http, control, opens=1, closes=1)
                    await radio_state(http, control, path="/links", active={"C0:FF:EE:12:34:56": True})
                    await client.connect(login=True)
                    states = []
                    client.subscribe_states(states.append)
                    client.subscribe_bluetooth_connections_free(lambda *s: slots.append(s))
                    client.switch_command(entities[0].key, True)
                    await until(lambda: states and states[-1].state)
                    refused = []
                    subscriptions.append(await client.bluetooth_device_connect(address, lambda *args: refused.append(args), feature_flags=47, address_type=1))
                    assert refused == [(False, 0, 128)] and slots[-1][0] == 0, (refused, slots)
                    await radio_state(http, control, opens=1, closes=1)
                else:
                    async with http.get(control+"/hold-disconnect?on=true"):
                        pass
                    tasks.append(asyncio.create_task(client.bluetooth_device_disconnect(address, timeout=3)))
                    await radio_state(http, control, path="/links", disconnecting=True)
                    for i in range(36):
                        client.bluetooth_device_disconnect_no_wait(address+10+i)
                    await client.list_entities_services()  # 36 distinct intents fit.
                    client.bluetooth_device_disconnect_no_wait(address+46)
                    await until(lambda: not client.is_connected)
                    await radio_state(http, control, path="/links", active={})
                    async with http.get(control+"/hold-disconnect?on=false"):
                        pass
                    await client.connect(login=True)
                    client.subscribe_bluetooth_connections_free(lambda free, limit, allocated: slots.append((free, limit, allocated)))
                    await until(lambda: slots[-1][0] == 3)
                print(f"PASS disconnect generation handoff: {mode}")
        except Exception as err:
            failures.append(f"{mode}: {type(err).__name__}: {err}")
        finally:
            async with aiohttp.ClientSession() as http:
                for endpoint in ("/hold-close?on=false", "/hold-disconnect?on=false"):
                    async with http.get(control+endpoint):
                        pass
            for task in tasks:
                task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
            for unsubscribe in subscriptions:
                unsubscribe()
            if second is not None:
                await second.disconnect()
            await client.disconnect()
            proc.terminate()
            await asyncio.wait_for(proc.wait(), 10)
    assert not failures, failures


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
                await asyncio.wait_for(client.bluetooth_device_disconnect(0xc0ffee123456), 1)
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
                assert ch.handle==0x15 and ch.properties==30 and ch.descriptors[0].handle==0x17,ch
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
                async with http.get(control+"/gatt-stats") as response:
                    before = await response.json()
                queued=asyncio.create_task(client.bluetooth_device_connect(0xaabbccddeefe,lambda *args: None,feature_flags=47,address_type=1))
                await asyncio.sleep(0)  # Let the second connect enter the protocol queue.
                await client.device_info()  # Reader barrier; GATT's worker is still blocked.
                queued.cancel()
                await asyncio.gather(queued, return_exceptions=True)
                await client.device_info()  # Its disconnect must arrive before cancelling the first.
                blocked.cancel()
                try:
                    await blocked
                except asyncio.CancelledError:
                    pass
                await radio_state(http,control,blocked=0)
                # A cancelled queued connect must never touch the controller
                # or block the next healthy connection behind its timeout.
                async with asyncio.timeout(5):
                    healthy=await client.bluetooth_device_connect(address,lambda *args: None,feature_flags=47,address_type=1)
                async with http.get(control+"/gatt-stats") as response:
                    after = await response.json()
                assert after["connects"] == before["connects"] + 1, (before,after)
                await client.bluetooth_device_disconnect(address)
                healthy()
                # A disconnect acknowledgement is a fence for old replies,
                # not just controller activity. Keep another address blocked
                # after it so a retry can overtake the cancelled callback.
                blocked=asyncio.create_task(client.bluetooth_device_connect(0xaabbccddeeff,lambda *args: None,feature_flags=47,address_type=1))
                await radio_state(http,control,blocked=1)
                middle=asyncio.create_task(client.bluetooth_device_connect(0xaabbccddeefe,lambda *args: None,feature_flags=47,address_type=1))
                abandoned=asyncio.create_task(client.bluetooth_device_connect(address,lambda *args: None,feature_flags=47,address_type=1))
                await asyncio.sleep(0)
                await client.device_info()
                # One disconnect cancels A and preempts B. Do not cancel the
                # client tasks too: their implicit disconnects would preempt C.
                await asyncio.wait_for(client.bluetooth_device_disconnect(address),5)
                (await abandoned)()
                (await blocked)()
                await radio_state(http,control,blocked=1)  # Now the middle connect, not the first.
                callbacks=[]
                retry=asyncio.create_task(client.bluetooth_device_connect(address,lambda *args: callbacks.append(args),feature_flags=47,address_type=1))
                await asyncio.sleep(0)
                await client.device_info()
                middle.cancel()
                await asyncio.gather(middle,return_exceptions=True)
                healthy=await asyncio.wait_for(retry,5)
                print(f"Same-address retry callbacks: {callbacks}")
                assert callbacks==[(True,77,0)],"an old cancelled request poisoned the fresh connection"
                assert await client.bluetooth_gatt_read(address,0x15)==b"\x93\x02"
                await client.bluetooth_device_disconnect(address)
                healthy()
                await until(lambda: slots[-1][0]==3)
                print("PASS same-address retry receives no cancelled pre-disconnect replies")
                # Keep two links while a third address blocks the worker.
                # Disconnecting one established link must not be lost to a
                # full ordinary queue, or tear down the unrelated link.
                healthy=await client.bluetooth_device_connect(address,lambda *args: None,feature_flags=47,address_type=1)
                other=await client.bluetooth_device_connect(address+1,lambda *args: None,feature_flags=47,address_type=1)
                blocked=asyncio.create_task(client.bluetooth_device_connect(0xaabbccddeeff,lambda *args: None,feature_flags=47,address_type=1))
                await radio_state(http,control,blocked=1)
                reads=[asyncio.create_task(client.bluetooth_gatt_read(address,handle)) for handle in range(1,81)]
                await until(lambda: sum(task.done() for task in reads)==48)
                await http.get(control+"/hold-disconnect?on=true")
                disconnects=[asyncio.create_task(client.bluetooth_device_disconnect(address)) for _ in range(64)]
                try:
                    await asyncio.sleep(0)
                    await client.device_info()  # Every disconnect reached the reader.
                    async with asyncio.timeout(5):
                        while True:
                            async with http.get(control+"/links") as response:
                                links=await response.json()
                            if links["disconnecting"] or any(task.done() for task in disconnects):
                                break
                            await asyncio.sleep(0.01)
                    early_ack=any(task.done() for task in disconnects)
                    assert not early_ack,"disconnect acknowledged before controller cleanup"
                    assert links["active"]=={"C0:FF:EE:12:34:56":True,"C0:FF:EE:12:34:57":True},links
                    async with http.get(control+"/hold-disconnect") as response:
                        snapshot=await response.json()
                    assert not snapshot["cancelled"],"duplicate disconnect cancelled controller cleanup"
                    # A disconnect must preempt the unrelated blocked connect,
                    # not wait for its timeout or a test-issued cancellation.
                    (await asyncio.wait_for(blocked,5))()
                    await radio_state(http,control,blocked=0)
                finally:
                    await http.get(control+"/hold-disconnect?on=false")
                async with asyncio.timeout(5):
                    await asyncio.gather(*disconnects)
                    results=await asyncio.gather(*reads,return_exceptions=True)
                assert all(isinstance(result,(BluetoothGATTAPIError,BluetoothConnectionDroppedError)) for result in results),results
                async with http.get(control+"/gatt-stats") as response:
                    snapshot=await response.json()
                print(f"Saturation disconnect: early_ack={early_ack}, controller_connected={snapshot['connected']}")
                assert not early_ack,"disconnect acknowledged before the blocked worker could remove the link"
                assert snapshot["connected"]==1,"disconnect acknowledged without removing the controller link"
                await until(lambda: slots[-1][0]==2)
                try:
                    await client.bluetooth_gatt_read(address,0x15)
                    raise AssertionError("disconnected peer retained its proxy slot")
                except BluetoothGATTAPIError:
                    pass
                assert await client.bluetooth_gatt_read(address+1,0x15)==b"\x93\x02"
                healthy()
                healthy=await client.bluetooth_device_connect(address,lambda *args: None,feature_flags=47,address_type=1)
                assert await client.bluetooth_gatt_read(address,0x15)==b"\x93\x02"
                await client.bluetooth_device_disconnect(address)
                await client.bluetooth_device_disconnect(address+1)
                healthy()
                other()
                await until(lambda: slots[-1][0]==3)
                async with http.get(control+"/gatt-stats") as response:
                    assert (await response.json())["connected"]==0
                print("PASS saturated disconnect preempts another address; 32 queued/48 refused; 64 retries preserve active cleanup; other link readable; same-address reconnect; all slots restored")
                healthy=await client.bluetooth_device_connect(address,lambda *args: None,feature_flags=47,address_type=1)
                other=await client.bluetooth_device_connect(address+1,lambda *args: None,feature_flags=47,address_type=1)
                read=asyncio.create_task(client.bluetooth_gatt_read(address+1,0x67))
                await radio_state(http,control,path="/gatt-stats",blocked_reads=1)
                await client.bluetooth_device_disconnect(address)
                try:
                    await asyncio.wait_for(read,1)
                    raise AssertionError("cancelled operation returned a successful late result")
                except BluetoothGATTAPIError:
                    pass
                assert await client.bluetooth_gatt_read(address+1,0x15)==b"\x93\x02"
                await client.bluetooth_device_disconnect(address+1)
                healthy()
                other()
                print("PASS unrelated-address preemption returns terminal GATT error; surviving link remains readable")
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
            print("PASS Noise rejection; default OFF/no radio; one persistent switch; exact raw ads; GATT UUID/handles/read/write/descriptor/notify/error; pair/unpair; 3-slot bound; active/queued connect cancellation; pause/resume/OFF slots")
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
        await generation_handoffs(start,key)


if __name__ == "__main__":
    asyncio.run(main())
