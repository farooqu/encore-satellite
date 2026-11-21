#!/usr/bin/env python3
"""Fresh Biscuit Linux 6.1/AArch64 development install over recovery USB."""

import argparse
import base64
import getpass
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import secrets
import shlex
import subprocess
import sys
import tarfile
import zipfile

from package import FORMAT, boot_image, sha, validate_kernel
import kernel_artifact


def unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate field: {key}")
        result[key] = value
    return result


def load_bundle(path, digest):
    data = path.read_bytes()
    if not re.fullmatch(r"[0-9a-f]{64}", digest) or sha(data) != digest:
        raise ValueError("bundle SHA256 mismatch; use the hash from your trusted local build")
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        payload = ("initramfs.gz", "kernel.json", "kernel.zImage-dtb", "rootfs.tar.gz")
        if sorted(archive.namelist()) != sorted(["manifest.json", *payload]):
            raise ValueError("unexpected or duplicate bundle members")
        if any(m.file_size > 256 * 1024 * 1024 for m in archive.infolist()):
            raise ValueError("bundle member too large")
        manifest = json.loads(archive.read("manifest.json"), object_pairs_hook=unique)
        if (type(manifest.get("schema")) is not int or manifest["schema"] != 1
                or manifest.get("target") != "biscuit" or manifest.get("format") != FORMAT
                or manifest.get("backend") != "incubator"
                or manifest.get("channel") != "dev"
                or type(manifest.get("package_revision")) is not int or manifest["package_revision"] < 1
                or not re.fullmatch(r"[0-9a-f]{40}", manifest.get("source_commit", ""))):
            raise ValueError("not an Encore Dot 2 development bundle")
        files = {n: archive.read(n) for n in payload}
        if manifest.get("files") != {n: sha(b) for n, b in files.items()}:
            raise ValueError("bundle content checksum mismatch")
    kernel = files["kernel.zImage-dtb"]
    provenance = json.loads(files["kernel.json"], object_pairs_hook=unique)
    kernel_artifact.validate(provenance, kernel)
    validate_kernel(kernel)
    base = manifest.get("base")
    if (not isinstance(base, dict)
            or set(base) != {"userspace_sha256", "kernel_inputs_sha256", "kernel_sha256", "initramfs_sha256"}
            or any(not isinstance(v, str) or not re.fullmatch(r"[0-9a-f]{64}", v) for v in base.values())
            or base["kernel_sha256"] != sha(kernel)
            or base["kernel_inputs_sha256"] != provenance["inputs_sha256"]
            or base["initramfs_sha256"] != sha(files["initramfs.gz"])
            or manifest.get("base_sha256") != sha(json.dumps(base, sort_keys=True).encode())):
        raise ValueError("Linux base identity does not match kernel/userspace/initramfs")
    # Validate before busybox tar sees anything. Trusted local packages may contain
    # filesystem symlinks, but not paths beneath them or special device files.
    with tarfile.open(fileobj=io.BytesIO(files["rootfs.tar.gz"]), mode="r:gz") as archive:
        members = archive.getmembers()
        names = {m.name for m in members}
        links = {m.name for m in members if m.issym()}
        if len(names) != len(members) or sum(m.size for m in members) > 512 * 1024 * 1024:
            raise ValueError("duplicate paths or oversized rootfs")
        for m in members:
            p = PurePosixPath(m.name)
            if (p.is_absolute() or not m.name or ".." in p.parts or "\\" in m.name
                    or not (m.isfile() or m.isdir() or m.issym())
                    or any(str(parent) in links for parent in p.parents)):
                raise ValueError(f"unsafe rootfs entry: {m.name}")
        required = {"sbin/init", "usr/local/bin/encore-satellite", "etc/encore-base", "etc/inittab"}
        if not required <= names or archive.extractfile("etc/encore-base").read() != (FORMAT + "\n").encode():
            raise ValueError("incomplete or incompatible Encore rootfs")
    return manifest, files


def device_name(serial, requested=None):
    name = requested if requested is not None else "encore-biscuit-" + sha(serial.encode())[:6]
    if not isinstance(name, str) or not re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", name):
        raise ValueError("name must be a DNS label: lowercase letters, digits and hyphens, no leading/trailing hyphen, at most 63 characters")
    return name


