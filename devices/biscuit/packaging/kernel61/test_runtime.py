"""Execute trial service contracts without any device or mounts."""
import hashlib
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import subprocess
import tempfile
import time
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]


def eventually(check, seconds=5):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if check():
            return
        time.sleep(.025)
    raise AssertionError("condition did not become true")


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=ROOT / ".amp/in/orb-backup")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bb = shutil.which("busybox")
        self.assertIsNotNone(self.bb)

    def test_stock_firmware_checks_both_slots_but_loads_only_a(self):
        root = self.root
        for directory in ("tmp", "run/encore", "dev/block", "bin"):
            (root / directory).mkdir(parents=True)
        mountinfo = root / "mountinfo"
        mountinfo.touch()
        script = (HERE / "stock-firmware").read_text().replace('[ -b "$devices/$node" ]', '[ -f "$devices/$node" ]')
        for path in ("work=/tmp;", "work=/run/encore;", "devices=/dev", "sys=/sys/class/block"):
            variable, location = path.split("=", 1)
            script = script.replace(path, f"{variable}={root}/{location[1:]}")
        script = script.replace("/proc/self/mountinfo", str(mountinfo))
        files = {}
        for line in script.splitlines():
            if len(line.split()) == 3 and len(line.split()[0]) == 64:
                _, _, name = line.split()
                payload = name.encode()
                files[name] = payload
                script = script.replace(line, f"{hashlib.sha256(payload).hexdigest()} {len(payload)} {name}")
        self.assertEqual(len(files), 4)
        for slot, number in (("a", 13), ("b", 14)):
            node = f"mmcblk0p{number}"
            (root / f"dev/{node}").touch()
            (root / f"dev/block/{node}").touch()
            sysfs = root / f"sys/class/block/{node}"
            sysfs.mkdir(parents=True)
            (sysfs / "uevent").write_text(f"PARTNAME=system_{slot}\n")
            (sysfs / "dev").write_text(f"179:{number}\n")
            stock = root / f"{node}/system/vendor/firmware"
            stock.mkdir(parents=True)
            (root / node / "vendor").symlink_to("/system/vendor")
            (root / node / "etc").symlink_to("/system/etc")
            for name, payload in files.items():
                (stock / name).write_bytes(payload)
        for name, body in (
            ("mount", 'test "$*" = "-t ext4 -o ro,noload,nosuid,nodev,noexec $5 $6"\n'
                      'echo "${5##*/}" >> "$FIXTURE/mounted"\ncp -a "$FIXTURE/${5##*/}/." "$6/"'),
            ("umount", 'rm -rf "$1/system" "$1/vendor" "$1/etc"'),
        ):
            command = root / "bin" / name
            command.write_text("#!/bin/sh\nset -eu\n" + body + "\n")
            command.chmod(0o700)
        env = {**os.environ, "PATH": f"{root / 'bin'}:{os.environ['PATH']}", "FIXTURE": str(root)}

        def run(mode):
            (root / "mounted").write_text("")
            return subprocess.run([self.bb, "sh", "-s", mode], input=script, text=True, env=env, capture_output=True)

        result = run("check")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((root / "mounted").read_text(), "mmcblk0p13\nmmcblk0p14\n")
        self.assertFalse(list((root / "tmp").iterdir()))
        # Same size, different bytes: system_b is checked, not just present.
        b_file = root / "mmcblk0p14/system/vendor/firmware/WMT_SOC.cfg"
        b_file.write_bytes(b"!" * len(files[b_file.name]))
        result = run("check")
        self.assertEqual(result.returncode, 1)
        self.assertIn("system_b lacks", result.stderr)
        self.assertFalse(list((root / "tmp").iterdir()))
        mountinfo.write_text("1 0 179:13 / /some-alias rw - ext4 ignored rw\n")
        result = run("load")
        self.assertIn("system_a is already mounted", result.stderr)
        self.assertEqual((root / "mounted").read_text(), "")
        self.assertFalse((root / "run/encore/firmware").exists())
        mountinfo.write_text("")
        result = run("load")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((root / "mounted").read_text(), "mmcblk0p13\n")
        output = root / "run/encore/firmware"
        self.assertEqual({p.name: p.read_bytes() for p in output.iterdir()}, files)
        self.assertEqual(list(output.parent.iterdir()), [output])

    def test_base_reaches_applet_install_with_no_path_applets(self):
        # A fresh root has BusyBox but not its applet links. Do not let the
        # host PATH conceal dependencies in the supervisor before base runs.
        bindir = self.root / "bin"
        bindir.mkdir()
        boot = self.root / "boot"
        ready = self.root / "base-ready"
        # Match the stock WLAN file size without using vendor firmware bytes.
        firmware = self.root / "WIFI_RAM_CODE_8163"
        firmware.write_bytes(b"wifi" * 93460)
        installed = self.root / "initialized"
        boot.write_text('#!/bin/sh\nset -eu\n'
                        'for applet in mkdir sleep cat; do\n'
                        '  if command -v "$applet"; then exit 9; fi\n'
                        'done\n'
                        f'"{self.bb}" --install -s "{bindir}"\n'
                        f'mkdir "{installed}"\n'
                        f'cp "{firmware}" "{installed}/"\n'
                        f'echo ready > "{ready}"\n')
        boot.chmod(0o700)
        script = self.root / "service"
        script.write_text((HERE / "runtime-service").read_text()
                          .replace("/bin/busybox.static", self.bb)
                          .replace("export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
                                   "export PATH=" + str(bindir))
                          .replace("/run/encore/services", str(self.root / "services"))
                          .replace("/etc/encore/boot.sh", str(boot)))
        log = self.root / "supervisor.log"
        with log.open("wb") as output:
            guard = subprocess.Popen([self.bb, "sh", str(script), "base"],
                                     stdin=subprocess.DEVNULL, stdout=output, stderr=output)
            try:
                state = self.root / "services/base/exit-status"
                eventually(lambda: state.exists() or guard.poll() is not None)
                self.assertTrue(state.exists(), log.read_text())
                self.assertEqual(state.read_text().strip(), "0")
                self.assertEqual(ready.read_text(), "ready\n")
                self.assertEqual((installed / firmware.name).read_bytes(), firmware.read_bytes())
                self.assertIsNone(guard.poll())
            finally:
                if guard.poll() is None:
                    guard.terminate()
                guard.wait(timeout=3)

    def test_wifi_owner_restores_only_new_saved_config_and_refuses_live_replacement(self):
        base = self.root / "base-ready"
        base.touch()
        (self.root / "wlan0").touch()
        (self.root / "run").mkdir()
        saved = self.root / "saved-wpa.conf"
        live = self.root / "run/wpa_supplicant.conf"
        runner = self.root / "wpa"
        runner.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\ncat "' + str(live) + '"\n')
        runner.chmod(0o700)
        script = self.root / "service"
        script.write_text((HERE / "runtime-service").read_text()
                          .replace("/bin/busybox.static", self.bb)
                          .replace("/run/encore/base-ready", str(base))
                          .replace("/sys/class/net/wlan0", str(self.root / "wlan0"))
                          .replace("/data/encore/wpa_supplicant.conf", str(saved))
                          .replace("/run/encore/wpa_supplicant.conf", str(live))
                          .replace("exec wpa_supplicant", "exec " + str(runner)))
        empty = "ctrl_interface=/run/wpa_supplicant\n"
        config = empty + "update_config=0\nnetwork={\n ssid=74657374\n}\n"
        for mode in ("fresh", "saved", "empty", "occupied"):
            with self.subTest(mode=mode):
                saved.unlink(missing_ok=True)
                live.unlink(missing_ok=True)
                if mode != "fresh":
                    saved.write_text("" if mode == "empty" else config)
                if mode == "occupied":
                    live.write_text("another owner\n")
                result = subprocess.run([self.bb, "sh", str(script), "wifi", "worker"],
                                        capture_output=True, text=True, timeout=3)
                if mode == "occupied":
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, "")
                    self.assertEqual(live.read_text(), "another owner\n")
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    expected = config if mode == "saved" else empty
                    self.assertEqual(live.read_text(), expected)
                    self.assertEqual(live.stat().st_mode & 0o777, 0o600)
                    self.assertEqual(result.stdout, "-D\nnl80211\n-i\nwlan0\n-c\n" + str(live) + "\n" + expected)

    def test_starter_enrolls_and_starts_muted_app_without_operator_markers(self):
        run = self.root / "run"
        run.mkdir()
        (run / "base-ready").touch()
        (run / "bluetooth-ready").write_text("14\n")
        (run / "udhcpc.pid").write_text("29\n")
        (self.root / "encore-base").write_text("encore-biscuit-arm64-starter-v1\n")
        script = self.root / "service"
        source = (HERE / "runtime-service").read_text().replace("/bin/busybox.static", self.bb)
        for source_path, target in (("/run/encore", run), ("/run/udhcpc.pid", run / "udhcpc.pid"),
                                    ("/etc/encore-base", self.root / "encore-base"),
                                    ("/sys/devices/platform/amz_privacy/privacy_state", self.root / "privacy"),
                                    ("/usr/local/bin/encore-satellite", self.root / "provision"),
                                    ("/usr/local/sbin/slotctl", self.root / "slotctl"),
                                    ("/usr/local/sbin/encore-run", self.root / "app")):
            source = source.replace(source_path, str(target))
        script.write_text(source)
        events = self.root / "events"
        for command, output in (("provision", 'provision "$@"'), ("slotctl", 'slotctl "$@"'), ("app", 'app "$@"')):
            path = self.root / command
            path.write_text(f"#!/bin/sh\necho {output} >> '{events}'\n")
            path.chmod(0o700)

        def worker(name):
            return [self.bb, "sh", str(script), name, "worker"]

        # Bluetooth readiness alone cannot start the app or spend a slot attempt.
        with self.assertRaises(subprocess.TimeoutExpired):
            subprocess.run(worker("app"), capture_output=True, timeout=.2)
        self.assertFalse(events.exists())
        subprocess.run(worker("enrollment"), capture_output=True, timeout=2, check=True)
        self.assertTrue((run / "enrollment-ready").exists())
        self.assertEqual(events.read_text(), "provision --provision-only\n")
        for privacy in (None, "0\n", "unknown\n", "1\n"):
            with self.subTest(privacy=privacy):
                if privacy is not None:
                    (self.root / "privacy").write_text(privacy)
                result = subprocess.run(worker("app"), capture_output=True, timeout=2)
                self.assertEqual(result.returncode == 0, privacy == "1\n", result.stderr)
                self.assertEqual((run / "app-started").exists(), privacy == "1\n")
                expected = "provision --provision-only\n" + ("slotctl arm\napp --require-muted-start\n" if privacy == "1\n" else "")
                self.assertEqual(events.read_text(), expected)

    def test_failed_service_parks_and_never_duplicates_even_after_guard_restart(self):
        runner = self.root / "wmtup"
        runner.write_text('#!/bin/sh\necho run >> "' + str(self.root / 'runs') + '"\nexit 7\n')
        runner.chmod(0o700)
        (self.root / "base-ready").touch()
        script = self.root / "service"
        script.write_text((HERE / "runtime-service").read_text()
                          .replace("/bin/busybox.static", self.bb)
                          .replace("/run/encore/base-ready", str(self.root / "base-ready"))
                          .replace("/run/encore/services", str(self.root / "services"))
                          .replace("/usr/local/bin/wmtup", str(runner)))
        def launch():
            return subprocess.Popen([self.bb, "sh", str(script), "radio"],
                                    stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                    stderr=subprocess.DEVNULL)
        first = launch()
        try:
            state = self.root / "services/radio/exit-status"
            eventually(state.exists)
            self.assertEqual(state.read_text().strip(), "7")
            self.assertIsNone(first.poll())
            duplicate = launch()
            self.assertEqual(duplicate.wait(timeout=2), 1)
        finally:
            first.terminate()
            first.wait(timeout=3)
        again = launch()
        try:
            time.sleep(.15)
            self.assertEqual((self.root / "runs").read_text(), "run\n")
            self.assertIsNone(again.poll())
        finally:
            again.terminate()
            again.wait(timeout=3)

    def test_network_manager_starts_after_base_ready_and_retries_with_backoff(self):
        run = self.root / "run"
        run.mkdir()
        runs = self.root / "runs"
        runner = self.root / "network"
        runner.write_text(f'#!/bin/sh\necho run >> "{runs}"\n[ "$(wc -l < "{runs}")" = 2 ] || exit 7\n')
        runner.chmod(0o700)
        script = self.root / "service"
        script.write_text((HERE / "runtime-service").read_text()
                          .replace("/bin/busybox.static", self.bb)
                          .replace("/run/encore", str(run))
                          .replace("/usr/local/sbin/encore-network", str(runner)))
        guard = subprocess.Popen([self.bb, "sh", str(script), "network"],
                                 stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            status = run / "services/network/exit-status"
            eventually((status.parent / "child.pid").exists)
            self.assertFalse(runs.exists())
            (run / "base-ready").touch()
            eventually(lambda: status.exists() and status.read_text().strip() == "7")
            time.sleep(.15)
            self.assertEqual(runs.read_text(), "run\n")
            eventually(lambda: status.read_text().strip() == "0", seconds=8)
            self.assertEqual(runs.read_text(), "run\nrun\n")
            self.assertIsNone(guard.poll())
        finally:
            if guard.poll() is None:
                guard.terminate()
            guard.wait(timeout=3)

    def test_stubborn_peripheral_does_not_hold_service_guard_shutdown(self):
        runner = self.root / "wmtup"
        runner.write_text("#!/bin/sh\ntrap '' TERM\nwhile :; do sleep 1; done\n")
        runner.chmod(0o700)
        (self.root / "base-ready").touch()
        script = self.root / "service"
        script.write_text((HERE / "runtime-service").read_text()
                          .replace("/bin/busybox.static", self.bb)
                          .replace("/run/encore/base-ready", str(self.root / "base-ready"))
                          .replace("/run/encore/services", str(self.root / "services"))
                          .replace("/usr/local/bin/wmtup", str(runner)))
        guard = subprocess.Popen([self.bb, "sh", str(script), "radio"],
                                 stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                 stderr=subprocess.DEVNULL)
        child = None
        try:
            path = self.root / "services/radio/child.pid"
            eventually(lambda: path.exists() and path.read_text().strip())
            child = int(path.read_text())
            eventually(lambda: os.getpgid(child) == child)
            guard.terminate()
            self.assertEqual(guard.wait(timeout=2.5), 0)
        finally:
            if guard.poll() is None:
                guard.kill()
                guard.wait(timeout=2)
            if child is not None:
                try:
                    os.killpg(child, signal.SIGKILL)
                except ProcessLookupError:
                    pass

    def test_controlling_tty_script_shell_executes_frames_and_interrupts(self):
        master, slave = pty.openpty()
        self.addCleanup(os.close, master)
        # The tool host may inherit SIGINT ignored; PID1 children do not.
        def signals():
            signal.signal(signal.SIGINT, signal.SIG_DFL)
            signal.signal(signal.SIGQUIT, signal.SIG_DFL)
        child = subprocess.Popen([self.bb, "setsid", "-c", self.bb, "sh", "/proc/self/fd/0"],
                                 stdin=slave, stdout=slave, stderr=slave, preexec_fn=signals)
        os.close(slave)
        try:
            # Octal framing ensures terminal input echo cannot fake success.
            os.write(master, b"printf '\\106\\122\\101\\115\\105\\n'\n")
            data = b""
            end = time.monotonic() + 3
            while b"FRAME\r\n" not in data and time.monotonic() < end:
                if select.select([master], [], [], .1)[0]:
                    data += os.read(master, 4096)
            self.assertIn(b"FRAME\r\n", data)
            self.assertNotIn(b"\x1b[6n", data)
            os.write(master, b"/usr/bin/sleep 30\n")
            time.sleep(.15)
            os.write(master, b"\x03")
            self.assertEqual(child.wait(timeout=2), -signal.SIGINT)
        finally:
            if child.poll() is None:
                os.killpg(child.pid, signal.SIGKILL)
                child.wait(timeout=2)


if __name__ == "__main__":
    unittest.main()
