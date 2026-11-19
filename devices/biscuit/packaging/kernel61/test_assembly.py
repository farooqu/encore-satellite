"""Offline trial boot contracts. All storage/mount/device operations are shims."""
import fcntl
import gzip
import hashlib
import io
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
from kernel61 import assemble

HERE = Path(__file__).resolve().parent
BACKUP = HERE.parents[3] / ".amp/in/orb-backup"
MAP = "c0008000 T _text\nc0d81ef0 B _end\n"


def template(page=2048):
    header = bytearray(page)
    header[:8] = b"ANDROID!"
    struct.pack_into("<10I", header, 8, 97, 0x40008000, 13, 0x43478000,
                     0, 0x41000000, 0x48000000, page, 0, 0x12345678)
    header[48:64] = b"offline-fixture!"
    command = b"bootopt=64S3,32N2,32N2 console=ttyS0 root=/dev/old skip_initramfs"
    header[64:64+len(command)] = command
    # Extra command-line fields must be sanitized too, not silently ignored.
    header[608:608+len(b" init=/evil dm=old resume=/dev/x")] = b" init=/evil dm=old resume=/dev/x"
    oldkernel = bytearray(97)
    struct.pack_into("<I", oldkernel, 36, 0x016F2818)
    return bytes(header) + oldkernel.ljust(page, b"\0") + b"old initramfs".ljust(page, b"\0")


