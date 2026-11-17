"""Key-only SSH contracts. All device state is disposable; never contacts ADB."""

import base64
import io
import os
import pwd
import shutil
import signal
import socket
import struct
import subprocess
import tarfile
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

import install
import package
import test_package

DROPBEAR = shutil.which("dropbear", path=os.defpath + ":/usr/sbin")


def string(value):
    return struct.pack(">I", len(value)) + value


def public_key(kind=b"ssh-ed25519", bits=3072, exponent=65537):
    def integer(value):
        b = value.to_bytes((value.bit_length() + 7) // 8, "big")
        return string((b"\0" if b[0] & 128 else b"") + b)

    body = (
        string(bytes(range(32)))
        if kind == b"ssh-ed25519"
        else integer(exponent) + integer(2 ** (bits - 1) + 1)
    )
    return kind + b" " + base64.b64encode(string(kind) + body) + b"\n"


class KeyTests(unittest.TestCase):
    def test_formats_bounds_options_injection_and_wire(self):
        for key in (public_key(), public_key(b"ssh-rsa"), public_key(b"ssh-rsa", 8192)):
            self.assertEqual(
                install.ssh_public_key(key.rstrip() + b" owner comment\n"), key
            )
        for bad in (
            b"",
            b"-----BEGIN OPENSSH PRIVATE KEY-----\n",
            b"ssh-ed25519 AAAA\n",
            public_key() + public_key(),
            public_key() + b"\n",
            public_key().replace(b"\n", b"\r\n"),
            b"restrict " + public_key(),
            b'command="reboot" ' + public_key(),
            b"x" * 2049,
            public_key(b"ssh-rsa", 2048),
            public_key(b"ssh-rsa", 8193),
            public_key(b"ssh-rsa", 3072, 2),
            public_key(b"ssh-rsa", 3072, 2**24 + 1),
        ):
            with self.assertRaises(ValueError):
                install.ssh_public_key(bad)
        key = public_key().split()
        for wire in (
            base64.b64decode(key[1]) + b"x",
            string(b"ssh-rsa") + string(bytes(32)),
            string(b"ssh-ed25519") + string(bytes(31)),
        ):
            with self.assertRaises(ValueError):
                install.ssh_public_key(key[0] + b" " + base64.b64encode(wire))


class InstallerSSHTests(unittest.TestCase):
    setUp = test_package.RecoveryTests.setUp
    bundle = test_package.RecoveryTests.bundle
    store = test_package.RecoveryTests.store

    def test_dry_run_validation_no_writes_and_update_preservation(self):
        self.bundle(1)
        self.args.ssh_public_key_file = self.root / "owner.pub"
        self.args.ssh_public_key_file.write_bytes(public_key().rstrip() + b" comment\n")
        self.args.ssh_enable = True
        self.args.apply = False
        before = {str(p): p.read_bytes() for p in self.device.rglob("*") if p.is_file()}
        install.install(self.args)
        self.assertEqual(
            before,
            {str(p): p.read_bytes() for p in self.device.rglob("*") if p.is_file()},
        )
        self.assertFalse(self.args.backup_dir.exists())
        for bad in (b"PRIVATE KEY", public_key() + public_key()):
            self.args.ssh_public_key_file.write_bytes(bad)
            with patch.object(install, "ADB") as adb, self.assertRaises(ValueError):
                install.install(self.args)
            adb.assert_not_called()
        self.args.ssh_public_key_file.write_bytes(public_key())
        self.args.apply = True
        install.install(self.args)
        ssh = self.device / "data/encore/ssh"
        self.assertEqual((ssh / "authorized_keys").read_bytes(), public_key())
        self.assertEqual((ssh / "enabled").read_bytes(), b"1\n")
        self.assertFalse((ssh / "host_ed25519").exists())
        # Disposable identity sentinel, not private key material.
        (ssh / "host_ed25519").write_bytes(b"on-device-identity-sentinel")
        before = {p.name: p.read_bytes() for p in ssh.iterdir()}
        for filename in (
            "recovery.img",
            "misc.img",
            "recovery.img.sha256",
            "misc.img.sha256",
        ):
            self.assertEqual(
                (self.args.backup_dir / filename).stat().st_mode & 0o777, 0o600
            )
        self.assertEqual(self.store("twrp.img").read_bytes(), self.twrp)
        self.assertEqual(
            self.store("twrp.img.sha256").read_text().split()[0], package.sha(self.twrp)
        )
        self.args.command = "update"
        self.args.ssh_public_key_file = None
        self.args.ssh_enable = False
        self.store("slots/a.state").write_text("good\n")
        self.bundle(2)
        install.install(self.args)
        self.assertEqual(before, {p.name: p.read_bytes() for p in ssh.iterdir()})

    def test_enable_requires_key_and_key_does_not_imply_enable(self):
        self.bundle(1)
        self.args.ssh_enable = True
        with patch.object(install, "ADB") as adb, self.assertRaises(ValueError):
            install.install(self.args)
        adb.assert_not_called()
        self.args.ssh_enable = False
        self.args.ssh_public_key_file = self.root / "owner.pub"
        self.args.ssh_public_key_file.write_bytes(public_key())
        install.install(self.args)
        self.assertFalse((self.device / "data/encore/ssh/enabled").exists())


class PackageSSHTests(unittest.TestCase):
    def test_real_bundle_service_linkage_permissions_and_no_shipped_keys(self):
        scratch = package.ROOT / ".amp/in/orb-backup"
        scratch.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=scratch) as directory:
            root = Path(directory)
            elf = bytearray(52)
            elf[:6] = b"\x7fELF\x01\x01"
            struct.pack_into("<H", elf, 18, 40)
            struct.pack_into("<H", elf, 42, 32)
            binary = root / "binary"
            binary.write_bytes(elf)
            kernel = root / "kernel"
            kernel.mkdir()
            with tarfile.open(kernel / "source.tar.gz", "w:gz") as archive:
                item = tarfile.TarInfo("src/COPYING")
                item.size = 7
                archive.addfile(item, io.BytesIO(b"license"))
            base = root / "base.tar.gz"
            required = (
                "bin/busybox.static",
                "sbin/wpa_supplicant",
                "sbin/wpa_cli",
                "lib/apk/db/installed",
                "usr/bin/dbus-daemon",
                "usr/bin/dbus-uuidgen",
                "usr/lib/bluetooth/bluetoothd",
                "usr/sbin/dropbear",
                "usr/bin/dropbearkey",
                "usr/bin/flock",
                "usr/sbin/iptables-legacy",
                "usr/sbin/ip6tables-legacy",
                "usr/sbin/iptables-legacy-restore",
                "usr/sbin/ip6tables-legacy-restore",
            )

            def make_base(extra=()):
                with tarfile.open(base, "w:gz") as archive:
                    for name in (*required, *extra):
                        item = tarfile.TarInfo(name)
                        item.size = len(elf)
                        archive.addfile(item, io.BytesIO(elf))

            make_base()
            output = root / "bundle.zip"
            with (
                patch.object(
                    package.kernel_artifact,
                    "load",
                    return_value=(
                        {"inputs_sha256": "a" * 64},
                        test_package.kernel_fixture(),
                    ),
                ),
                patch.object(
                    package,
                    "load_sendspin",
                    return_value=({}, {"encore-sendspin": bytes(elf)}),
                ),
            ):
                package.build(base, binary, binary, binary, 1, output, kernel, root)
                import zipfile

                with (
                    zipfile.ZipFile(output) as bundle,
                    tarfile.open(
                        fileobj=io.BytesIO(bundle.read("rootfs.tar.gz")), mode="r:gz"
                    ) as archive,
                ):
                    inittab = archive.extractfile("etc/inittab").read()
                    for service in ("ssh", "network"):
                        self.assertIn(
                            f"::respawn:/usr/local/sbin/encore-{service}\n".encode(),
                            inittab,
                        )
                    self.assertEqual(
                        archive.getmember("root/.ssh/authorized_keys").linkname,
                        "/run/encore/ssh/authorized_keys",
                    )
                    self.assertEqual(archive.getmember("root").mode, 0o700)
                    self.assertEqual(archive.getmember("root/.ssh").mode, 0o700)
                    self.assertEqual(archive.getmember("etc/shadow").mode, 0o600)
                    self.assertIn(b"root:!:", archive.extractfile("etc/shadow").read())
                    self.assertEqual(
                        archive.getmember("usr/local/sbin/encore-firewall").mode, 0o755
                    )
                    self.assertFalse(
                        any(m.name.startswith("data/encore/ssh/") for m in archive)
                    )
                make_base(("etc/dropbear/dropbear_ed25519_host_key",))
                with self.assertRaisesRegex(ValueError, "host identity"):
                    package.build(base, binary, binary, binary, 1, output, kernel, root)