def wifi_config(ssid, password):
    encoded = ssid.encode("utf-8")
    if not 1 <= len(encoded) <= 32:
        raise ValueError("SSID must contain 1..32 UTF-8 bytes")
    if not 8 <= len(password) <= 63 or any(ord(c) < 32 or ord(c) > 126 for c in password):
        raise ValueError("WPA2 passphrase must be 8..63 printable ASCII characters")
    psk = hashlib.pbkdf2_hmac("sha1", password.encode(), encoded, 4096, 32).hex()
    # Hex encoding avoids supplicant and shell injection and never persists the
    # original passphrase. The derived PSK is still a secret; mode 0600.
    return f"ctrl_interface=/run/wpa_supplicant\nupdate_config=0\nnetwork={{\n ssid={encoded.hex()}\n psk={psk}\n}}\n".encode()


class ADB:
    def __init__(self, serial):
        self.prefix = ["adb", "-s", serial]

    def shell(self, command, data=None):
        # Raw shell forwards binary stdin/EOF, unlike legacy exec-out. Recovery
        # still merges stderr and loses exit status, so require a status trailer.
        marker = "\nENCORE_STATUS_" + secrets.token_hex(16) + ":"
        script = ("sh -c " + shlex.quote("set -eu; " + command)
                  + "\nstatus=$?\nprintf " + shlex.quote(marker + "%d\n") + ' "$status"')
        result = subprocess.run([*self.prefix, "shell", "-T", "sh -c " + shlex.quote(script)],
                                input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=300)
        if result.returncode:
            # Never echo stdin: configuration and PSKs travel only there.
            raise RuntimeError("recovery operation failed: " + result.stderr.decode(errors="replace")[-1000:])
        output, found, status = result.stdout.rpartition(marker.encode())
        if not found or not re.fullmatch(rb"(?:0|[1-9][0-9]{0,2})\n", status) or int(status) > 255:
            raise RuntimeError("recovery operation returned no valid remote exit status")
        if int(status):
            raise RuntimeError(f"recovery operation failed with remote exit status {int(status)}")
        return output

    def write(self, path, data, mode="600"):
        self.shell(f"umask 077; cat > {shlex.quote(path)}; chmod {mode} {shlex.quote(path)}", data)
        if self.shell(f"sha256sum {shlex.quote(path)}").decode().split()[0] != sha(data):
            raise RuntimeError("USB write read-back checksum mismatch")


def preflight(adb, *, native=False):
    if adb.shell("id -u").strip() != b"0":
        raise ValueError("root adb in TWRP is required")
    adb.shell("test -x /sbin/recovery")
    if adb.shell("cat /proc/idme/device_type_id").strip(b"\0\r\n ") != b"A3S5BH2HU6VAYF":
        raise ValueError("this installer is only for Echo Dot 2 / biscuit")
    partitions = [("misc", 8), ("recovery", 12), ("cache", 15), ("userdata", 16)]
    if native:
        partitions += [("boot_a", 10), ("boot_b", 11)]
    for name, number in partitions:
        # TWRP can expose several aliases, and its readlink accepts one path.
        values = adb.shell(f'for path in /dev/block/platform/*/by-name/{name}; do readlink -f "$path"; done').decode().splitlines()
        if not values or any(value != f"/dev/block/mmcblk0p{number}" for value in values):
            raise ValueError(f"unexpected {name} partition layout; refusing writes")
    if native:
        for number, start in ((10, 163840), (11, 196608)):
            sysfs = f"/sys/class/block/mmcblk0p{number}"
            if adb.shell(f"cat {sysfs}/start {sysfs}/size").split() != [str(start).encode(), b"32768"]:
                raise ValueError("unexpected native boot store geometry; refusing writes")