class EnvelopeTests(unittest.TestCase):
    def test_addresses_sections_command_and_android_id(self):
        payload, ramdisk = b"new-kernel" * 301, gzip.compress(b"new ramdisk" * 83, mtime=0)
        for page in (2048, 4096, 8192, 16384):
            with self.subTest(page=page):
                original = template(page)
                result = assemble.boot_image(original, payload, ramdisk, MAP)
                self.assertEqual(struct.unpack_from("<I", result, 12)[0], 0x40008000)
                self.assertEqual(struct.unpack_from("<I", result, 20)[0], 0x43478000)
                self.assertEqual(result[24:64], original[24:64])
                self.assertEqual(result[64:576].split(b"\0")[0], b"bootopt=64S3,32N2,32N2 console=ttyS0 rdinit=/init")
                self.assertEqual(result[608:1632], bytes(1024))
                self.assertEqual(result[page:page+len(payload)], payload)
                offset = page + ((len(payload) + page - 1) // page) * page
                self.assertEqual(result[offset:offset+len(ramdisk)], ramdisk)
                expected = hashlib.sha1(payload + struct.pack("<I", len(payload)) +
                                        ramdisk + struct.pack("<I", len(ramdisk)) + bytes(4)).digest()
                self.assertEqual(result[576:608], expected + bytes(12))

    def test_rejects_unsafe_headers_memory_ranges_and_truncation(self):
        for offset, value in ((8, 0), (12, 0x40010000), (20, 0x43020000),
                              (20, 0x443FFFE0), (24, 8), (32, 0x43000000),
                              (36, 1024), (40, 1), (40, 100)):
            with self.subTest(offset=offset, value=value):
                changed = bytearray(template())
                struct.pack_into("<I", changed, offset, value)
                with self.assertRaises(ValueError):
                    assemble.boot_image(changed, b"kernel", b"r" * 65, MAP)
        for original, symbols in ((template()[:-1], MAP),
                                   (template().replace(b"bootopt=64S3,32N2,32N2", b"bootopt=64S3,64N2,64N2"), MAP),
                                   (template().replace(struct.pack("<I", 0x016F2818), b"MTK!"), MAP),
                                   (template(), "c0008000 T _text\nc3008001 B _end"),
                                   (template(), "c0008000 T _text\n")):
            with self.subTest(symbols=symbols, size=len(original)):
                with self.assertRaises(ValueError):
                    assemble.boot_image(original, b"kernel", b"ramdisk", symbols)

    def test_ramdisk_avoids_both_reservations_tags_and_ram_end(self):
        # 65 bytes distinguishes an exact end boundary from one-byte overlap.
        for address, accepted in ((0x4302FFFF, False), (0x43030000, True),
                                  (0x443FFFBF, True), (0x443FFFC0, False),
                                  (0x44400000, False), (0x445FFFFF, False),
                                  (0x44600000, True), (0x46000000, True),
                                  (0x47FFFFBF, True), (0x47FFFFC0, False),
                                  (0x48000000, False), (0x4800FFFF, False),
                                  (0x48010000, True), (0x5FFFFFBF, True),
                                  (0x5FFFFFC0, False)):
            with self.subTest(address=hex(address)):
                header = bytearray(template())
                struct.pack_into("<I", header, 20, address)
                if accepted:
                    image = assemble.boot_image(header, b"kernel", b"r" * 65, MAP)
                    self.assertEqual(struct.unpack_from("<I", image, 20)[0], address)
                else:
                    with self.assertRaises(ValueError):
                        assemble.boot_image(header, b"kernel", b"r" * 65, MAP)

    def test_header_only_requires_consistent_explicit_framing_evidence(self):
        original = template()
        header, prefix = original[:2048], original[2048:2096]
        metadata = {"header_page_bytes": 2048, "header_page_sha256": assemble.sha(header),
                    "full_saved_file_bytes": 6144, "full_saved_file_sha256": assemble.sha(original),
                    "kernel_first48_hex": prefix.hex(), "kernel_first48_sha256": assemble.sha(prefix),
                    "section_bounds": {"kernel": {"offset": 2048, "end": 2145, "aligned_end": 4096},
                                       "ramdisk": {"offset": 4096, "end": 4109, "aligned_end": 6144},
                                       "second": {"offset": 6144, "size": 0},
                                       "all_sections_within_saved_file": True}}
        expected = assemble.boot_image(original, b"new", b"ramdisk", MAP)
        self.assertEqual(assemble.boot_image(header, b"new", b"ramdisk", MAP, metadata), expected)
        self.assertEqual(expected[44:48], struct.pack("<I", 0x12345678))
        with self.assertRaises(ValueError):
            assemble.boot_image(header, b"new", b"ramdisk", MAP)
        bad_prefix = prefix[:36] + b"MTK!" + prefix[40:]
        for changes in ({"header_page_sha256": "0" * 64}, {"header_page_bytes": 4096},
                        {"full_saved_file_bytes": 6143}, {"full_saved_file_sha256": "invalid"},
                        {"kernel_first48_sha256": "0" * 64},
                        {"kernel_first48_hex": bad_prefix.hex(), "kernel_first48_sha256": assemble.sha(bad_prefix)},
                        {"section_bounds": {**metadata["section_bounds"], "second": {"offset": 6145, "size": 0}}}):
            with self.subTest(changes=changes):
                with self.assertRaises(ValueError):
                    assemble.boot_image(header, b"new", b"ramdisk", MAP, {**metadata, **changes})


class BootDecisionTests(unittest.TestCase):
    def setUp(self):
        BACKUP.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=BACKUP)
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
        for directory in ("dev", "proc", "sys", "run", "newroot", "store"):
            source = source.replace("/" + directory, str(self.root / directory))
        check = self.script("check", source)
        for mode in ("ok", "rescue-only", "checksum", "extract", "-t ext4", "-t devtmpfs", "size=128m", "--move /proc"):
            with self.subTest(mode=mode):
                (self.root / "events").write_text("")
                path.write_bytes(archive if mode != "checksum" else archive + b"wrong")
                if mode == "extract":
                    path.write_bytes(b"not gzip")
                    check.write_text(source.replace(hashlib.sha256(archive).hexdigest(), hashlib.sha256(b"not gzip").hexdigest()))
                else:
                    check.write_text(source)
                check.write_text(check.read_text().replace("@RESCUE_ONLY@", "1" if mode == "rescue-only" else "0"))
                result = subprocess.run(["sh", str(check)], capture_output=True, text=True,
                                        env={**os.environ, "ROOT": str(self.root), "BUSYBOX": self.busybox, "FAIL": mode}, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                events = (self.root / "events").read_text()
                self.assertEqual("SWITCH_ROOT" in events, mode == "ok", events + result.stderr)
                self.assertEqual("RESCUE" in events, mode != "ok", events)
                if mode in ("ok", "checksum", "extract", "-t ext4", "size=128m"):
                    self.assertIn("-o ro,noload", events)
                if mode == "rescue-only":
                    self.assertNotIn("-t ext4", events)
                    self.assertNotIn("size=128m", events)
                self.assertNotIn("UNEXPECTED_SLEEP", events)

    def test_usb_rescue_restarts_only_its_own_gadget_with_one_console_owner(self):
        bb = self.script("usb-bb", '''
case "$1" in
mountpoint|mount|mdev) exit 0;;
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
        for directory in ("dev", "sys", "run", "etc"):
            source = source.replace("/" + directory, str(self.root / directory))
        check = self.script("usb-check", source)
        udcs = self.root / "sys/class/udc"
        udcs.mkdir(parents=True)
        gadget = self.root / "sys/kernel/config/usb_gadget/encore-biscuit61"
        gadget.parent.mkdir(parents=True)
        (self.root / "dev/ttyGS0").touch()
        (self.root / "etc/encore").mkdir(parents=True)
        for mode in ("none", "ambiguous", "existing", "single", "dual", "ctty"):
            with self.subTest(mode=mode):
                for name in ("usb-consoles", "usb-ctty"):
                    (self.root / "etc/encore" / name).unlink(missing_ok=True)
                if mode == "dual":
                    (self.root / "etc/encore/usb-consoles").write_text("2\n")
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
                success = mode in ("single", "dual", "ctty")
                self.assertEqual(result.returncode, 88 if success else 1, result.stderr)
                self.assertEqual((self.root / "events").read_text(), "CONSOLE\n" if success else "")
                if success:
                    self.assertEqual((gadget / "UDC").read_text(), "controller-one\n")
                    self.assertEqual((gadget / "idProduct").read_text(), "0x4ee7\n")
                    self.assertTrue((gadget / "configs/c.1/acm.usb0").is_symlink())
                    self.assertEqual((gadget / "configs/c.1/acm.usb1").is_symlink(), mode == "dual")
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
        elf = bytearray(52)
        elf[:6] = b"\x7fELF\x01\x01"
        struct.pack_into("<H", elf, 18, 40)
        struct.pack_into("<HH", elf, 42, 32, 0)
        tools = ("bin/busybox.static", "usr/local/bin/wmtup", "usr/local/bin/btbridge", "usr/local/bin/encore-sendspin",
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

        result, busybox = assemble.rootfs_archive(archive(), bytes(elf) + b"new-runtime", {})
        self.assertEqual(busybox, bytes(elf))
        with tarfile.open(fileobj=io.BytesIO(result), mode="r:gz") as t:
            for name in old:
                self.assertNotIn(name, t.getnames())
            self.assertEqual(t.extractfile("etc/encore-base").read(), b"encore-biscuit-kernel61-trial-v1\n")
            self.assertIn(b"/sys/firmware/devicetree/base/idme/bt_mac_addr/value", t.extractfile("usr/local/sbin/encore-bluetooth").read())
            self.assertIn(b"exec /bin/busybox.static init", t.extractfile("sbin/init").read())
            self.assertIn(b"::once:/etc/encore/boot.sh\n", t.extractfile("etc/inittab").read())
            self.assertIn(b"::respawn:/usr/local/sbin/encore-console\n", t.extractfile("etc/inittab").read())
            self.assertNotIn(b"sysinit", t.extractfile("etc/inittab").read())
            self.assertEqual(t.extractfile("usr/local/bin/encore-satellite").read(), bytes(elf) + b"new-runtime")
        for extra in ("data/encore/config.json", "store/encore/active", "../outside"):
            with self.subTest(extra=extra):
                with self.assertRaises(ValueError):
                    assemble.rootfs_archive(archive(extra), bytes(elf), {})


if __name__ == "__main__":
    unittest.main()
