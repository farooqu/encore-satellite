"""Development bundle/provisioning contracts, without physical device claims."""

import gzip
import io
import json
import os
from pathlib import Path
import re
import struct
import subprocess
import tarfile
import tempfile
import unittest
import zipfile
from argparse import Namespace
from contextlib import redirect_stdout
from unittest.mock import patch

import install
import package
import kernel_artifact


def kernel_fixture(bluetooth=True, padding=0, board=b"biscuit"):
    config = b'CONFIG_ARCH_MTK_PROJECT="biscuit"\n'
    config += b"CONFIG_BT=y\nCONFIG_BT_HCIVHCI=y\n" if bluetooth else b"# CONFIG_BT is not set\n"
    payload = gzip.compress(b"vmlinux:IKCFG_ST" + gzip.compress(config, mtime=0) + b"IKCFG_ED", mtime=0) + b"x" * padding
    header = bytearray(48)
    struct.pack_into("<I", header, 36, 0x016f2818)
    struct.pack_into("<I", header, 44, len(header) + len(payload))
    trees = b"".join(struct.pack(">II", 0xd00dfeed, 40 + len(value)) + b"\0" * 32 + value
                     for value in (board + b"-evt", board + b"-min-evt"))
    return bytes(header) + payload + trees


def kernel_provenance(image):
    return {"schema": 1, "target": "echo-dot-2", "inputs": kernel_artifact.inputs(),
            "inputs_sha256": kernel_artifact.identity(), "hardware_qualified": False,
            "files": {"zImage-dtb": package.sha(image), "zImage-dtb.config": "1" * 64,
                      "zImage-dtb.System.map": "2" * 64, "source.tar.gz": "3" * 64}}


def write_bundle(path, rootfs, ramdisk, revision, userspace="b" * 64, target="echo-dot-2", kernel=None):
    kernel = kernel_fixture() if kernel is None else kernel
    record = kernel_provenance(kernel)
    files = {"rootfs.tar.gz": rootfs, "initramfs.gz": ramdisk, "kernel.zImage-dtb": kernel,
             "kernel.json": json.dumps(record).encode()}
    base = {"userspace_sha256": userspace, "kernel_inputs_sha256": record["inputs_sha256"],
            "kernel_sha256": package.sha(kernel), "initramfs_sha256": package.sha(ramdisk)}
    manifest = {"schema": 1, "target": target, "backend": "incubator", "format": "encore-dot-2-v1", "channel": "dev",
                "source_commit": "a" * 40, "package_revision": revision, "base": base,
                "base_sha256": package.sha(json.dumps(base, sort_keys=True).encode()),
                "files": {n: package.sha(b) for n, b in files.items()}}
    with zipfile.ZipFile(path, "w") as archive:
        archive.writestr("manifest.json", json.dumps(manifest))
        for name, data in files.items():
            archive.writestr(name, data)
    return package.sha(path.read_bytes())


