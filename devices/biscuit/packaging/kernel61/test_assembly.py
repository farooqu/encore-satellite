"""Offline trial boot contracts. All storage/mount/device operations are shims."""
import fcntl
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import struct
import subprocess
import sys
import tarfile
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from kernel61 import arm64_assemble, locked_build, rootfs, starter_build

HERE = Path(__file__).resolve().parent
BACKUP = HERE.parents[3] / ".amp/in/orb-backup"


class BootDecisionTests(unittest.TestCase):
    def setUp(self):
        BACKUP.mkdir(parents=True, exist_ok=True)
        # Exercise checkout paths like /home/runner as well as the target /run.
        self.temp = tempfile.TemporaryDirectory(prefix="runner-", dir=BACKUP)
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.busybox = shutil.which("busybox")
        self.assertIsNotNone(self.busybox, "BusyBox required for boot decisions")
        for name in ("dev/pts", "run/encore", "store/encore/kernel61-trial", "newroot", "proc", "sys"):
            (self.root / name).mkdir(parents=True)
        (self.root / "dev/mmcblk0p15").touch()

    def script(self, name, source):
        result = self.root / name
        result.write_text("#!/bin/sh\n" + source)
        result.chmod(0o755)
        return result

    def test_external_lock_rejects_prebuilt_outputs_and_substituted_inputs(self):
        lock = locked_build.read_lock((HERE / "ci-inputs.json").read_bytes())
        (self.root / "alpine-apks.tar.gz").write_bytes(b"substitute external bytes")
        with self.assertRaisesRegex(ValueError, "external input changed: alpine-apks.tar.gz"):
            locked_build.verify_inputs(self.root, lock["sha256"])
        lock["sha256"]["Image"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "invalid Biscuit external-input lock"):
            locked_build.read_lock(json.dumps(lock))

    def remap_paths(self, source, *directories):
        # Rewrite only root path components, without rescanning fixture paths.
        return re.sub(rf"(?<![\w/])/({'|'.join(directories)})(?![\w.-])",
                      lambda match: str(self.root / match[1]), source)

    def test_only_verified_ram_root_switches_and_every_failure_enters_rescue(self):
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode="w:gz") as t:
            content = b"encore-biscuit-kernel61-trial-v1\n"
            info = tarfile.TarInfo("etc/encore-base")
            info.size = len(content)
            t.addfile(info, io.BytesIO(content))
        archive = raw.getvalue()
        path = self.root / "store/encore/kernel61-trial/rootfs.tar.gz"
        bb = self.script("bb", '''
case "$1" in
mount)
  printf '%s\n' "$*" >> "$ROOT/events"
  case "$*" in *"$FAIL"*) [ "$FAIL" = ok ] || exit 1;; esac
  exit 0;;
mdev) exit 0;;
switch_root) echo SWITCH_ROOT >> "$ROOT/events"; exit 0;;
sleep) echo UNEXPECTED_SLEEP >> "$ROOT/events"; exit 1;;
esac
exec "$BUSYBOX" "$@"
''')
        source = (HERE / "init").read_text().replace("/bin/busybox.static", str(bb))
        source = source.replace("@ROOTFS_SHA256@", hashlib.sha256(archive).hexdigest())
        # In this host-only harness rescue exits, rather than opening a shell.
        a, b = source.index("rescue() {"), source.index("$BB mount -t proc")
        source = source[:a] + 'rescue() { echo "RESCUE $*" >> "$ROOT/events"; exit 0; }\n' + source[b:]
        source = source.replace("[ ! -b /dev/", "[ ! -f /dev/")
        source = self.remap_paths(source, "dev", "proc", "sys", "run", "newroot", "store")
        check = self.script("check", source)
        for mode in ("ok", "checksum", "extract", "-t ext4", "-t devtmpfs", "size=128m", "--move /proc"):
            with self.subTest(mode=mode):
                (self.root / "events").write_text("")
                path.write_bytes(archive if mode != "checksum" else archive + b"wrong")
                if mode == "extract":
                    path.write_bytes(b"not gzip")
                    check.write_text(source.replace(hashlib.sha256(archive).hexdigest(), hashlib.sha256(b"not gzip").hexdigest()))
                else:
                    check.write_text(source)
                result = subprocess.run(["sh", str(check)], capture_output=True, text=True,
                                        env={**os.environ, "ROOT": str(self.root), "BUSYBOX": self.busybox, "FAIL": mode}, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                events = (self.root / "events").read_text()
                self.assertEqual("SWITCH_ROOT" in events, mode == "ok", events + result.stderr)
                self.assertEqual("RESCUE" in events, mode != "ok", events)
                if mode in ("ok", "checksum", "extract", "-t ext4", "size=128m"):
                    self.assertIn("-o ro,noload", events)
                self.assertNotIn("UNEXPECTED_SLEEP", events)

    def test_starter_uses_verified_fallback_without_spending_attempts(self):
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode="w:gz") as tar:
            identity = (arm64_assemble.STARTER_FORMAT + "\n").encode()
            member = tarfile.TarInfo("etc/encore-base")
            member.size = len(identity)
            tar.addfile(member, io.BytesIO(identity))
        archive = raw.getvalue()
        ramdisk = arm64_assemble.initramfs(b"", hashlib.sha256(archive).hexdigest(), b"", starter=True)
        subprocess.run([self.busybox, "cpio", "-i", "init"], input=gzip.decompress(ramdisk),
                       cwd=self.root, capture_output=True, check=True)
        bb = self.script("bb", '''
case "$1" in
mount|umount|mdev|sync) exit 0;;
switch_root) echo SWITCH_ROOT; exit 0;;
mv) [ "$FAIL_RENAME" = 0 ] || exit 1;;
esac
exec "$BUSYBOX" "$@"
''')
        source = (self.root / "init").read_text().replace("/bin/busybox.static", str(bb))
        a, b = source.index("rescue() {"), source.index("$BB mount -t proc")
        source = source[:a] + 'rescue() { echo "RESCUE $*"; exit 1; }\n' + source[b:]
        source = source.replace("[ ! -b /dev/", "[ ! -f /dev/")
        source = self.remap_paths(source, "dev", "proc", "sys", "run", "newroot", "store")
        check = self.script("starter-check", source)
        store = self.root / "store/encore"
        booted = self.root / "run/encore/slot"
        (store / "format").write_text(arm64_assemble.STARTER_FORMAT + "\n")
        for slot in ("a", "b"):
            (store / "slots" / slot).mkdir(parents=True)
        for active, states, bad, fail_rename, expected in (
            ("a", ("held", "good"), "", False, "a"),
            ("a", ("held", "held"), "a", False, "b"),
            ("b", ("held", "trial 0"), "", False, "a"),
            ("a", ("bad", "held"), "b", False, None),
            ("a", ("held", "held"), "ab", False, None),
            ("a", ("trial 0", "held"), "", True, None),
        ):
            with self.subTest(active=active, states=states, bad=bad, fail_rename=fail_rename):
                booted.unlink(missing_ok=True)
                (store / "active").write_text(active + "\n")
                for slot, state in zip(("a", "b"), states):
                    (store / "slots" / f"{slot}.state").write_text(state + "\n")
                    (store / "slots" / slot / "rootfs.tar.gz").write_bytes(
                        archive + b"damaged" if slot in bad else archive)
                result = subprocess.run(["sh", str(check)], capture_output=True, text=True, timeout=10,
                                        env={**os.environ, "BUSYBOX": self.busybox,
                                             "FAIL_RENAME": str(int(fail_rename))})
                self.assertEqual(result.returncode, 0 if expected else 1, result.stdout + result.stderr)
                self.assertEqual("SWITCH_ROOT" in result.stdout, expected is not None, result.stdout)
                if expected:
                    self.assertEqual(booted.read_text().strip(), expected)
                else:
                    self.assertFalse(booted.exists())
                    self.assertIn("RESCUE", result.stdout)
                self.assertEqual((store / "active").read_text().strip(), expected or active)
                self.assertEqual(tuple((store / "slots" / f"{slot}.state").read_text().strip()
                                       for slot in ("a", "b")), states)

    def test_rearm_preserves_adjacent_bytes_and_precedes_trial_activation(self):
        archive = self.root / "rootfs.tar.gz"
        with tarfile.open(archive, "w:gz") as tar:
            for name, contents in (("etc/encore-base", (arm64_assemble.STARTER_FORMAT + "\n").encode()),
                                   ("sbin/init", b"#!/bin/sh\n")):
                member = tarfile.TarInfo(name)
                member.mode, member.size = 0o755, len(contents)
                tar.addfile(member, io.BytesIO(contents))
        compatibility = hashlib.sha256(b"separate kernel and device tree identity").hexdigest()
        ramdisk = arm64_assemble.initramfs(b"", hashlib.sha256(archive.read_bytes()).hexdigest(), b"",
                                          starter=True, signed=("0.1.1-dev.5", 22, b"", compatibility), recovery_rearm=True)
        subprocess.run([self.busybox, "cpio", "-i", "init"], input=gzip.decompress(ramdisk),
                       cwd=self.root, capture_output=True, check=True)
        verify = self.script("verify", f'[ "$5" = 22 ] && [ "$6" = {compatibility} ] && [ "$MODE" != unverified ]\n')
        bb = self.script("bb", '''
case "$1" in
mount|umount|mdev|sync) exit 0;;
switch_root) echo SWITCH_ROOT; exit 0;;
dd)
  case "$*" in
    *"of=$ROOT/dev/mmcblk0p8"*)
      echo WRITE >> "$ROOT/events"
      [ "$MODE" != write-error ] || exit 1
      [ "$MODE" != readback-error ] || exit 0
      # Bytes can be visible to readers even if the flush fails.
      if [ "$MODE" = flush-error ] && [ "${6-}" = conv=notrunc,fsync ]; then
        "$BUSYBOX" "$@" || exit 1
        exit 1
      fi;;
  esac;;
esac
exec "$BUSYBOX" "$@"
''')
        source = (self.root / "init").read_text().replace("/bin/busybox.static", str(bb))
        source = source.replace("/bin/arm64-verify", str(verify))
        a, b = source.index("rescue() {"), source.index("$BB mount -t proc")
        source = source[:a] + 'rescue() { echo "RESCUE $*"; exit 1; }\n' + source[b:]
        source = source.replace('[ ! -b /dev/', '[ ! -f /dev/').replace('[ -b "$misc" ]', '[ -f "$misc" ]')
        source = self.remap_paths(source, "dev", "proc", "sys", "run", "newroot", "store")
        # Leave /dev/zero backed by the host's zero device, not test storage.
        source = source.replace(str(self.root / "dev/zero"), "/dev/zero")
        check = self.script("routing-check", source)
        store = self.root / "store/encore"
        (store / "format").write_text(arm64_assemble.STARTER_FORMAT + "\n")
        for slot in ("a", "b"):
            directory = store / "slots" / slot
            directory.mkdir(parents=True)
            shutil.copyfile(archive, directory / "rootfs.tar.gz")
        partition = self.root / "sys/class/block/mmcblk0p8/partition"
        partition.parent.mkdir(parents=True)
        misc = self.root / "dev/mmcblk0p8"
        trailing = bytes(range(256)) * 8
        empty, recovery = bytes(32), b"boot-recovery".ljust(32, b"\0")
        for mode, before, after, failure, writes in (
            ("cold", empty + trailing, recovery + trailing, "", 1),
            ("trial", empty + trailing, recovery + trailing, "", 1),
            ("fallback", empty + trailing, recovery + trailing, "", 1),
            ("armed", recovery + trailing, recovery + trailing, "", 0),
            ("foreign", b"boot-fastboot".ljust(32, b"\0") + trailing, None, "misc-command", 0),
            ("short", bytes(31), None, "misc-command", 0),
            ("wrong-partition", empty + trailing, None, "misc-device", 0),
            ("unverified", empty + trailing, None, "no-verified-slot", 0),
            ("write-error", empty + trailing, None, "misc-write", 1),
            ("flush-error", empty + trailing, recovery + trailing, "misc-write", 1),
            ("readback-error", empty + trailing, None, "misc-write", 1),
        ):
            with self.subTest(mode=mode):
                # The loader consumes the command before each recovery boot.
                misc.write_bytes(before)
                partition.write_text("9\n" if mode == "wrong-partition" else "8\n")
                (store / "active").write_text("a\n" if mode in ("cold", "armed") else "b\n")
                (store / "slots/a.state").write_text("good\n")
                (store / "slots/b.state").write_text("update-spent\n" if mode == "fallback" else "update\n")
                marker = self.root / "run/encore/arm64-update-trial"
                marker.unlink(missing_ok=True)
                events = self.root / "events"
                events.write_text("")
                result = subprocess.run(["sh", str(check)], capture_output=True, text=True, timeout=10,
                                        env={**os.environ, "ROOT": str(self.root), "BUSYBOX": self.busybox, "MODE": mode})
                self.assertEqual(result.returncode, int(bool(failure)), result.stdout + result.stderr)
                self.assertEqual(result.stdout.strip(), "RESCUE " + failure if failure else "SWITCH_ROOT")
                self.assertEqual(misc.read_bytes(), before if after is None else after)
                self.assertEqual(events.read_text().splitlines(), ["WRITE"] * writes)
                self.assertEqual(marker.exists(), mode == "trial")
                self.assertEqual((store / "slots/b.state").read_text(),
                                 "update-spent\n" if mode in ("trial", "fallback") else "update\n")
                if not failure:
                    self.assertEqual((self.root / "run/encore/slot").read_text(), "b\n" if mode == "trial" else "a\n")

    def test_usb_rescue_restarts_only_its_own_gadget_with_one_console_owner(self):
        bb = self.script("usb-bb", '''
case "$1" in
mountpoint|mount|mdev) exit 0;;
# Host BusyBox may omit flock; use the host's real file descriptor lock.
flock) shift; exec flock "$@";;
mkdir)
  "$BUSYBOX" "$@" || exit
  case "$2" in */encore-biscuit61) "$BUSYBOX" mkdir "$2/functions" "$2/configs";; esac
  exit 0;;
sleep) [ "$2" != 2 ] || exit 88; exit 0;;
sh|setsid)
  [ ! -e /proc/$$/fd/9 ] || exit 77
  [ "$(cat "$ROOT/run/encore/usb-ready")" = "$PPID" ] || exit 76
  echo CONSOLE >> "$ROOT/events"; exit 0;;
esac
exec "$BUSYBOX" "$@"
''')
        source = (HERE / "usb-rescue").read_text().replace("/bin/busybox.static", str(bb))
        source = source.replace("test -c /dev/ttyGS0", "test -f /dev/ttyGS0")
        source = self.remap_paths(source, "dev", "sys", "run", "etc")
        check = self.script("usb-check", source)
        udcs = self.root / "sys/class/udc"
        udcs.mkdir(parents=True)
        gadget = self.root / "sys/kernel/config/usb_gadget/encore-biscuit61"
        gadget.parent.mkdir(parents=True)
        (self.root / "dev/ttyGS0").touch()
        (self.root / "etc/encore").mkdir(parents=True)
        for mode in ("none", "ambiguous", "existing", "single", "ctty"):
            with self.subTest(mode=mode):
                (self.root / "etc/encore/usb-ctty").unlink(missing_ok=True)
                if mode == "ctty":
                    (self.root / "etc/encore/usb-ctty").touch()
                shutil.rmtree(udcs)
                udcs.mkdir()
                if gadget.exists():
                    shutil.rmtree(gadget)
                if mode != "none":
                    (udcs / "controller-one").mkdir()
                if mode == "ambiguous":
                    (udcs / "controller-two").mkdir()
                if mode == "existing":
                    gadget.mkdir()
                    (gadget / "UDC").write_text("do-not-steal")
                (self.root / "events").write_text("")
                result = subprocess.run(["sh", str(check)], capture_output=True, text=True,
                                        env={**os.environ, "ROOT": str(self.root), "BUSYBOX": self.busybox}, timeout=10)
                success = mode in ("single", "ctty")
                self.assertEqual(result.returncode, 88 if success else 1, result.stderr)
                self.assertEqual((self.root / "events").read_text(), "CONSOLE\n" if success else "")
                if success:
                    self.assertEqual((gadget / "UDC").read_text(), "controller-one\n")
                    self.assertEqual((gadget / "idProduct").read_text(), "0x4ee7\n")
                    self.assertTrue((gadget / "configs/c.1/acm.usb0").is_symlink())
                    self.assertEqual([p.name for p in (gadget / "functions").iterdir()], ["acm.usb0"])
                    # Configfs returns a relative link even when created absolute.
                    link = gadget / "configs/c.1/acm.usb0"
                    link.unlink()
                    link.symlink_to("../../functions/acm.usb0")
                    # The supervisor can restart without trying to rebind UDC.
                    again = subprocess.run(["sh", str(check)], capture_output=True, text=True,
                                           env={**os.environ, "ROOT": str(self.root), "BUSYBOX": self.busybox}, timeout=10)
                    self.assertEqual(again.returncode, 88, again.stderr)
                    self.assertEqual((self.root / "events").read_text(), "CONSOLE\nCONSOLE\n")
                    self.assertFalse((self.root / "run/encore/usb-ready").exists())
                    # A second owner must not remove the first owner's readiness.
                    ready = self.root / "run/encore/usb-ready"
                    ready.write_text("owned-by-live-console")
                    with (self.root / "run/encore/usb-rescue.lock").open("w") as lock:
                        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                        blocked = subprocess.run(["sh", str(check)], capture_output=True, text=True,
                                                 env={**os.environ, "ROOT": str(self.root), "BUSYBOX": self.busybox}, timeout=10)
                        self.assertEqual(blocked.returncode, 1)
                        self.assertEqual(ready.read_text(), "owned-by-live-console")
                elif mode == "existing":
                    self.assertEqual((gadget / "UDC").read_text(), "do-not-steal")
                else:
                    self.assertFalse(gadget.exists())

    def test_wifi_firmware_is_readable_at_the_driver_path_before_power_on(self):
        firmware = self.root / "store/encore/firmware"
        firmware.mkdir(parents=True)
        (firmware / "WMT_SOC.cfg").write_bytes(b"config")
        (firmware / "WIFI_RAM_CODE_8163").write_bytes(b"radio-image")
        (self.root / "etc").mkdir()
        (self.root / "run/encore/usb-ready").write_text(str(os.getpid()))
        # The compiled MT6628 glue opens /etc/firmware/WIFI_RAM_CODE_8163;
        # reaching the same bytes through /lib/firmware alone is insufficient.
        source = (HERE / "boot.sh").read_text().split("/usr/local/bin/wmtup", 1)[0]
        source += 'test "$(cat /etc/firmware/WIFI_RAM_CODE_8163)" = radio-image\n'
        bb = self.script("firmware-bb", 'case "$1" in --install|mount) exit 0;; esac\nexec "$BUSYBOX" "$@"\n')
        firewall = self.script("firmware-firewall", "exit 0\n")
        source = source.replace("/bin/busybox.static", str(bb)).replace("/usr/local/sbin/encore-firewall", str(firewall))
        source = source.replace("\nmount ", "\n" + str(bb) + " mount ").replace("ip link set lo up", ":")
        source = re.sub(r"/(run|data|tmp|dev|store|system|lib|etc|vendor)(?=[/\s])",
                        lambda m: str(self.root / m[1]), source)
        check = self.script("firmware-check", source)
        result = subprocess.run(["sh", str(check)], capture_output=True, text=True,
                                env={**os.environ, "BUSYBOX": self.busybox}, timeout=10)
        self.assertEqual(result.returncode, 0, (self.root / "run/encore/base.log").read_text() + result.stderr)

    def test_base_never_enables_radio_without_usb_or_after_firewall_failure(self):
        # Run the real boot prefix up to radio/link activation; all mount and
        # firewall operations are shims. USB readiness uses real process liveness.
        source = (HERE / "boot.sh").read_text().split("ip link set lo up")[0] + "echo RADIO_ALLOWED\n"
        bb = self.script("base-bb", 'case "$1" in --install|mount) exit 0;; esac\nexec "$BUSYBOX" "$@"\n')
        firewall = self.script("firewall", 'exit "$FW_STATUS"\n')
        source = source.replace("/bin/busybox.static", str(bb)).replace("/usr/local/sbin/encore-firewall", str(firewall))
        source = source.replace("\nmount ", "\n" + str(bb) + " mount ").replace("sleep 1", ":")
        source = re.sub(r"/(run|data|tmp|dev)(?=[/\s])", lambda m: str(self.root / m[1]), source)
        check = self.script("base-check", source)
        ready = self.root / "run/encore/usb-ready"
        for owner, fw, allowed in ((None, "0", False), ("2147483647", "0", False),
                                   ("invalid", "0", False), (str(os.getpid()), "1", False),
                                   (str(os.getpid()), "0", True)):
            with self.subTest(owner=owner, firewall=fw):
                ready.unlink(missing_ok=True)
                if owner is not None:
                    ready.write_text(owner)
                result = subprocess.run(["sh", str(check)], capture_output=True, text=True,
                                        env={**os.environ, "BUSYBOX": self.busybox, "FW_STATUS": fw}, timeout=10)
                self.assertEqual(result.returncode, 0 if allowed else 1, result.stderr)
                self.assertEqual("RADIO_ALLOWED" in (self.root / "run/encore/base.log").read_text(), allowed)

    def test_dhcp_and_base_readiness_require_a_live_supplicant(self):
        source = (HERE / "boot.sh").read_text().split("echo ctrl_interface=", 1)[1]
        source = "set -eu\necho ctrl_interface=" + source
        source = source.replace("/run/", str(self.root / "run") + "/")
        check = self.script("wifi-start", source)
        # Model the retained binary's unusual usage/zero-exit behavior.
        self.script("wpa_supplicant", '''
case " $* " in *" -f "*) echo usage; exit 0;; esac
[ "$MODE" != failed ] || exit 1
[ "$MODE" != usagezero ] || { echo usage; exit 0; }
touch "$ROOT/daemon"
''')
        self.script("wpa_cli", '[ -e "$ROOT/daemon" ] || exit 1\nprintf "%s\\n" "$PONG"\n')
        self.script("udhcpc", 'touch "$ROOT/dhcp"\n')
        for mode, pong, allowed in (("good", "PONG", True), ("usagezero", "PONG", False),
                                     ("failed", "PONG", False), ("good", "FAIL", False)):
            with self.subTest(mode=mode, reply=pong):
                for name in ("daemon", "dhcp", "run/encore/base-ready"):
                    (self.root / name).unlink(missing_ok=True)
                result = subprocess.run(["sh", str(check)], capture_output=True, text=True,
                                        env={**os.environ, "ROOT": str(self.root), "MODE": mode,
                                             "PONG": pong, "PATH": str(self.root) + ":" + os.environ["PATH"]}, timeout=10)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)
                self.assertEqual((self.root / "dhcp").exists(), allowed)
                self.assertEqual((self.root / "run/encore/base-ready").exists(), allowed)