def update_eligibility(adb, manifest, store):
    """Read-only checks, repeated under the installation lock before any apply."""
    existing = adb.shell(f"if [ -e {store}/format ]; then cat {store}/format; fi").strip().decode()
    if existing != FORMAT:
        raise ValueError("update requires an existing compatible Encore installation")
    target = adb.shell(f"cat {store}/active").strip().decode()
    if target not in ("a", "b"):
        raise ValueError("invalid Encore active slot")
    if adb.shell(f"cat {store}/slots/{target}.state").strip() != b"good":
        raise ValueError("current slot has not passed health checks; refusing to overwrite its fallback")
    old = json.loads(adb.shell(f"cat {store}/slots/{target}/etc/encore-package.json"), object_pairs_hook=unique)
    # Include withdrawn/failed trial builds, not just the active revision.
    highest = int(adb.shell(f"cat {store}/highest-revision").strip())
    if any(old.get(k) != manifest.get(k) for k in ("target", "backend", "channel")):
        raise ValueError("ordinary updates must preserve target, installed backend and channel; package replacement/migration is not implemented")
    if (old.get("format") != FORMAT or old.get("base_sha256") != manifest.get("base_sha256")
            or manifest["package_revision"] <= highest):
        raise ValueError("incompatible Linux base (kernel/userspace/initramfs) or non-increasing package revision; base migration is not an app-slot update")
    image = adb.shell(f"cat {store}/linux.img")
    saved_hash = adb.shell(f"cat {store}/linux.img.sha256").decode().split()[0]
    if sha(image) != saved_hash:
        raise ValueError("saved Encore recovery image is damaged; stay in TWRP")
    return ("b" if target == "a" else "a"), image


def install(args):
    # Validate before even read-only device operations, including dry-run.
    starter = args.command in ("install-arm64", "transition-arm64")
    if starter:
        from kernel61 import starter_install
        manifest, rootfs, image = starter_install.load_bundle(args.bundle, args.sha256)
    else:
        manifest, files = load_bundle(args.bundle, args.sha256)
    fresh = args.command == "install"
    adb = ADB(args.serial)
    # Recovery stages the bundled Linux environment, not its own kernel/BlueZ.
    preflight(adb)
    recovery = None
    if not starter:
        image = None
    if fresh:
        recovery = adb.shell("cat /dev/block/mmcblk0p12")
        image = boot_image(recovery, files["initramfs.gz"], files["kernel.zImage-dtb"])
    name = device_name(args.serial, args.name) if fresh or starter else None
    print(f"{args.command}: Echo Dot 2, development revision {manifest['package_revision']}"
          + (f", name {name}" if name else ", preserve Encore identity/Wi-Fi/pairing"))
    print("No bootloader, boot_a/b, system or factory partition writes; no saved Wi-Fi imported.")
    if starter:
        # Also runs on apply, before mounting anything writable or making backups.
        starter_install.check_stock_firmware(adb)
    if not args.apply:
        if starter:
            for mount, number in (("/cache", 15), ("/data", 16)):
                source = adb.shell(f"awk '$2 == \"{mount}\" && $3 == \"ext4\" {{print $1}}' /proc/mounts").decode().strip()
                if not source or adb.shell(f"readlink -f {shlex.quote(source)}").strip() != f"/dev/block/mmcblk0p{number}".encode():
                    print(f"{mount}: p{number} not already mounted; capacity and state inventory unchecked")
                    continue
                free = int(adb.shell(f"df -Pk {mount} | awk 'END {{print $4}}'"))
                state = adb.shell(f"if test -L {mount}/encore; then echo symlink; "
                                  f"elif test -d {mount}/encore; then echo directory; "
                                  f"elif test -e {mount}/encore; then echo unexpected; else echo absent; fi").decode().strip()
                print(f"{mount}: {free} KiB free; existing Encore state: {state}")
                if args.command == "install-arm64" and state != "absent":
                    raise ValueError(f"fresh install requires {mount}/encore to be absent")
            print("No persistent device writes performed. Apply rechecks firmware, capacity, state and recovery backup.")
            return
        if not fresh:
            # Do not mount even read-only: ext4 can replay its journal. Only
            # inspect an already-mounted, verified cache filesystem.
            source = adb.shell("awk '$2 == \"/cache\" && $3 == \"ext4\" {print $1}' /proc/mounts").decode().strip()
            if source and adb.shell(f"readlink -f {shlex.quote(source)}").strip() == b"/dev/block/mmcblk0p15":
                update_eligibility(adb, manifest, "/cache/encore")
                print("Read-only update eligibility passed; all checks will be repeated under lock before apply.")
            else:
                print("UNCHECKED update eligibility: cache is not already accessible on the expected ext4 partition. "
                      "Installation format/existence, active slot validity/health, target/backend/channel, "
                      "Linux base compatibility, highest revision/anti-rollback and saved recovery image integrity "
                      "have NOT been checked. Apply mounts storage and checks all of these under lock.")
                print("Device/bundle preflight only; update eligibility is unknown. No device writes.")
                return
        print("Read-only preflight passed. No device writes; add --apply --allow-development to perform this operation.")
        return
    if not args.allow_development:
        raise ValueError("unsigned local bundles require explicit --allow-development; this is not a production update channel")
    # TWRP remains in RAM. Mount only the two filesystems we own directories on.
    for mount, number in (("/cache", 15), ("/data", 16)):
        adb.shell(f"mountpoint -q {mount} || mount {mount}; "
                  f"source=$(awk '$2 == \"{mount}\" && $3 == \"ext4\" && $4 ~ /^rw(,|$)/ {{print $1}}' /proc/mounts); "
                  f"test \"$(readlink -f \"$source\")\" = /dev/block/mmcblk0p{number}")
    store = "/cache/encore"
    # Serialize the preconditions too: another PC must not change active after
    # we select an inactive target but before we acquire the write lock.
    if args.command == "install-arm64":
        # A lock outside the target directory lets the installer require that
        # the installation directory is absent, including after interruption.
        lock = "/cache/.encore-install-lock"
        store_check = f"test ! -e {store}; test ! -L {store}"
    else:
        lock = f"{store}/.install-lock"
        store_check = f"test -d {store}; test ! -L {store}" if starter else f"mkdir -p {store}"
    adb.shell(f"{store_check}; umask 077; mkdir {lock}")
    try:
        if starter:
            starter_install.install_locked(adb, args, manifest, rootfs, image, name)
        else:
            adb.shell(f"test ! -e {store}/.arm64-transition")
            apply_locked(args, adb, manifest, files, store, name, recovery, image)
    finally:
        adb.shell(f"rmdir {lock}")