class ProvisioningTests(unittest.TestCase):
    def test_default_custom_and_invalid_names(self):
        self.assertRegex(install.device_name("unit-one"), r"^encore-dot-[0-9a-f]{6}$")
        self.assertEqual(install.device_name("unit-one"), install.device_name("unit-one"))
        self.assertNotEqual(install.device_name("unit-one"), install.device_name("unit-two"))
        self.assertEqual(install.device_name("unit-one", "office-speaker"), "office-speaker")
        for name in ("", "Office Speaker", "-office", "office-", "office;reboot", "a" * 64):
            with self.subTest(name=name), self.assertRaises(ValueError):
                install.device_name("serial", name)

    def test_wifi_escaping_and_known_wpa_vector(self):
        # Published WPA PBKDF2 test vector, not an expectation from our helper.
        config = install.wifi_config("IEEE", "password").decode()
        self.assertIn("psk=f42c6fc52df0ebef9ebb4b90b38a5f902e83fe1b135a70e23aed762e9710a12e", config)
        self.assertIn("ssid=49454545", config)
        injected = install.wifi_config('a"\nnetwork={', "long password").decode()
        self.assertEqual(injected.count("network={"), 1)
        for ssid, password in (("", "password"), ("é" * 17, "password"), ("ok", "short"), ("ok", "bad\npassword")):
            with self.assertRaises(ValueError):
                install.wifi_config(ssid, password)

    def test_boot_image_preserves_kernel_addresses_and_removes_android_root(self):
        page = 2048
        kernel = kernel_fixture()
        header = bytearray(page)
        header[:8] = b"ANDROID!"
        struct.pack_into("<9I", header, 8, len(kernel), 0x40008000, 9, 0x44000000, 0, 0, 0x40000100, page, 0)
        command = b'console=tty0 root=/dev/old skip_initramfs dm="system none ro,0 1 android-verity" androidboot.serialno=example'
        header[64:64 + len(command)] = command
        original = bytes(header) + kernel.ljust(page, b"\0") + b"oldramdis".ljust(page, b"\0")
        result = package.boot_image(original, b"new ramdisk")
        self.assertEqual(result[page:page+len(kernel)], kernel)
        self.assertEqual(struct.unpack_from("<I", result, 20)[0], 0x44000000)
        self.assertEqual(struct.unpack_from("<I", result, 16)[0], 11)
        self.assertEqual(result[2*page:2*page+11], b"new ramdisk")
        self.assertEqual(result[64:576].split(b"\0")[0], b"console=tty0 androidboot.serialno=example")
        for invalid in (b"", original[:3000], original.replace(b"ANDROID!", b"INVALID!", 1)):
            with self.assertRaises(ValueError):
                package.boot_image(invalid, b"replacement")

    def test_replacement_kernel_requires_embedded_ble_config_and_identical_device_trees(self):
        old = kernel_fixture(bluetooth=False)
        new = kernel_fixture(padding=4096)
        header = bytearray(2048)
        header[:8] = b"ANDROID!"
        struct.pack_into("<9I", header, 8, len(old), 0x40008000, 3, 0x44000000, 0, 0, 0x40000100, 2048, 0)
        recovery = bytes(header) + old.ljust(2048, b"\0") + b"old".ljust(2048, b"\0")
        image = package.boot_image(recovery, b"new init", new)
        self.assertEqual(struct.unpack_from("<I", image, 8)[0], len(new))
        self.assertEqual(struct.unpack_from("<I", image, 12)[0], 0x40008000)
        self.assertEqual(image[2048:2048 + len(new)], new)
        ramdisk_start = 2048 + (len(new) + 2047) // 2048 * 2048
        self.assertEqual(image[ramdisk_start:ramdisk_start + 8], b"new init")
        for invalid in (old, new[:-1], kernel_fixture(board=b"radar"), b"\x7fELF" + b"\0" * 100):
            with self.subTest(kernel=invalid[:48]), self.assertRaises(ValueError):
                package.boot_image(recovery, b"new init", invalid)


class SlotTests(unittest.TestCase):
    def setUp(self):
        backup = package.ROOT / ".amp/in/orb-backup"
        backup.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=backup)
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = {**os.environ, "STORE": str(self.root), "BOOTED": str(self.root / "booted")}
        (self.root / "format").write_text("encore-dot-2-v1\n")
        for slot in ("a", "b"):
            init = self.root / "slots" / slot / "sbin/init"
            init.parent.mkdir(parents=True)
            init.write_text("#!/bin/sh\n")
            init.chmod(0o755)
        (self.root / "active").write_text("b\n")
        (self.root / "slots/a.state").write_text("good\n")
        (self.root / "slots/b.state").write_text("trial 2\n")

    def run_slot(self, command, success=True):
        result = subprocess.run(["sh", str(package.HERE / "slotctl"), command],
                                env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0 if success else 1, result.stderr)
        return result.stdout.strip()

    def test_two_trial_boots_then_old_good_slot(self):
        self.assertEqual(self.run_slot("next"), "b")
        self.assertEqual(self.run_slot("next"), "b")
        self.assertEqual(self.run_slot("next"), "a")
        self.assertEqual((self.root / "slots/b.state").read_text(), "bad\n")
        self.assertEqual(self.run_slot("next"), "a")

    def test_health_commit_and_both_initial_slots_fail_to_rescue(self):
        self.assertEqual(self.run_slot("next"), "b")
        (self.root / "booted").write_text("a\n")
        self.run_slot("commit", False)  # stale health callback cannot commit another slot
        (self.root / "booted").write_text("b\n")
        self.run_slot("commit")
        self.assertEqual((self.root / "slots/b.state").read_text(), "good\n")
        for _ in range(3):
            self.assertEqual(self.run_slot("next"), "b")
        (self.root / "slots/a.state").write_text("trial 2\n")
        (self.root / "slots/b.state").write_text("trial 0\n")
        self.assertEqual(self.run_slot("next"), "a")
        self.assertEqual(self.run_slot("next"), "a")
        self.run_slot("next", False)


