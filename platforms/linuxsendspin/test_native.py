#!/usr/bin/env python3
"""Real SDK companion maintenance/IPC smoke tests (never prints tokens).

python3 platforms/linuxsendspin/test_native.py --binary build/sendspin-host/encore-sendspin
Native ARM can also be tested with --runner qemu-arm (no sysroot: static).
With aiosendspin[server]==10.3.0 installed, add --protocol to test real Noise
pairing, controller command fences, reconnect and PCM over a loopback server.
"""
import argparse
import asyncio
import base64
import logging
import os
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import time
import unittest

PARSER = argparse.ArgumentParser()
PARSER.add_argument("--binary", type=Path, required=True)
PARSER.add_argument("--runner")
PARSER.add_argument("--protocol", action="store_true")
ARGS, REST = PARSER.parse_known_args()
EXE = ([ARGS.runner] if ARGS.runner else []) + [str(ARGS.binary.resolve())]
IPC_VERSION = 4
READY, PLAYING, STOPPED = 1 << 31, 1 << 30, 1 << 29


class Companion(unittest.TestCase):
    def setUp(self):
        # Generated secrets are ephemeral, not put in orb-backup/source kits.
        self.tmp = tempfile.TemporaryDirectory(prefix="sendspin-test-", dir="/dev/shm")
        self.addCleanup(self.tmp.cleanup)
        self.state = Path(self.tmp.name) / "private"

    def cli(self, mode):
        return subprocess.run(EXE + [mode, "--state", str(self.state)], capture_output=True, timeout=5)

    def token_text(self):
        p = self.cli("pairing-token")
        self.assertEqual(p.returncode, 0, "token CLI failed")
        self.assertEqual(p.stderr, b"", "SDK must not log")
        token = p.stdout.strip()
        self.assertTrue(token.startswith(b"SP:0") and len(token) == 107, "stdout must contain only token")
        return token

    def token(self):
        token = self.token_text()
        body = token[4:].replace(b"9", b"2")
        return base64.b32decode(body + b"=" * ((-len(body)) % 8))

    def child(self, url="ws://127.0.0.1:1/sendspin"):
        parent, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
        # The private CLI always receives exactly fd 3, as Go ExtraFiles does.
        def setup():
            os.dup2(child.fileno(), 3)
            os.set_inheritable(3, True)
        p = subprocess.Popen(EXE + ["run", "--state", str(self.state), "--name", "test",
                                   "--url", url, "--mac", "02:7a:12:93:df:01",
                                   "--manufacturer", "Amazon", "--model", "Echo Dot (2nd gen)",
                                   "--version", "0.10.0-dev.1"],
                             pass_fds=(child.fileno(), 3), preexec_fn=setup,
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        child.close()
        self.addCleanup(parent.close)
        def reap():
            if p.poll() is None:
                p.kill()
            p.communicate(timeout=5)
        self.addCleanup(reap)
        return p, parent

    def test_cold_restart_revoke_stable_identity(self):
        first = self.token()
        self.assertTrue(first == self.token(), "cold/restart token must be stable")
        self.assertEqual(self.cli("revoke-all").returncode, 0)
        second = self.token()
        self.assertTrue(first[:32] == second[:32], "revocation changed identity")
        self.assertTrue(first[32:] != second[32:], "old pairing secret not revoked")
        self.assertEqual(self.state.stat().st_mode & 0o777, 0o700)
        self.assertEqual((self.state / "state").stat().st_mode & 0o777, 0o600)

    def test_corruption_and_deleted_state_fail_closed(self):
        self.token()
        path = self.state / "state"
        original = path.read_bytes()
        broken = bytearray(original)
        broken[20] ^= 0x40
        path.write_bytes(broken)
        self.assertNotEqual(self.cli("pairing-token").returncode, 0)
        self.assertTrue(path.read_bytes() == broken, "corrupt identity was overwritten")
        path.unlink()
        self.assertNotEqual(self.cli("pairing-token").returncode, 0)
        self.assertFalse(path.exists())

    def test_failed_revocation_write_is_not_ignored(self):
        first = self.token()
        (self.state / "state.new").symlink_to("/dev/null")
        self.assertNotEqual(self.cli("revoke-all").returncode, 0)
        self.assertTrue(self.token() == first, "failed transaction damaged identity")

    def test_live_lock_and_malformed_ipc(self):
        self.token()
        p, parent = self.child()
        # Observe without acquiring the lock: maintenance polling can win the
        # startup race and legitimately make the run child fail closed.
        st = (self.state / "lock").stat()
        inode = f"{os.major(st.st_dev):02x}:{os.minor(st.st_dev):02x}:{st.st_ino}"
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            locks = (line.split() for line in Path("/proc/locks").read_text().splitlines())
            if any(len(row) >= 6 and row[1] == "FLOCK" and row[4] == str(p.pid)
                   and row[5] == inode for row in locks):
                break
            self.assertIsNone(p.poll(), "run child failed during startup")
            time.sleep(.005)
        else:
            self.fail("run child never acquired lock")
        self.assertNotEqual(self.cli("pairing-token").returncode, 0)
        self.assertNotEqual(self.cli("revoke-all").returncode, 0)
        parent.send(b"malformed")
        stdout, stderr = p.communicate(timeout=2)
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(stdout, b"")
        self.assertEqual(stderr, b"")

    def test_parent_close_terminates_child(self):
        self.token()
        p, parent = self.child()
        parent.close()
        p.communicate(timeout=2)
        self.assertNotEqual(p.returncode, 0)

    @unittest.skipUnless(ARGS.protocol, "requires --protocol and aiosendspin[server]==10.3.0")
    def test_real_noise_pairing_controller_and_reconnect(self):
        asyncio.run(self.protocol_roundtrip())

    async def protocol_roundtrip(self):
        from aiohttp import web
        from aiohttp.test_utils import TestServer
        from aiosendspin.audio import AudioFormat
        from aiosendspin.models.types import MediaCommand, PairMethod
        from aiosendspin.noise.keys import Identity
        from aiosendspin.noise.pairing import PairingAttempt
        from aiosendspin.noise.pairing_token import decode_psk_token
        from aiosendspin.noise.trust_store import InMemoryServerPairingStore, PskCategory
        from aiosendspin.server.roles.controller.events import (
            ControllerNextEvent, ControllerPauseEvent, ControllerPlayEvent,
        )
        from aiosendspin.server.server import SendspinServer

        # Avoid printing upstream diagnostics that may contain credentials.
        sdk_logger = logging.getLogger("aiosendspin")
        self.addCleanup(sdk_logger.setLevel, sdk_logger.level)
        sdk_logger.setLevel(logging.CRITICAL + 1)
        loop = asyncio.get_running_loop()
        store = InMemoryServerPairingStore()
        server = SendspinServer(loop, Identity.generate(), "test-server", pairing_store=store,
                                allow_noncompliant_clients=False)
        app = web.Application()
        app.router.add_get("/sendspin", server.on_client_connect)
        header = struct.Struct("<4sHHQQII")

        async def credentials(ipc):
            packet = await asyncio.wait_for(loop.sock_recv(ipc, 16384 + header.size), 5)
            self.assertEqual(header.unpack_from(packet), (b"ESPN", IPC_VERSION, 9, 0, 0, 150, 0))
            payload = packet[header.size:]
            token = decode_psk_token(payload[43:].decode("ascii"))
            self.assertEqual(payload[:43].decode("ascii"), token.client_id)
            return token

        async def connected():
            async with asyncio.timeout(10):
                while True:
                    client = server.get_client(token.client_id)
                    if client is not None and client.connection is not None:
                        return client
                    await asyncio.sleep(.01)

        async def readiness(ipc, wanted, *, after=0):
            async with asyncio.timeout(5):
                while True:
                    packet = await loop.sock_recv(ipc, 16384 + header.size)
                    self.assertTrue(packet, "companion exited before readiness changed")
                    magic, version, kind, gen, req, size, value = header.unpack_from(packet)
                    self.assertEqual((magic, version), (b"ESPN", IPC_VERSION))
                    if kind == 8 and value == wanted and gen > after:
                        self.assertEqual(size, 0)
                        self.assertEqual(req != 0, wanted != 0)
                        return gen, req

        async def command(ipc, gen, value):
            await loop.sock_sendall(ipc, header.pack(b"ESPN", IPC_VERSION, 7, gen, 0, 0, value))

        async def child_status(p):
            async with asyncio.timeout(5):
                while (status := p.poll()) is None:
                    await asyncio.sleep(.01)
                return status

        async def receive_audio(client, ipc):
            sample = struct.pack("<hh", 1234, -2345)
            stream = client.group.start_stream()
            stream.prepare_audio(sample * 24_000,
                                 AudioFormat(sample_rate=48_000, channels=2, bit_depth=16))
            await stream.commit_audio()
            async with asyncio.timeout(10):
                while True:
                    packet = await loop.sock_recv(ipc, 16384 + header.size)
                    self.assertTrue(packet, "companion exited before sending PCM")
                    magic, version, kind, epoch, request, size, _ = header.unpack_from(packet)
                    self.assertEqual((magic, version), (b"ESPN", IPC_VERSION))
                    if kind != 4:
                        continue
                    pcm = packet[header.size:]
                    self.assertEqual(len(pcm), size)
                    self.assertGreater(size, 0)
                    await loop.sock_sendall(ipc, header.pack(b"ESPN", IPC_VERSION, 5, epoch, request, 0, size))
                    # The SDK primes with silence and waits for consumption
                    # feedback, not just IPC acceptance, before aligning PCM.
                    frames = size // 4
                    await asyncio.sleep(frames / 48_000)
                    played_us = time.clock_gettime_ns(time.CLOCK_MONOTONIC_RAW) // 1_000
                    await loop.sock_sendall(ipc, header.pack(b"ESPN", IPC_VERSION, 6, epoch, played_us, 0, frames))
                    if sample in pcm:
                        return

        try:
            async with TestServer(app) as web_server:
                url = f"ws://127.0.0.1:{web_server.port}/sendspin"
                p, ipc = self.child(url)
                ipc.setblocking(False)
                token = await credentials(ipc)
                client = await connected()
                self.assertNotEqual(client.connection_security.psk_category, PskCategory.LONG_TERM)
                with self.assertRaises(TimeoutError):
                    await asyncio.wait_for(loop.sock_recv(ipc, header.size), .1)
                await asyncio.wait_for(server.initiate_pairing(token.client_id, PairingAttempt(
                    method=PairMethod.PAIRING_PSK, client_id=token.client_id, pairing_psk=token.pairing_psk,
                )), 10)
                self.assertEqual(client.connection_security.psk_category, PskCategory.LONG_TERM)
                old, group = await readiness(ipc, READY | STOPPED)
                self.assertEqual(client.info.name, "test")
                device = client.info.device_info
                self.assertEqual((device.mac_address, device.manufacturer, device.product_name, device.software_version),
                                 ("02:7a:12:93:df:01", "Amazon", "Echo Dot (2nd gen)", "0.10.0-dev.1"))
                controller = client.group.group_role("controller")
                commands = (1 << 0) | (1 << 2) | (1 << 3)
                controller.set_supported_commands([MediaCommand.PLAY, MediaCommand.PAUSE, MediaCommand.NEXT])
                idle, same_group = await readiness(ipc, READY | STOPPED | commands, after=old)
                self.assertEqual(same_group, group)
                events = asyncio.Queue()
                client.group.add_event_listener(lambda _, e: events.put_nowait(e)
                                                if isinstance(e, (ControllerNextEvent, ControllerPauseEvent,
                                                                  ControllerPlayEvent)) else None)
                client.group.start_stream()  # No PCM; authoritative group state drives readiness.
                playing, same_group = await readiness(ipc, READY | PLAYING | commands, after=idle)
                self.assertEqual(same_group, group)
                await command(ipc, idle, 3)  # Same capabilities, outdated playback snapshot.
                with self.assertRaises(TimeoutError):
                    await asyncio.wait_for(events.get(), .1)
                await command(ipc, playing, 3)
                self.assertIsInstance(await asyncio.wait_for(events.get(), 5), ControllerPauseEvent)
                await client.group.stop(keep_progress=True)  # Music Assistant's pause shape.
                paused, same_group = await readiness(ipc, READY | STOPPED | commands, after=playing)
                self.assertEqual(same_group, group)
                await command(ipc, paused, 1)
                self.assertIsInstance(await asyncio.wait_for(events.get(), 5), ControllerPlayEvent)
                await command(ipc, paused, 4)
                self.assertIsInstance(await asyncio.wait_for(events.get(), 5), ControllerNextEvent)
                await client.group.remove_client(client)  # Creates a new singleton group.
                client.group.group_role("controller").set_supported_commands(
                    [MediaCommand.PLAY, MediaCommand.PAUSE, MediaCommand.NEXT])
                regrouped, new_group = await readiness(ipc, READY | STOPPED | commands, after=paused)
                self.assertNotEqual(new_group, group)
                await client.connection.disconnect(retry_connection=False)
                await readiness(ipc, 0, after=regrouped)
                self.assertEqual(await child_status(p), 75)

                # New SDK process, same private state, no new pairing exchange.
                p, ipc = self.child(url)
                ipc.setblocking(False)
                self.assertEqual((await credentials(ipc)).client_id, token.client_id)
                client = await connected()
                self.assertEqual(client.connection_security.psk_category, PskCategory.LONG_TERM)
                controller = client.group.group_role("controller")
                controller.set_supported_commands([MediaCommand.NEXT])
                await readiness(ipc, READY | STOPPED | (1 << 3))
                await receive_audio(client, ipc)
                ipc.close()
                self.assertNotEqual(await child_status(p), 0)
        finally:
            await server.close()

        self.assertEqual(self.cli("revoke-all").returncode, 0)


if __name__ == "__main__":
    unittest.main(argv=[__file__] + REST)
