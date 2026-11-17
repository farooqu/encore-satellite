#!/usr/bin/env python3
"""Real SDK companion maintenance/IPC smoke tests (never prints tokens).

python3 platforms/linuxsendspin/test_native.py --binary build/sendspin-host/encore-sendspin
Native ARM can also be tested with --runner qemu-arm (no sysroot: static).
"""
import argparse
import base64
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest

PARSER = argparse.ArgumentParser()
PARSER.add_argument("--binary", type=Path, required=True)
PARSER.add_argument("--runner")
ARGS, REST = PARSER.parse_known_args()
EXE = ([ARGS.runner] if ARGS.runner else []) + [str(ARGS.binary.resolve())]


class Companion(unittest.TestCase):
    def setUp(self):
        # Generated secrets are ephemeral, not put in orb-backup/source kits.
        self.tmp = tempfile.TemporaryDirectory(prefix="sendspin-test-", dir="/dev/shm")
        self.addCleanup(self.tmp.cleanup)
        self.state = Path(self.tmp.name) / "private"

    def cli(self, mode):
        return subprocess.run(EXE + [mode, "--state", str(self.state)], capture_output=True, timeout=5)

    def token(self):
        p = self.cli("pairing-token")
        self.assertEqual(p.returncode, 0, "token CLI failed")
        self.assertEqual(p.stderr, b"", "SDK must not log")
        token = p.stdout.strip()
        self.assertTrue(token.startswith(b"SP:0") and len(token) == 107, "stdout must contain only token")
        body = token[4:].replace(b"9", b"2")
        return base64.b32decode(body + b"=" * ((-len(body)) % 8))

    def child(self):
        parent, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
        # The private CLI always receives exactly fd 3, as Go ExtraFiles does.
        def setup():
            os.dup2(child.fileno(), 3)
            os.set_inheritable(3, True)
        p = subprocess.Popen(EXE + ["run", "--state", str(self.state), "--name", "test",
                                   "--url", "ws://127.0.0.1:1/sendspin"],
                             pass_fds=(child.fileno(), 3), preexec_fn=setup,
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        child.close()
        self.addCleanup(parent.close)
        self.addCleanup(lambda: p.kill() if p.poll() is None else None)
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


if __name__ == "__main__":
    unittest.main(argv=[__file__] + REST)