class BundleTests(unittest.TestCase):
    def test_build_embeds_verified_project_kernel_and_rejects_damaged_or_wrong_inputs(self):
        backup = package.ROOT / ".amp/in/orb-backup"
        backup.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=backup) as directory:
            root = Path(directory)
            artifact = root / "kernel"
            artifact.mkdir()
            image = kernel_fixture()
            record = kernel_provenance(image)
            source = io.BytesIO()
            with tarfile.open(fileobj=source, mode="w:gz") as archive:
                license = tarfile.TarInfo("src/COPYING")
                license.size = len(b"kernel license")
                archive.addfile(license, io.BytesIO(b"kernel license"))
            for name, data in (("zImage-dtb", image), ("zImage-dtb.config", b"config"),
                               ("zImage-dtb.System.map", b"symbols"), ("source.tar.gz", source.getvalue())):
                (artifact / name).write_bytes(data)
                record["files"][name] = package.sha(data)
            (artifact / "provenance.json").write_text(json.dumps(record))
            elf = bytearray(52)
            elf[:6] = b"\x7fELF\x01\x01"
            struct.pack_into("<H", elf, 18, 40)
            struct.pack_into("<H", elf, 42, 32)
            binary = root / "runtime"
            binary.write_bytes(elf)
            music = root / "sendspin"
            (music / "notices").mkdir(parents=True)
            hardfloat = bytearray(elf)
            struct.pack_into("<I", hardfloat, 36, 0x400)
            (music / "encore-sendspin").write_bytes(hardfloat)
            (music / "source.tar.gz").write_bytes(source.getvalue())
            (music / "notices/README.txt").write_text("source and license notices")
            integration = package.ROOT / "platforms/linuxsendspin"
            inputs = [integration / "build.py", integration / "dependencies.lock.json", integration / "bridge_linux.go",
                      package.HERE / "sendspin.sh", *sorted(p for p in (integration / "native").rglob("*") if p.is_file())]
            music_record = {
                "schema": 1, "target": "armv7",
                "dependencies": json.loads((integration / "dependencies.lock.json").read_bytes()),
                "integration_sha256": {str(p.relative_to(package.ROOT)): package.sha(p.read_bytes()) for p in inputs},
                "artifacts": {str(p.relative_to(music)): package.sha(p.read_bytes()) for p in music.rglob("*") if p.is_file()},
            }
            (music / "provenance.json").write_text(json.dumps(music_record))
            base = root / "base.tar.gz"
            with tarfile.open(base, "w:gz") as archive:
                for name in ("bin/busybox.static", "sbin/wpa_supplicant", "sbin/wpa_cli", "lib/apk/db/installed",
                             "usr/bin/dbus-daemon", "usr/bin/dbus-uuidgen", "usr/lib/bluetooth/bluetoothd",
                             "usr/sbin/dropbear", "usr/bin/dropbearkey", "usr/bin/flock",
                             "usr/sbin/iptables-legacy", "usr/sbin/ip6tables-legacy",
                             "usr/sbin/iptables-legacy-restore", "usr/sbin/ip6tables-legacy-restore"):
                    item = tarfile.TarInfo(name)
                    item.size = len(elf)
                    archive.addfile(item, io.BytesIO(elf))
            bundle = root / "bundle.zip"
            with patch("time.localtime", return_value=(2026, 1, 1, 0, 0, 0, 3, 1, -1)):
                package.build(base, binary, binary, binary, 7, bundle, artifact, music)
            first_build = bundle.read_bytes()
            with patch("time.localtime", return_value=(2027, 6, 2, 12, 34, 56, 2, 153, -1)):
                package.build(base, binary, binary, binary, 7, bundle, artifact, music)
            self.assertEqual(bundle.read_bytes(), first_build,
                             "identical package inputs must not depend on the build clock")
            manifest, files = install.load_bundle(bundle, package.sha(bundle.read_bytes()))
            self.assertEqual(files["kernel.zImage-dtb"], image)
            self.assertEqual(manifest["base"]["kernel_sha256"], package.sha(image))
            with tarfile.open(fileobj=io.BytesIO(files["rootfs.tar.gz"]), mode="r:gz") as archive:
                self.assertEqual(json.load(archive.extractfile("etc/encore-kernel.json")), record)
                self.assertEqual(archive.extractfile("usr/share/licenses/encore/LICENSE").read(),
                                 (package.ROOT / "LICENSE").read_bytes())
                self.assertEqual(archive.extractfile("usr/share/licenses/encore/Linux-COPYING").read(), b"kernel license")
                self.assertIn(b"Apache License", archive.extractfile("usr/share/licenses/encore/mixednet/LICENSE-APACHE-2.0.txt").read())
                self.assertIn(b"KISS FFT", archive.extractfile("usr/share/licenses/encore/mwwfront/NOTICE").read())
                self.assertIn(b"Mark Borgerding", archive.extractfile("usr/share/licenses/encore/Techo5-NOTICE.txt").read())
                self.assertEqual(archive.extractfile("usr/local/bin/encore-sendspin").read(), hardfloat)
                self.assertEqual(json.load(archive.extractfile("etc/encore-sendspin.json")), music_record)
            (music / "encore-sendspin").write_bytes(hardfloat + b"corrupt")
            with self.assertRaisesRegex(ValueError, "Sendspin artifact checksum"):
                package.build(base, binary, binary, binary, 8, bundle, artifact, music)
            (music / "encore-sendspin").write_bytes(hardfloat)
            music_record["target"] = "host"
            (music / "provenance.json").write_text(json.dumps(music_record))
            with self.assertRaisesRegex(ValueError, "Sendspin artifact inputs"):
                package.build(base, binary, binary, binary, 8, bundle, artifact, music)
            music_record["target"] = "armv7"
            (music / "provenance.json").write_text(json.dumps(music_record))
            for name in kernel_artifact.FILES:
                original = (artifact / name).read_bytes()
                (artifact / name).write_bytes(original + b"corrupt")
                with self.subTest(member=name), self.assertRaisesRegex(ValueError, "checksum|provenance"):
                    package.build(base, binary, binary, binary, 8, bundle, artifact, music)
                (artifact / name).write_bytes(original)
            record["inputs"]["lock"]["recipe"]["commit"] = "0" * 40
            (artifact / "provenance.json").write_text(json.dumps(record))
            with self.assertRaisesRegex(ValueError, "provenance"):
                package.build(base, binary, binary, binary, 8, bundle, artifact, music)

    def test_accepts_matching_bundle_rejects_tamper_wrong_target_and_unsafe_tar(self):
        backup = package.ROOT / ".amp/in/orb-backup"
        backup.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=backup) as directory:
            path = Path(directory) / "bundle.zip"

            def bundle(target="echo-dot-2", extra=None):
                buf = io.BytesIO()
                with tarfile.open(fileobj=buf, mode="w") as archive:
                    for name, data in [("sbin/init", b"init"), ("usr/local/bin/encore-satellite", b"binary"),
                                       ("etc/encore-base", b"encore-dot-2-v1\n"), ("etc/inittab", b"init"), *([] if extra is None else [extra])]:
                        info = tarfile.TarInfo(name)
                        info.size = len(data)
                        archive.addfile(info, io.BytesIO(data))
                return write_bundle(path, gzip.compress(buf.getvalue()), b"initramfs", 3, target=target)

            digest = bundle()
            manifest, _ = install.load_bundle(path, digest)
            self.assertEqual(manifest["package_revision"], 3)
            with self.assertRaises(ValueError):
                install.load_bundle(path, "0" * 64)
            for target, extra in (("android-generic", None), ("echo-dot-2", ("../escape", b"bad")),
                                  ("echo-dot-2", ("/absolute", b"bad")), ("echo-dot-2", ("etc/inittab", b"duplicate"))):
                digest = bundle(target, extra)
                with self.assertRaises(ValueError):
                    install.load_bundle(path, digest)

            # Even internally consistent digests cannot excuse a kernel without BLE.
            digest = bundle()
            with zipfile.ZipFile(path) as archive:
                rootfs = archive.read("rootfs.tar.gz")
            digest = write_bundle(path, rootfs, b"initramfs", 3, kernel=kernel_fixture(bluetooth=False))
            with self.assertRaisesRegex(ValueError, "Bluetooth/VHCI"):
                install.load_bundle(path, digest)
            digest = bundle()
            with zipfile.ZipFile(path) as archive:
                payload = {name: archive.read(name) for name in archive.namelist()}
            metadata = json.loads(payload["manifest.json"])
            metadata["backend"] = "assist"
            payload["manifest.json"] = json.dumps(metadata).encode()
            with zipfile.ZipFile(path, "w") as archive:
                for name, data in payload.items():
                    archive.writestr(name, data)
            with self.assertRaisesRegex(ValueError, "development bundle"):
                install.load_bundle(path, package.sha(path.read_bytes()))
            metadata["backend"] = "incubator"
            metadata["base"]["kernel_sha256"] = "0" * 64
            metadata["base_sha256"] = package.sha(json.dumps(metadata["base"], sort_keys=True).encode())
            payload["manifest.json"] = json.dumps(metadata).encode()
            with zipfile.ZipFile(path, "w") as archive:
                for name, data in payload.items():
                    archive.writestr(name, data)
            with self.assertRaisesRegex(ValueError, "Linux base identity"):
                install.load_bundle(path, package.sha(path.read_bytes()))