class RootfsTests(unittest.TestCase):
    def test_trial_replaces_release_composition_without_saved_state_or_legacy_idme(self):
        elf = bytearray(120)
        elf[:6] = b"\x7fELF\x02\x01"
        struct.pack_into("<H", elf, 18, 183)
        struct.pack_into("<Q", elf, 32, 64)
        struct.pack_into("<HH", elf, 54, 56, 1)
        struct.pack_into("<I", elf, 64, 1)
        tools = ("bin/busybox.static", "usr/local/bin/wmtup", "usr/local/bin/btbridge",
                 "sbin/wpa_supplicant", "sbin/wpa_cli", "lib/apk/db/installed", "usr/bin/dbus-daemon",
                 "usr/bin/dbus-uuidgen", "usr/lib/bluetooth/bluetoothd", "usr/sbin/dropbear", "usr/bin/dropbearkey",
                 "usr/bin/flock", "usr/sbin/iptables-legacy", "usr/sbin/ip6tables-legacy",
                 "usr/sbin/iptables-legacy-restore", "usr/sbin/ip6tables-legacy-restore")
        old = ("usr/local/sbin/slotctl", "usr/local/sbin/encore-recovery", "usr/local/sbin/encore-health", "etc/encore-package.json")

        def archive(extra=None):
            buf = io.BytesIO()
            with tarfile.open(fileobj=buf, mode="w:gz") as t:
                for name in (*tools, *old, *((extra,) if extra else ())):
                    info = tarfile.TarInfo(name)
                    info.size = len(elf)
                    t.addfile(info, io.BytesIO(elf))
            return buf.getvalue()

        result, busybox = rootfs.rootfs_archive(archive(), {})
        self.assertEqual(busybox, bytes(elf))
        with tarfile.open(fileobj=io.BytesIO(result), mode="r:gz") as t:
            for name in old:
                self.assertNotIn(name, t.getnames())
            self.assertEqual(t.extractfile("etc/encore-base").read(), b"encore-biscuit-kernel61-trial-v1\n")
            self.assertIn(b"/sys/firmware/devicetree/base/idme/bt_mac_addr/value", t.extractfile("usr/local/sbin/encore-bluetooth").read())
            self.assertIn(b"exec /bin/busybox.static init", t.extractfile("sbin/init").read())
            self.assertEqual(t.extractfile("etc/inittab").read(), b"::respawn:/usr/local/sbin/usb-rescue\n")
            self.assertNotIn("usr/local/bin/encore-satellite", t.getnames())
        for extra in ("data/encore/config.json", "store/encore/active", "../outside"):
            with self.subTest(extra=extra):
                with self.assertRaises(ValueError):
                    rootfs.rootfs_archive(archive(extra), {})

    def test_starter_packages_clock_sync_and_replaces_old_rescue_console(self):
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w:gz") as t:
            for name in ("usr/local/sbin/usb-rescue", "usr/local/sbin/usb-console", "etc/encore/usb-consoles"):
                data = b"obsolete"
                member = tarfile.TarInfo(name)
                member.size = len(data)
                t.addfile(member, io.BytesIO(data))
        packed = starter_build.starter_rootfs(archive.getvalue(), {})
        with tarfile.open(fileobj=io.BytesIO(packed), mode="r:gz") as t:
            self.assertIn(b"::respawn:/usr/local/sbin/runtime-service time-sync",
                          t.extractfile("etc/inittab").read().splitlines())
            self.assertEqual(t.extractfile("usr/local/sbin/usb-rescue").read(), (HERE / "usb-rescue").read_bytes())
            self.assertNotIn("usr/local/sbin/usb-console", t.getnames())
            self.assertNotIn("etc/encore/usb-consoles", t.getnames())


if __name__ == "__main__":
    unittest.main()