def apply_locked(args, adb, manifest, files, store, name, recovery, image):
    fresh = args.command == "install"
    existing = adb.shell(f"if [ -e {store}/format ]; then cat {store}/format; fi").strip().decode()
    if fresh and existing:
        raise ValueError("Encore already exists; use update to preserve it (no implicit factory reset)")
    if not fresh and existing != FORMAT:
        raise ValueError("update requires an existing compatible Encore installation")
    if fresh:
        if adb.shell("test ! -e /data/encore && echo clean").strip() != b"clean":
            raise ValueError("/data/encore already exists; refusing to erase it")
        if adb.shell("cat /dev/block/mmcblk0p12") != recovery:
            raise ValueError("recovery changed after preflight; inspect it before retrying")
        # Default to first-boot Improv. USB credentials are an explicit alternative,
        # never a migration of Fire OS state.
        wifi = wifi_config(args.wifi_ssid, getpass.getpass("Wi-Fi WPA2 passphrase: ")) if args.wifi_ssid is not None else None
        config = json.dumps({"schema": 1, "name": name, "bluetooth_proxy": True}).encode()
        # Keep actual TWRP and misc backups, not personal Fire OS settings.
        args.backup_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        if args.backup_dir.is_symlink():
            raise ValueError("backup directory must not be a symlink")
        args.backup_dir.chmod(0o700)
        for filename, data in (("recovery.img", recovery), ("misc.img", adb.shell("cat /dev/block/mmcblk0p8"))):
            path = args.backup_dir / filename
            with path.open("xb") as out:
                os.chmod(path, 0o600)
                out.write(data)
                out.flush()
                os.fsync(out.fileno())
            with (args.backup_dir / (filename + ".sha256")).open("xb") as out:
                os.chmod(out.name, 0o600)
                out.write((sha(data) + "\n").encode())
                out.flush()
                os.fsync(out.fileno())
        backup_fd = os.open(args.backup_dir, os.O_RDONLY)
        try:
            os.fsync(backup_fd)
        finally:
            os.close(backup_fd)
        # Only firmware from this unit or explicitly provided local files. No
        # requirement to boot/restore Fire OS; missing blobs produce an actionable error.
        if args.firmware_dir:
            firmware = {p.name: p.read_bytes() for p in args.firmware_dir.iterdir() if p.is_file()}
        else:
            raw = adb.shell("for d in /system_root/system/vendor/firmware /system/vendor/firmware "
                            "/cache/techo5/firmware; do if [ -s $d/WIFI_RAM_CODE_8163 ]; then "
                            "tar -cf - -C $d .; exit; fi; done; exit 1")
            with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
                firmware = {PurePosixPath(m.name).name: archive.extractfile(m).read()
                            for m in archive if m.isfile()}
        if (not firmware.get("WMT_SOC.cfg") or not firmware.get("WIFI_RAM_CODE_8163")
                or not any(n.endswith("_hdr.bin") and len(b) >= 28 for n, b in firmware.items())):
            raise ValueError("unit Wi-Fi firmware incomplete; provide --firmware-dir with WMT_SOC.cfg, WIFI_RAM_CODE_8163 and ROM *_hdr.bin patches")
        key = base64.b64encode(secrets.token_bytes(32)) + b"\n"
        with (args.backup_dir / "encore-api-key.txt").open("xb") as out:
            os.chmod(out.name, 0o600)
            out.write(key)
        target = "a"
    else:
        target, image = update_eligibility(adb, manifest, store)
    adb.shell(f"mkdir -p {store}/slots")
    try:
        stage = store + "/.staging"
        adb.shell(f"rm -rf {stage}; mkdir {stage}")
        adb.write(stage + "/rootfs.tar.gz", files["rootfs.tar.gz"])
        # Invalidate fallback state BEFORE replacing its bytes. Active slot is
        # changed last; a power cut leaves the old active good slot untouched.
        adb.shell(f"echo bad > {store}/slots/{target}.state; sync; "
                  f"rm -rf {store}/slots/{target}; mkdir {store}/slots/{target}; "
                  f"tar -xzf {stage}/rootfs.tar.gz -C {store}/slots/{target}; "
                  f"test -x {store}/slots/{target}/usr/local/bin/encore-satellite")
        adb.write(f"{store}/slots/{target}/etc/encore-package.json", json.dumps(manifest).encode())
        if fresh:
            adb.shell(f"mkdir -p {store}/firmware /data/encore; chmod 700 /data/encore")
            for filename, data in firmware.items():
                if not re.fullmatch(r"[A-Za-z0-9_.-]+", filename):
                    raise ValueError("invalid firmware filename")
                adb.write(f"{store}/firmware/{filename}", data)
            adb.write("/data/encore/config.json", config)
            adb.write("/data/encore/noise.psk", key)
            if wifi is not None:
                adb.write("/data/encore/wpa_supplicant.conf", wifi)
            adb.write(f"{store}/twrp.img", recovery)
            adb.write(f"{store}/twrp.img.sha256", f"{sha(recovery)}  twrp.img\n".encode())
            adb.write(f"{store}/linux.img", image)
            adb.write(f"{store}/linux.img.sha256", f"{sha(image)}  linux.img\n".encode())
            # Start both slots from the same clean payload; neither is marked
            # healthy until it boots. No claim either is known-good yet.
            adb.shell(f"mkdir {store}/slots/b; cp -a {store}/slots/a/. {store}/slots/b/; "
                      f"echo 'trial 2' > {store}/slots/b.state")
            adb.write(store + "/format", (FORMAT + "\n").encode())
        adb.write(store + "/highest-revision", (str(manifest["package_revision"]) + "\n").encode())
        adb.shell(f"echo 'trial 2' > {store}/slots/{target}.state; sync; "
                  f"echo {target} > {store}/active.new; sync; mv {store}/active.new {store}/active; sync")
        # Updating from TWRP restores the SAME saved Encore recovery image, not
        # a new kernel or initramfs supplied by the application update.
        # Recovery dd can disable conv; flush with the checked sync below.
        adb.shell(f"dd if={store}/linux.img of=/dev/block/mmcblk0p12 bs=4096; sync")
        if sha(adb.shell(f"head -c {len(image)} /dev/block/mmcblk0p12")) != sha(image):
            raise RuntimeError("recovery flash read-back failed; keep TWRP running and restore the host backup")
        adb.shell(f"rm -rf {stage}; sync")
    finally:
        adb.shell(f"rm -rf {stage}; sync")
    print("Staged and read-back checked. Reboot to recovery to boot Encore; hardware acceptance is still required.")
    # No automatic reboot: the USB recovery connection is the user's escape path.