class RecoveryTests(unittest.TestCase):
    """Run the public installer and real shell/tar/dd against private regular files.

    This exercises transaction ordering and preservation, not kernel/flash behavior.
    Only adb transport and credential input are substituted.
    """

    def setUp(self):
        backup = package.ROOT / ".amp/in/orb-backup"
        backup.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=backup)
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.device = self.root / "device"
        for directory in ("cache", "data", "dev/block/platform/test/by-name", "proc/idme", "sbin", "tools",
                          "sys/class/bluetooth", "sys/class/misc/vhci"):
            (self.device / directory).mkdir(parents=True)
        (self.device / "sys/class/misc/vhci/dev").write_text("10:137\n")
        for name, number in (("misc", 8), ("recovery", 12), ("cache", 15), ("userdata", 16)):
            partition = self.device / f"dev/block/mmcblk0p{number}"
            partition.write_bytes(b"original misc" if name == "misc" else b"")
            (self.device / f"dev/block/platform/test/by-name/{name}").symlink_to(partition)
        header = bytearray(2048)
        header[:8] = b"ANDROID!"
        kernel = kernel_fixture()
        struct.pack_into("<9I", header, 8, len(kernel), 0x40008000, 7, 0x44000000, 0, 0, 0x40000100, 2048, 0)
        self.twrp = bytes(header) + kernel.ljust(2048, b"\0") + b"ramdisk".ljust(2048, b"\0")
        (self.device / "dev/block/mmcblk0p12").write_bytes(self.twrp)
        (self.device / "proc/idme/device_type_id").write_bytes(b"A3S5BH2HU6VAYF\0")
        (self.device / "proc/mounts").write_text("".join(
            f"{self.device}/dev/block/mmcblk0p{number} {self.device}/{mount} ext4 rw 0 0\n"
            for mount, number in (("cache", 15), ("data", 16))))
        for filename, script in (("sbin/recovery", "exit 0"), ("tools/mountpoint", "exit 0"), ("tools/id", "echo 0")):
            p = self.device / filename
            p.write_text("#!/bin/sh\n" + script + "\n")
            p.chmod(0o755)
        self.firmware = self.root / "firmware"
        self.firmware.mkdir()
        for filename in ("WMT_SOC.cfg", "WIFI_RAM_CODE_8163", "ROMv1_hdr.bin"):
            (self.firmware / filename).write_bytes(b"unit firmware" * 3)
        self.args = Namespace(command="install", serial="test-unit", name="kitchen-dot",
                              apply=True, allow_development=True, wifi_ssid="new-network",
                              backup_dir=self.root / "private-backup", firmware_dir=self.firmware,
                              bundle=self.root / "bundle.zip")
        self.fail_extract = False
        owner = self

        class RecoveryADB(install.ADB):
            def shell(self, command, data=None):
                # A partial extraction destroys only the selected target. Raising
                # after this command tests that it was invalidated before writes.
                interrupted = owner.fail_extract and "tar -xzf" in command
                if interrupted:
                    command = command.split("tar -xzf")[0] + "touch /cache/encore/slots/b/partial"
                command = re.sub(r"(?<![\w/.-])(/cache|/data|/dev/block|/sbin/recovery|/proc/idme|/proc/mounts|/sys/class)",
                                 lambda m: str(owner.device) + m[0], command)
                result = subprocess.run(["sh", "-ec", command], input=data, capture_output=True,
                                        env={**os.environ, "PATH": str(owner.device / "tools") + ":" + os.environ["PATH"]})
                if result.returncode or interrupted:
                    raise RuntimeError("simulated recovery failure")
                return result.stdout.replace(str(owner.device).encode() + b"/dev/block", b"/dev/block")

        self.addCleanup(patch.stopall)
        patch.object(install, "ADB", RecoveryADB).start()
        patch.object(install.getpass, "getpass", return_value="fresh-password").start()

    def bundle(self, revision, base="b" * 64, kernel=None):
        buf = io.BytesIO()
        with tarfile.open(fileobj=buf, mode="w") as archive:
            for name, data in (("sbin/init", b"#!/bin/sh\n"), ("usr/local/bin/encore-satellite", f"runtime-{revision}".encode()),
                               ("etc/encore-base", b"encore-dot-2-v1\n"), ("etc/inittab", b"init")):
                info = tarfile.TarInfo(name)
                info.size, info.mode = len(data), 0o755
                archive.addfile(info, io.BytesIO(data))
        self.args.sha256 = write_bundle(self.args.bundle, gzip.compress(buf.getvalue()),
                                        b"stable-initramfs", revision, base, kernel=kernel)

    def store(self, path):
        return self.device / "cache/encore" / path

    def test_clean_install_and_alternating_updates_preserve_identity_and_good_slot(self):
        self.bundle(1)
        self.args.apply = False
        install.install(self.args)
        self.assertFalse(self.store("").exists())  # default preflight makes no writes
        self.args.apply = True
        install.install(self.args)
        self.assertEqual((self.args.backup_dir / "recovery.img").read_bytes(), self.twrp)
        self.assertEqual(self.store("twrp.img").read_bytes(), self.twrp)
        self.assertEqual(self.store("slots/a.state").read_text(), "trial 2\n")
        self.assertEqual(self.store("slots/b.state").read_text(), "trial 2\n")
        config = self.device / "data/encore/config.json"
        self.assertEqual(json.loads(config.read_bytes()), {"schema": 1, "name": "kitchen-dot"})
        key = self.device / "data/encore/noise.psk"
        self.assertEqual(key.read_bytes(), (self.args.backup_dir / "encore-api-key.txt").read_bytes())
        self.assertEqual(key.stat().st_mode & 0o777, 0o600)
        preserved = {p.name: p.read_bytes() for p in config.parent.iterdir()}
        for path in (config, config.parent / "wpa_supplicant.conf"):
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        with self.assertRaises(ValueError):
            install.install(self.args)  # no implicit factory reset
        (self.device / "sys/class/misc/vhci/dev").unlink()  # update staging recovery need not provide BLE
        self.args.command = "update"
        self.bundle(2)
        with self.assertRaises(ValueError):
            install.install(self.args)  # no healthy slot yet
        self.store("slots/a.state").write_text("good\n")
        active_manifest = self.store("slots/a/etc/encore-package.json")
        original_manifest = active_manifest.read_bytes()
        for field, wrong in (("backend", "assist"), ("channel", "stable")):
            record = json.loads(original_manifest)
            record[field] = wrong
            active_manifest.write_text(json.dumps(record))
            with self.assertRaisesRegex(ValueError, "preserve target, installed backend and channel"):
                install.install(self.args)
            self.assertEqual(self.store("active").read_text(), "a\n")
            self.assertEqual(self.store("slots/b.state").read_text(), "trial 2\n")
        active_manifest.write_bytes(original_manifest)
        for revision, old, target in ((2, "a", "b"), (3, "b", "a")):
            self.store(f"slots/{old}.state").write_text("good\n")
            old_payload = self.store(f"slots/{old}/usr/local/bin/encore-satellite").read_bytes()
            self.bundle(revision)
            install.install(self.args)
            self.assertEqual(self.store("active").read_text(), target + "\n")
            self.assertEqual(self.store(f"slots/{old}.state").read_text(), "good\n")
            self.assertEqual(self.store(f"slots/{old}/usr/local/bin/encore-satellite").read_bytes(), old_payload)
            self.assertEqual(self.store(f"slots/{target}/usr/local/bin/encore-satellite").read_bytes(), f"runtime-{revision}".encode())
            self.assertEqual({p.name: p.read_bytes() for p in config.parent.iterdir()}, preserved)
            self.store(f"slots/{target}.state").write_text("good\n")
            with self.assertRaises(ValueError):
                install.install(self.args)  # replay/downgrade is not an update
        self.assertEqual((self.device / "dev/block/mmcblk0p8").read_bytes(), b"original misc")
        self.assertEqual((self.device / "dev/block/mmcblk0p12").read_bytes(), self.store("linux.img").read_bytes())

    def test_interrupted_update_never_selects_or_leaves_partial_fallback_good(self):
        self.bundle(1)
        install.install(self.args)
        self.store("slots/a.state").write_text("good\n")
        self.store("slots/b.state").write_text("good\n")
        original = self.store("slots/a/usr/local/bin/encore-satellite").read_bytes()
        self.args.command = "update"
        self.bundle(2, base="c" * 64)
        with self.assertRaises(ValueError):
            install.install(self.args)
        self.assertEqual(self.store("slots/b.state").read_text(), "good\n")
        self.bundle(2)
        self.fail_extract = True
        with self.assertRaises(RuntimeError):
            install.install(self.args)
        self.assertEqual(self.store("active").read_text(), "a\n")
        self.assertEqual(self.store("slots/a.state").read_text(), "good\n")
        self.assertEqual(self.store("slots/a/usr/local/bin/encore-satellite").read_bytes(), original)
        self.assertEqual(self.store("slots/b.state").read_text(), "bad\n")
        self.assertFalse(self.store(".install-lock").exists())
        self.assertFalse(self.store(".staging").exists())

    def test_default_ble_setup_has_no_wifi_migration_or_live_recovery_bluetooth_requirement(self):
        self.bundle(1)
        self.args.wifi_ssid = None
        self.args.name = None
        (self.device / "sys/class/misc/vhci/dev").unlink()
        install.install(self.args)
        self.assertFalse((self.device / "data/encore/wpa_supplicant.conf").exists())
        config = json.loads((self.device / "data/encore/config.json").read_bytes())
        self.assertEqual(config["name"], install.device_name("test-unit"))
        self.assertFalse(config.get("bluetooth_proxy", False))

    def test_stock_recovery_installs_bundled_kernel_without_live_bluetooth(self):
        replacement = kernel_fixture(padding=4096)
        self.bundle(1, kernel=replacement)
        old_kernel = kernel_fixture(bluetooth=False)
        header = bytearray(self.twrp[:2048])
        struct.pack_into("<I", header, 8, len(old_kernel))
        self.twrp = bytes(header) + old_kernel.ljust(2048, b"\0") + b"ramdisk".ljust(2048, b"\0")
        partition = self.device / "dev/block/mmcblk0p12"
        partition.write_bytes(self.twrp)
        (self.device / "sys/class/misc/vhci/dev").unlink()
        self.args.apply = False
        install.install(self.args)
        self.assertFalse(self.store("").exists())
        self.assertEqual(partition.read_bytes(), self.twrp)
        self.args.apply = True
        install.install(self.args)
        self.assertEqual(self.store("twrp.img").read_bytes(), self.twrp)
        self.assertEqual((self.args.backup_dir / "recovery.img").read_bytes(), self.twrp)
        image = partition.read_bytes()
        size = struct.unpack_from("<I", image, 8)[0]
        self.assertEqual(image[2048:2048+size], replacement)
        self.assertEqual(image, self.store("linux.img").read_bytes())

    def test_app_update_rejects_kernel_change_without_touching_good_slots_or_recovery(self):
        self.bundle(1)
        install.install(self.args)
        self.store("slots/a.state").write_text("good\n")
        before = {str(p.relative_to(self.store(""))): p.read_bytes()
                  for p in self.store("").rglob("*") if p.is_file()}
        recovery = (self.device / "dev/block/mmcblk0p12").read_bytes()
        self.args.command = "update"
        self.bundle(2, kernel=kernel_fixture(padding=4096))
        with self.assertRaisesRegex(ValueError, "incompatible Linux base"):
            install.install(self.args)
        self.assertEqual({str(p.relative_to(self.store(""))): p.read_bytes()
                          for p in self.store("").rglob("*") if p.is_file()}, before)
        self.assertEqual((self.device / "dev/block/mmcblk0p12").read_bytes(), recovery)

    def test_update_dry_run_checks_accessible_storage_and_never_writes(self):
        self.bundle(1)
        self.args.command, self.args.apply = "update", False
        with self.assertRaisesRegex(ValueError, "existing compatible Encore"):
            install.install(self.args)
        self.assertFalse(self.store("").exists())
        self.args.command, self.args.apply = "install", True
        install.install(self.args)
        self.args.command, self.args.apply = "update", False
        self.bundle(2)
        with self.assertRaisesRegex(ValueError, "has not passed health"):
            install.install(self.args)
        self.store("slots/a.state").write_text("good\n")
        before = {str(p): p.read_bytes() for p in self.device.rglob("*") if p.is_file()}
        with redirect_stdout(io.StringIO()) as output:
            install.install(self.args)
        self.assertIn("update eligibility passed", output.getvalue())
        self.assertEqual({str(p): p.read_bytes() for p in self.device.rglob("*") if p.is_file()}, before)
        self.bundle(1)
        with self.assertRaisesRegex(ValueError, "non-increasing"):
            install.install(self.args)
        self.bundle(2, base="c" * 64)
        with self.assertRaisesRegex(ValueError, "incompatible Linux base"):
            install.install(self.args)
        self.bundle(2)
        self.store("linux.img").write_bytes(b"damaged")
        with self.assertRaisesRegex(ValueError, "recovery image is damaged"):
            install.install(self.args)

    def test_unmounted_update_dry_run_enumerates_unchecked_eligibility(self):
        self.bundle(2)
        self.args.command, self.args.apply = "update", False
        (self.device / "proc/mounts").write_text("")
        with redirect_stdout(io.StringIO()) as output:
            install.install(self.args)
        for text in ("UNCHECKED", "active slot", "backend/channel", "Linux base", "highest revision", "recovery image integrity"):
            self.assertIn(text, output.getvalue())
        self.assertNotIn("preflight passed", output.getvalue())
        self.assertFalse(self.store("").exists())

    def test_apply_rechecks_eligibility_under_lock_after_successful_dry_run(self):
        self.bundle(1)
        install.install(self.args)
        self.store("slots/a.state").write_text("good\n")
        self.args.command, self.args.apply = "update", False
        self.bundle(2)
        install.install(self.args)
        original = install.update_eligibility

        def changed_after_dry_run(adb, manifest, store):
            self.assertTrue(self.store(".install-lock").is_dir())
            self.store("highest-revision").write_text("2\n")
            return original(adb, manifest, store)

        self.args.apply = True
        with patch.object(install, "update_eligibility", side_effect=changed_after_dry_run):
            with self.assertRaisesRegex(ValueError, "non-increasing"):
                install.install(self.args)
        self.assertEqual(self.store("active").read_text(), "a\n")
        self.assertFalse(self.store(".staging").exists())


if __name__ == "__main__":
    unittest.main()
