"""Execute boot/health decisions with private files, never partitions or hardware."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import package


class BootHealthTests(unittest.TestCase):
    def setUp(self):
        backup = package.ROOT / ".amp/in/orb-backup"
        backup.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=backup)
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for directory in ("run/encore", "data/encore", "store/encore/slots", "tools"):
            (self.root / directory).mkdir(parents=True)
        self.env = {**os.environ, "ROOT": str(self.root),
                    "PATH": str(self.root / "tools") + ":" + os.environ["PATH"]}

    def script(self, name, source):
        path = self.root / name
        path.write_text("#!/bin/sh\n" + source)
        path.chmod(0o755)
        return path

    def relocated(self, source):
        for directory in ("run", "data", "store"):
            source = source.replace("/" + directory + "/", str(self.root / directory) + "/")
        return source

    def test_wifi_startup_requires_a_responding_control_socket(self):
        self.script("tools/wpa_supplicant", '''
# The shipped Alpine build prints usage and exits zero for unsupported -f.
for arg in "$@"; do [ "$arg" != -f ] || { echo usage; exit 0; }; done
case "$MODE" in
  ready) touch "$ROOT/daemon-live";;
  usage-zero) echo usage;;
  failed) exit 1;;
esac
''')
        self.script("tools/wpa_cli", '''
if [ -e "$ROOT/daemon-live" ]; then echo PONG; else echo FAIL; exit 1; fi
''')
        self.script("tools/udhcpc", 'echo dhcp > "$ROOT/dhcp-started"\n')
        source = (package.HERE / "boot.sh").read_text()
        source = "set -eu\n" + source[source.index('if [ -s /data/encore/wpa_supplicant.conf ]; then'):]
        script = self.script("wifi-boot-check", self.relocated(source))
        for mode in ("ready", "usage-zero", "failed"):
            with self.subTest(mode=mode):
                for name in ("daemon-live", "dhcp-started", "run/encore/base-ready"):
                    (self.root / name).unlink(missing_ok=True)
                result = subprocess.run(["sh", str(script)], capture_output=True, text=True,
                                        env={**self.env, "MODE": mode}, timeout=10)
                self.assertEqual(result.returncode == 0, mode == "ready", result.stderr)
                self.assertEqual((self.root / "run/encore/base-ready").exists(), mode == "ready")
                self.assertEqual((self.root / "dhcp-started").exists(), mode == "ready")

    def test_misc_exact_command_and_failure_gate_before_rootfs_mount(self):
        busybox = shutil.which("busybox")
        self.assertIsNotNone(busybox, "BusyBox is required to check the initramfs applets")
        bb = self.script("tools/bb", '''
case "$1" in
dd)
  case "$2" in
    if=*/misc)
      [ "$FAIL" != read ] || exit 1
      if [ "$FAIL" = short ]; then printf short > "$ROOT/run/misc-command"; exit 0; fi;;
    if=*/recovery-command)
      case "$FAIL" in write) exit 1;; readback) exit 0;; esac;;
  esac;;
sync) [ "$FAIL" != sync ] || exit 1;;
mount) echo ROOTFS_MOUNT; exit 1;;
esac
exec "$BUSYBOX" "$@"
''')
        source = (package.HERE / "init").read_text()
        source = source[source.index("select_recovery() {"):source.index("  slot=$(")]
        source += "  echo unexpected-rootfs\nfi\necho RESCUE\n"
        source = self.relocated(source).replace("/dev/mmcblk0p8", str(self.root / "misc"))
        script = self.script("init-check", f"BB={bb}\n" + source)
        expected = b"boot-recovery".ljust(32, b"\0")
        metadata = b"slot metadata must not change" * 5
        for mode in ("ok", "already", "embedded-nul", "read", "short", "write", "sync", "readback"):
            with self.subTest(mode=mode):
                original = expected if mode == "already" else b"boot-\0recovery".ljust(32, b"\0")
                (self.root / "misc").write_bytes(original + metadata)
                result = subprocess.run(["sh", str(script)], capture_output=True, text=True,
                                        env={**self.env, "FAIL": mode, "BUSYBOX": busybox}, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("RESCUE", result.stdout)
                self.assertEqual("ROOTFS_MOUNT" in result.stdout, mode in ("ok", "already", "embedded-nul"))
                data = (self.root / "misc").read_bytes()
                self.assertEqual(data[32:], metadata)
                self.assertEqual(data[:32], expected if mode in ("ok", "already", "embedded-nul", "sync") else original)

    def test_only_live_provisioning_pauses_budget_and_credentials_resume_health(self):
        self.script("tools/date", 'echo $((1000 + $(cat "$ROOT/count")))\n')
        self.script("tools/sync", "exit 0\n")
        self.script("tools/reboot", 'echo reboot >> "$ROOT/events"\n')
        self.script("tools/slotctl", 'echo "$1" >> "$ROOT/events"\n')
        self.script("tools/sleep", '''
n=$(cat "$ROOT/count"); n=$((n+1)); echo "$n" > "$ROOT/count"
[ "$n" -lt 1000 ] || exit 1
case "$MODE" in
  healthy|credentials|runtime)
    if [ "$n" -le 400 ]; then echo "$LIVE_PID $((1000+n))" > "$ROOT/run/encore/provisioning-ready"; fi;;
esac
if [ "$n" -eq 400 ]; then
  case "$MODE" in credentials|runtime) echo wifi > "$ROOT/data/encore/wpa_supplicant.conf";; esac
  if [ "$MODE" = runtime ]; then echo "$LIVE_PID" > "$ROOT/run/encore/ready"; fi
fi
''')
        source = self.relocated((package.HERE / "health").read_text())
        source = source.replace("/usr/local/sbin/slotctl", str(self.root / "tools/slotctl"))
        script = self.script("health-check", source)
        (self.root / "run/encore/slot").write_text("a\n")
        (self.root / "run/encore/base-ready").touch()
        (self.root / "run/encore/bluetooth-ready").write_text(str(os.getpid()))
        (self.root / "store/encore/slots/a.state").write_text("trial 1\n")
        for mode in ("bluetooth-only", "dead", "frozen", "malformed", "healthy", "credentials", "runtime"):
            with self.subTest(mode=mode):
                for name in ("events", "run/encore/ready", "run/encore/provisioning-ready", "data/encore/wpa_supplicant.conf"):
                    (self.root / name).unlink(missing_ok=True)
                (self.root / "count").write_text("0\n")
                if mode != "bluetooth-only":
                    marker = "not-a-heartbeat" if mode == "malformed" else f"{2147483647 if mode == 'dead' else os.getpid()} 1000\n"
                    (self.root / "run/encore/provisioning-ready").write_text(marker)
                result = subprocess.run(["sh", str(script)], capture_output=True, text=True,
                                        env={**self.env, "MODE": mode, "LIVE_PID": str(os.getpid())}, timeout=30)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual((self.root / "events").read_text(), "commit\n" if mode == "runtime" else "reboot\n")
                count = int((self.root / "count").read_text())
                self.assertLess(count, 610)
                if mode in ("healthy", "credentials", "runtime"):
                    self.assertGreaterEqual(count, 400)
                else:
                    self.assertLessEqual(count, 191)

    def test_arm64_update_health_commits_or_requests_fallback(self):
        self.script("tools/sync", "exit 0\n")
        self.script("tools/reboot", 'echo fallback >> "$ROOT/events"\n')
        self.script("tools/sleep", "exit 0\n")
        slotctl = self.script("tools/slotctl", (package.HERE / "slotctl").read_text())
        source = self.relocated((package.HERE / "health").read_text())
        source = source.replace("/usr/local/sbin/slotctl", str(slotctl))
        script = self.script("arm64-update-health", source)
        store = self.root / "store/encore"
        booted = self.root / "run/encore/slot"
        booted.write_text("b\n")
        (booted.parent / "arm64-update-trial").touch()
        (store / "format").write_text("encore-biscuit-arm64-starter-v1\n")
        (store / "active").write_text("b\n")
        (store / "highest-revision").write_text("24\n")
        (store / "slots/a.state").write_text("good\n")
        for healthy in (True, False):
            with self.subTest(healthy=healthy):
                (store / "slots/b.state").write_text("update-spent\n")
                for name in ("base-ready", "ready"):
                    path = booted.parent / name
                    if healthy:
                        path.write_text(str(os.getpid()) + "\n")
                    else:
                        path.unlink(missing_ok=True)
                subprocess.run(["sh", str(script), "supervised"], check=True, timeout=10,
                               env={**self.env, "STORE": str(store), "BOOTED": str(booted)})
                self.assertEqual((store / "slots/b.state").read_text(), "good\n" if healthy else "update-spent\n")
                self.assertEqual((store / "slots/a.state").read_text(), "good\n")
                self.assertEqual((store / "highest-revision").read_text(), "24\n")
                self.assertEqual((self.root / "events").exists(), not healthy)

    def test_held_candidate_neither_commits_nor_spends_health_budget_until_armed(self):
        self.script("tools/sync", "exit 0\n")
        self.script("tools/reboot", 'echo reboot >> "$ROOT/events"\n')
        self.script("tools/slotctl", 'echo "$1" >> "$ROOT/events"\n')
        self.script("tools/sleep", '''
n=$(cat "$ROOT/count"); n=$((n+1)); echo "$n" > "$ROOT/count"
[ "$n" -lt 420 ] || exit 1
if [ "$MODE" = arm ] && [ "$n" -eq 400 ]; then
  echo 'trial 1' > "$ROOT/store/encore/slots/a.state"
fi
''')
        source = self.relocated((package.HERE / "health").read_text())
        source = source.replace("/usr/local/sbin/slotctl", str(self.root / "tools/slotctl"))
        script = self.script("held-health-check", source)
        (self.root / "run/encore/slot").write_text("a\n")
        (self.root / "run/encore/base-ready").touch()
        # Even a live/stale readiness marker must not commit a held slot.
        (self.root / "run/encore/ready").write_text(str(os.getpid()))
        for mode in ("hold", "arm"):
            with self.subTest(mode=mode):
                (self.root / "events").unlink(missing_ok=True)
                (self.root / "count").write_text("0\n")
                (self.root / "store/encore/slots/a.state").write_text("held\n")
                result = subprocess.run(["sh", str(script)], capture_output=True, text=True,
                                        env={**self.env, "MODE": mode}, timeout=30)
                self.assertEqual(result.returncode, 0 if mode == "arm" else 1, result.stderr)
                self.assertGreaterEqual(int((self.root / "count").read_text()), 400)
                events = self.root / "events"
                if mode == "arm":
                    self.assertEqual(events.read_text(), "commit\n")
                else:
                    self.assertFalse(events.exists())


if __name__ == "__main__":
    unittest.main()