class SupervisorTests(unittest.TestCase):
    def setUp(self):
        scratch = package.ROOT / ".amp/in/orb-backup"
        scratch.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=scratch)
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.data, self.run = self.root / "data", self.root / "run"
        self.data.mkdir()
        self.run.mkdir()
        (self.root / "home/.ssh").mkdir(parents=True)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        # Exercise the actual BusyBox wire parser, not the host awk dialect.
        for app in ("awk", "base64", "od", "sed"):
            (self.bin / app).symlink_to(shutil.which("busybox"))
        self.env = {**os.environ, "PATH": str(self.bin) + ":" + os.environ["PATH"]}
        fake = self.root / "daemon"
        fake.write_text(
            f'#!/bin/sh\necho $$ > "{self.run}/dropbear.pid"\n'
            f"setsid sh -c 'echo $$ > \"{self.run}/session\"; sleep 600' &\nwait\n"
        )
        fake.chmod(0o755)
        firewall = self.root / "firewall"
        firewall.write_text(f'#!/bin/sh\necho "$1" >> "{self.root}/gate"\n')
        firewall.chmod(0o755)
        script = (package.HERE / "ssh").read_text()
        script = script.replace("/data/encore/ssh", str(self.data)).replace(
            "/run/encore/ssh", str(self.run)
        )
        script = script.replace(
            "while [ ! -e /run/encore/base-ready ]; do sleep 1; done", ":"
        )
        script = script.replace("/root/.ssh", str(self.root / "home/.ssh")).replace(
            '"$run" /root ', f'"$run" {self.root}/home '
        )
        script = script.replace("/usr/sbin/dropbear", str(fake)).replace(
            "/usr/local/sbin/encore-firewall", str(firewall)
        )
        self.script = self.root / "supervisor"
        self.script.write_text(script)
        (self.data / "host_ed25519").write_bytes(b"identity-sentinel")

    def wait_for(self, predicate):
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if predicate():
                return
            time.sleep(0.05)
        self.fail("supervisor state transition timed out")

    def launch(self):
        log = (self.root / "log").open("w+")
        self.addCleanup(log.close)
        proc = subprocess.Popen(
            ["sh", str(self.script)], env=self.env, stdout=log, stderr=log
        )

        def stop():
            proc.terminate()
            try:
                proc.wait(timeout=8)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()

        self.addCleanup(stop)
        return proc

    def test_default_off_invalid_keys_rotation_and_child_termination(self):
        canary = subprocess.Popen(["sleep", "60"])

        def stop_canary():
            canary.terminate()
            canary.wait(timeout=3)

        self.addCleanup(stop_canary)
        proc = self.launch()
        self.wait_for(lambda: (self.run / "stopped").exists())
        self.assertFalse((self.run / "running").exists())
        (self.data / "enabled").write_bytes(b"1\n")
        (self.data / "authorized_keys").write_bytes(b"ssh-ed25519 AAAA\n")
        time.sleep(1.2)
        self.assertFalse((self.run / "running").exists())
        (self.data / "authorized_keys").write_bytes(public_key())
        self.wait_for(
            lambda: (self.run / "running").exists() or proc.poll() is not None
        )
        self.assertIsNone(proc.poll(), (self.root / "log").read_text())
        old = (self.run / "running").read_bytes()
        self.wait_for(lambda: (self.run / "session").exists())
        child = int((self.run / "session").read_text())
        (self.data / "authorized_keys").write_bytes(
            public_key() + public_key(b"ssh-rsa")
        )
        self.wait_for(
            lambda: (
                (self.run / "authorized_keys")
                .read_bytes()
                .count(b"no-agent-forwarding")
                == 2
            )
        )
        self.assertEqual((self.run / "running").read_bytes(), old)
        self.assertEqual(
            (self.data / "host_ed25519").read_bytes(), b"identity-sentinel"
        )
        (self.data / "enabled").write_bytes(b"")
        self.wait_for(lambda: (self.run / "stopped").exists())
        self.assertFalse((self.run / "running").exists())
        state = Path(f"/proc/{child}/stat")
        self.assertTrue(
            not state.exists() or state.read_text().split(") ")[1].startswith("Z ")
        )
        self.assertEqual((self.root / "gate").read_text().splitlines()[-1], "ssh-close")
        self.assertIsNone(canary.poll(), "disabled SSH killed an unrelated process")

    def test_descendant_enumeration_never_includes_unrelated_processes(self):
        child = subprocess.Popen(["sleep", "60"])
        try:
            script = self.script.read_text()
            function = script[
                script.index("descendants() {") : script.index("stop_server() {")
            ]
            result = subprocess.run(
                ["sh", "-c", f'roots="{child.pid}";\n' + function + "\ndescendants"],
                capture_output=True,
                text=True,
                check=True,
            )
            self.assertEqual(
                [int(line.split()[0]) for line in result.stdout.splitlines()],
                [child.pid],
            )
        finally:
            child.terminate()
            child.wait(timeout=3)

    @unittest.skipUnless(
        DROPBEAR and shutil.which("ssh-keygen"), "local SSH tools required"
    )
    def test_real_dropbear_rotation_preserves_session_and_disable_terminates_it(self):
        self.real_dropbear_lifecycle()

    @unittest.skipUnless(
        DROPBEAR and shutil.which("ssh-keygen"), "local SSH tools required"
    )
    def test_listener_crash_does_not_escape_explicit_session_teardown(self):
        self.real_dropbear_lifecycle(crash_listener=True)

    def real_dropbear_lifecycle(self, crash_listener=False):
        for name in ("owner", "rotation"):
            subprocess.run(
                [
                    "ssh-keygen",
                    "-q",
                    "-t",
                    "ed25519",
                    "-N",
                    "",
                    "-f",
                    str(self.root / name),
                ],
                check=True,
                capture_output=True,
            )
        owner = install.ssh_public_key((self.root / "owner.pub").read_bytes())
        rotation = install.ssh_public_key((self.root / "rotation.pub").read_bytes())
        (self.data / "authorized_keys").write_bytes(owner)
        (self.data / "enabled").write_bytes(b"1\n")
        (self.data / "host_ed25519").unlink()
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        # Only test endpoints differ: unprivileged loopback port and private
        # authorization directory, never the orb's real home or SSH daemon.
        daemon = self.root / "daemon"
        shutil.copyfile(DROPBEAR, daemon)
        script = self.script.read_text().replace(
            f"{daemon} -F", f"{daemon} -p 127.0.0.1:{port} -D {self.run} -F"
        )
        script = script.replace('[ "$name" = dropbear ]', '[ "$name" = daemon ]')
        self.script.write_text(script)
        supervisor = self.launch()
        self.wait_for(
            lambda: (self.run / "running").exists() or supervisor.poll() is not None
        )
        self.assertIsNone(supervisor.poll(), (self.root / "log").read_text())
        identity = (self.data / "host_ed25519").read_bytes()
        listener = (self.run / "running").read_bytes()
        prefix = [
            "ssh",
            "-F",
            "/dev/null",
            "-o",
            "BatchMode=yes",
            "-o",
            "IdentitiesOnly=yes",
            "-o",
            "StrictHostKeyChecking=no",
            "-o",
            "UserKnownHostsFile=/dev/null",
            "-o",
            "GlobalKnownHostsFile=/dev/null",
            "-o",
            "LogLevel=ERROR",
            "-p",
            str(port),
        ]
        account = pwd.getpwuid(os.getuid()).pw_name + "@127.0.0.1"
        with (self.root / "session.log").open("w+") as output:
            session = subprocess.Popen(
                [
                    *prefix,
                    "-i",
                    str(self.root / "owner"),
                    account,
                    "echo connected; sleep 60",
                ],
                stdout=output,
                stderr=output,
            )
            try:
                self.wait_for(
                    lambda: (
                        "connected" in (self.root / "session.log").read_text()
                        or session.poll() is not None
                    )
                )
                self.assertIsNone(
                    session.poll(), (self.root / "session.log").read_text()
                )
                (self.data / "authorized_keys").write_bytes(owner + rotation)
                self.wait_for(
                    lambda: (
                        (self.run / "authorized_keys")
                        .read_bytes()
                        .count(b"no-agent-forwarding")
                        == 2
                    )
                )
                accepted = subprocess.run(
                    [*prefix, "-i", str(self.root / "rotation"), account, "true"],
                    capture_output=True,
                    timeout=8,
                )
                self.assertEqual(accepted.returncode, 0, accepted.stderr.decode())
                self.assertIsNone(session.poll())
                self.assertEqual((self.run / "running").read_bytes(), listener)
                self.assertEqual((self.data / "host_ed25519").read_bytes(), identity)
                if crash_listener:
                    os.kill(int(listener.split()[0]), signal.SIGKILL)
                (self.data / "enabled").write_bytes(b"")
                self.wait_for(lambda: (self.run / "stopped").exists())
                self.assertNotEqual(session.wait(timeout=8), 0)
                self.assertFalse((self.run / "running").exists())
                rejected = subprocess.run(
                    [*prefix, "-i", str(self.root / "owner"), account, "true"],
                    capture_output=True,
                    timeout=8,
                )
                self.assertNotEqual(rejected.returncode, 0)
            finally:
                if session.poll() is None:
                    session.terminate()
                    session.wait(timeout=3)

    def test_wire_validation_matches_installer(self):
        # Run just the actual function against hostile persisted state.
        script = self.script.read_text()
        function = script[
            script.index("valid_keys() {") : script.index("\nwhile :; do\n  flock -x 9")
        ]
        for key in (
            public_key(),
            public_key(b"ssh-rsa"),
            public_key(b"ssh-rsa", 8192),
            public_key(b"ssh-rsa", 2048),
            public_key(b"ssh-rsa", 8193),
            public_key() + b"\n",
            b"restrict " + public_key(),
            b"ssh-ed25519 AAAA\n",
            b"ssh-ed25519 "
            + base64.b64encode(string(b"ssh-ed25519\0") + string(bytes(32)))
            + b"\n",
        ):
            (self.data / "authorized_keys").write_bytes(key)
            result = subprocess.run(
                [
                    "sh",
                    "-c",
                    f'data="{self.data}"; run="{self.run}";\n'
                    + function
                    + "\nvalid_keys",
                ],
                env=self.env,
                capture_output=True,
            )
            try:
                install.ssh_public_key(key)
                valid = True
            except ValueError:
                valid = False
            self.assertEqual(result.returncode == 0, valid, result.stderr.decode())