def install_native(args):
    # The same full receipt check used before GitHub release retention. No
    # locally hashed or unsigned seed can reach the native installer.
    sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "release"))
    from pipeline import native_starter, verify
    from kernel61 import starter_install

    receipt = verify(args.candidate, args.public_key, args.receipt_sha256)
    if receipt["target"] != "biscuit" or not native_starter(receipt):
        raise ValueError("signed native Biscuit candidate required")
    artifact = receipt["artifact"]
    manifest, files = starter_install.load_native_bundle(args.candidate / artifact["name"], artifact["sha256"])
    name = device_name(args.serial, args.name)
    adb = ADB(args.serial)
    preflight(adb, native=True)
    starter_install.check_stock_firmware(adb)
    recovery = adb.shell("sha256sum /dev/block/mmcblk0p12").decode().split()[0]
    if recovery != args.twrp_sha256:
        raise ValueError("current full recovery partition does not match the independently reviewed TWRP hash")
    if not args.apply:
        print(f"Read-only signed native preflight passed for {name}. Apply checks empty state and retains full backup before writes.")
        return
    for mount, number in (("/cache", 15), ("/data", 16)):
        adb.shell(f"mountpoint -q {mount} || mount {mount}; "
                  f"source=$(awk '$2 == \"{mount}\" && $3 == \"ext4\" && $4 ~ /^rw(,|$)/ {{print $1}}' /proc/mounts); "
                  f"test \"$(readlink -f \"$source\")\" = /dev/block/mmcblk0p{number}")
    lock = "/cache/.encore-install-lock"
    adb.shell(f"umask 077; mkdir {lock}")
    try:
        starter_install.install_native_locked(adb, args, manifest, files, name)
    finally:
        adb.shell(f"rmdir {lock}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(required=True)
    p = commands.add_parser("install", help="fresh recovery-based Linux 6.1/AArch64 installation")
    p.set_defaults(command="install-arm64")
    p.add_argument("--serial", required=True, help="exact adb device serial (no implicit device selection)")
    p.add_argument("--bundle", type=Path, required=True)
    p.add_argument("--sha256", required=True, help="out-of-band hash from your trusted local build")
    p.add_argument("--apply", action="store_true", help="perform device writes; default is read-only preflight")
    p.add_argument("--allow-development", action="store_true", help="acknowledge unsigned local development build")
    p.add_argument("--name", help="new DNS device name; no old configuration is imported")
    p.add_argument("--twrp-sha256", required=True, help="SHA256 of the independently verified full original recovery partition")
    p.add_argument("--backup-dir", type=Path, required=True, help="new private local directory for recovery backups")
    p = commands.add_parser("install-native", help="fresh signed native A/B installation through TWRP USB")
    p.set_defaults(command="install-native")
    p.add_argument("--serial", required=True)
    p.add_argument("--candidate", type=Path, required=True, help="verified GitHub signed candidate directory")
    p.add_argument("--receipt-sha256", required=True, help="independently reviewed receipt JSON SHA256")
    p.add_argument("--public-key", type=Path, required=True, help="pinned Ed25519 public root PEM")
    p.add_argument("--twrp-sha256", required=True, help="independently verified full original recovery SHA256")
    p.add_argument("--backup-dir", type=Path, required=True, help="new private rollback backup directory")
    p.add_argument("--name", help="new DNS device name")
    p.add_argument("--apply", action="store_true", help="perform device writes; otherwise read-only preflight")
    args = parser.parse_args()
    try:
        (install_native if args.command == "install-native" else install)(args)
    except (ValueError, KeyError, OSError, RuntimeError, subprocess.SubprocessError, zipfile.BadZipFile, tarfile.TarError) as error:
        parser.exit(1, f"Encore provisioning failed: {error}\n")


if __name__ == "__main__":
    main()