@unittest.skipUnless(
    DROPBEAR and shutil.which("ssh-keygen") and shutil.which("sudo"),
    "local Dropbear/OpenSSH/sudo required",
)
class HandshakeTests(unittest.TestCase):
    def test_locked_root_key_auth_and_password_forwarding_rejection(self):
        if subprocess.run(["sudo", "-n", "true"], capture_output=True).returncode:
            self.skipTest("noninteractive sudo required for disposable chroot")
        scratch = package.ROOT / ".amp/in/orb-backup"
        scratch.mkdir(parents=True, exist_ok=True)
        # Generated keys live only inside this context and are always deleted.
        with tempfile.TemporaryDirectory(dir=scratch) as directory:
            root = Path(directory)
            # Also clean up on an early failure after root takes ownership of
            # the deliberately non-traversable test home.
            self.addCleanup(
                lambda: subprocess.run(
                    ["sudo", "-n", "rm", "-rf", str(root)],
                    check=True,
                    capture_output=True,
                )
            )
            jail = root / "jail"
            jail.mkdir()
            for directory in ("etc", "root/.ssh", "usr/sbin", "bin", "dev"):
                (jail / directory).mkdir(parents=True, exist_ok=True)

            def copy_binary(path, dest=None):
                target = jail / (dest or path).lstrip("/")
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(path, target)
                result = subprocess.run(["ldd", path], capture_output=True, text=True)
                for token in result.stdout.split():
                    if token.startswith("/") and Path(token).is_file():
                        target = jail / token.lstrip("/")
                        target.parent.mkdir(parents=True, exist_ok=True)
                        shutil.copy2(token, target)

            copy_binary(DROPBEAR, "/usr/sbin/dropbear")
            copy_binary(shutil.which("busybox"), "/bin/sh")
            (jail / "etc/passwd").write_text("root:x:0:0:root:/root:/bin/sh\n")
            (jail / "etc/shadow").write_text("root:!:0:0:99999:7:::\n")
            (jail / "etc/shells").write_text("/bin/sh\n")
            (jail / "etc/group").write_text("root:x:0:root\n")
            for name, minor in (("null", 3), ("urandom", 9)):
                subprocess.run(
                    [
                        "sudo",
                        "-n",
                        "mknod",
                        str(jail / "dev" / name),
                        "c",
                        "1",
                        str(minor),
                    ],
                    check=True,
                    capture_output=True,
                )
            for name in ("owner", "wrong"):
                subprocess.run(
                    [
                        "ssh-keygen",
                        "-q",
                        "-t",
                        "ed25519",
                        "-N",
                        "",
                        "-f",
                        str(root / name),
                    ],
                    check=True,
                    capture_output=True,
                )
            subprocess.run(
                ["dropbearkey", "-t", "ed25519", "-f", str(jail / "host")],
                check=True,
                capture_output=True,
            )
            restrictions = b"no-port-forwarding,no-agent-forwarding,no-X11-forwarding "
            (jail / "root/.ssh/authorized_keys").write_bytes(
                restrictions + install.ssh_public_key((root / "owner.pub").read_bytes())
            )
            for name in ("root", "root/.ssh"):
                (jail / name).chmod(0o700)
            (jail / "root/.ssh/authorized_keys").chmod(0o600)
            # Dropbear's strict file ownership check requires root-owned home.
            subprocess.run(
                ["sudo", "-n", "chown", "-R", "0:0", str(jail / "root")],
                check=True,
                capture_output=True,
            )
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                port = sock.getsockname()[1]
            log = (root / "server.log").open("w+")
            proc = subprocess.Popen(
                [
                    "sudo",
                    "-n",
                    "chroot",
                    str(jail),
                    "/usr/sbin/dropbear",
                    "-F",
                    "-E",
                    "-s",
                    "-g",
                    "-j",
                    "-k",
                    "-T",
                    "3",
                    "-I",
                    "300",
                    "-r",
                    "/host",
                    "-P",
                    "/pid",
                    "-p",
                    f"127.0.0.1:{port}",
                ],
                stdout=log,
                stderr=log,
            )
            try:
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline:
                    try:
                        with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                            break
                    except OSError:
                        time.sleep(0.05)
                prefix = [
                    "ssh",
                    "-F",
                    "/dev/null",
                    "-o",
                    "BatchMode=yes",
                    "-o",
                    "IdentitiesOnly=yes",
                    "-o",
                    "StrictHostKeyChecking=no",
                    "-o",
                    "UserKnownHostsFile=/dev/null",
                    "-o",
                    "GlobalKnownHostsFile=/dev/null",
                    "-o",
                    "LogLevel=ERROR",
                    "-o",
                    "ConnectTimeout=3",
                    "-p",
                    str(port),
                ]
                client_env = {
                    k: v
                    for k, v in os.environ.items()
                    if k not in ("SSH_AUTH_SOCK", "DISPLAY", "XAUTHORITY")
                }

                def ssh(identity="owner", options=(), command="printf key-ok"):
                    return subprocess.run(
                        [
                            *prefix,
                            "-i",
                            str(root / identity),
                            *options,
                            "root@127.0.0.1",
                            command,
                        ],
                        capture_output=True,
                        text=True,
                        timeout=8,
                        env=client_env,
                    )

                result = ssh()
                self.assertEqual(
                    result.returncode,
                    0,
                    result.stderr + " " + (root / "server.log").read_text(),
                )
                self.assertEqual(result.stdout, "key-ok")
                self.assertNotEqual(ssh("wrong").returncode, 0)
                result = ssh(
                    options=[
                        "-o",
                        "PubkeyAuthentication=no",
                        "-o",
                        "PreferredAuthentications=password,keyboard-interactive",
                    ]
                )
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("publickey", result.stderr)
                self.assertNotEqual(
                    ssh(
                        options=[
                            "-o",
                            "ExitOnForwardFailure=yes",
                            "-R",
                            "0:localhost:9",
                        ]
                    ).returncode,
                    0,
                )
                self.assertNotEqual(ssh(options=["-W", "localhost:9"]).returncode, 0)
                if shutil.which("ssh-agent"):
                    agent_socket = root / "agent.socket"
                    agent = subprocess.Popen(
                        ["ssh-agent", "-D", "-a", str(agent_socket)],
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL,
                    )
                    try:
                        deadline = time.monotonic() + 3
                        while not agent_socket.exists() and time.monotonic() < deadline:
                            time.sleep(0.02)
                        self.assertTrue(agent_socket.exists())
                        client_env["SSH_AUTH_SOCK"] = str(agent_socket)
                        self.assertEqual(
                            ssh(
                                options=["-A"], command='test -z "$SSH_AUTH_SOCK"'
                            ).returncode,
                            0,
                        )
                    finally:
                        agent.terminate()
                        agent.wait(timeout=3)
                        client_env.pop("SSH_AUTH_SOCK", None)
                if shutil.which("xauth"):
                    client_env.update(
                        DISPLAY=":99", XAUTHORITY=str(root / "xauthority")
                    )
                    subprocess.run(
                        [
                            "xauth",
                            "-f",
                            client_env["XAUTHORITY"],
                            "add",
                            ":99",
                            ".",
                            "0123456789abcdef0123456789abcdef",
                        ],
                        check=True,
                        capture_output=True,
                    )
                    result = ssh(
                        options=[
                            "-X",
                            "-o",
                            "ForwardX11Trusted=yes",
                            "-o",
                            "LogLevel=DEBUG1",
                        ],
                        command='test -z "$DISPLAY"',
                    )
                    self.assertEqual(result.returncode, 0)
                    self.assertIn("X11 forwarding request failed", result.stderr)
            finally:
                if (jail / "pid").exists():
                    subprocess.run(
                        [
                            "sudo",
                            "-n",
                            "kill",
                            "-TERM",
                            (jail / "pid").read_text().strip(),
                        ],
                        capture_output=True,
                    )
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.terminate()
                    proc.wait(timeout=5)
                log.close()
                # Root owns the deliberately restrictive test home, so remove
                # that subtree via sudo before TemporaryDirectory cleans up.
                subprocess.run(
                    ["sudo", "-n", "rm", "-rf", str(jail / "root")],
                    check=True,
                    capture_output=True,
                )


if __name__ == "__main__":
    unittest.main()
