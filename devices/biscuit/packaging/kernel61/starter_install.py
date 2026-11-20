"""Verify and stage a held Biscuit Linux 6.1/AArch64 installation."""

import base64
import gzip
import hashlib
import io
import json
import os
import re
import secrets
import struct
import tarfile
import zipfile
import zlib
from pathlib import Path, PurePosixPath

from kernel61.arm64_assemble import boot_image, initramfs, FPGA_SHA256, STARTER_FORMAT, TEMPLATE_SHA256
from kernel61.arm64_probe import BUSYBOX_SHA256, native_elf
from package import FORMAT as LEGACY, sha

FORMAT = STARTER_FORMAT
BASE_SHA = "a8386b120851db6bf30cd6a2a354d605d3c76ea83e62b18c5549079a9b373fbe"
CA_SHA = "5961c0a3c65459931c023e0075c09733f09aea4ca439cb85bc4a08bb4f3f568b"
IMAGE_SHA = "3214a7bea1c369b34e8cae6b2cd97f2eb37cda6c6242b984fedc94337ad23604"
DTB_SHA = "27f2ee89b2c1cb787fe8c88a5555da03d22f5a7f9b5669135cd83dd0aab4f312"
P12 = "/dev/block/mmcblk0p12"
STORE = "/cache/encore"
STATE = "/data/encore"
STOCK_FIRMWARE = Path(__file__).with_name("stock-firmware")
NATIVE_TARGET = "biscuit-native-ab"
NATIVE_FORMAT = "encore-biscuit-native-ab-v1"
NATIVE_LAYOUT = hashlib.sha256(NATIVE_FORMAT.encode()).hexdigest()
NATIVE_FILES = {"boot_a.img": 16 << 20, "boot_b.img": 16 << 20, "rootfs.tar.gz": 96 << 20}


def native_identity(core, revision):
    starter_version(core, revision)
    return {"schema": 1, "target": NATIVE_TARGET, "backend": "incubator", "format": NATIVE_FORMAT,
            "update_channels": True, "base_sha256": NATIVE_LAYOUT, "channel": "stable",
            "core_version": core, "package_revision": revision}


def native_members(path, limits):
    with zipfile.ZipFile(path) as bundle:
        if sorted(bundle.namelist()) != sorted(limits) or any(
                not 0 < m.file_size <= limits[m.filename] for m in bundle.infolist()):
            raise ValueError("invalid native bundle members or size")
        return {name: bundle.read(name) for name in limits}


def load_native_bundle(path, digest):
    """Read the exact first-install payload, not an OTA or recovery ZIP."""
    with path.open("rb") as stream:
        if hashlib.file_digest(stream, "sha256").hexdigest() != digest:
            raise ValueError("native seed does not match the reviewed SHA256")
        files = native_members(stream, {**NATIVE_FILES, "manifest.json": 1 << 20, "header.bin": 2048})
    manifest = json.loads(files.pop("manifest.json"))
    header = files.pop("header.bin")
    identity = native_identity(manifest["core_version"], manifest["package_revision"])
    if (set(manifest) != set(identity) | {"input_lock_sha256", "kernel_image_sha256", "dtb_sha256", "files"}
            or any(manifest[name] != value for name, value in identity.items())
            or manifest["files"] != {name: sha(data) for name, data in files.items()}
            or any(not re.fullmatch(r"[a-f0-9]{64}", manifest[n]) for n in
                   ("input_lock_sha256", "kernel_image_sha256", "dtb_sha256"))):
        raise ValueError("invalid native seed identity or member checksums")
    rootfs = files["rootfs.tar.gz"]
    with tarfile.open(fileobj=io.BytesIO(rootfs), mode="r:gz") as archive:
        def read(name, limit):
            entry = archive.getmember(name)
            if not entry.isfile() or not 0 < entry.size <= limit:
                raise ValueError("invalid native rootfs member: " + name)
            return archive.extractfile(entry).read()

        busybox = read("bin/busybox.static", 4 << 20)
        fpga = read("lib/firmware/i2s_to_spi_v34.bin", 32768)
        verifier = read("usr/local/bin/arm64-verify", 16 << 20)
        native_elf(verifier, static=True)
        if sha(busybox) != BUSYBOX_SHA256 or sha(fpga) != FPGA_SHA256 or read("etc/encore-arm64-version", 256) != (
                starter_version(identity["core_version"], identity["package_revision"]) + "\n").encode():
            raise ValueError("wrong native runtime identity or immutable firmware")
    for slot in "ab":
        boot = files[f"boot_{slot}.img"]
        if len(boot) < 2048 or boot[:8] != b"ANDROID!":
            raise ValueError("invalid native boot envelope")
        kernel_len, ka, ramdisk_len, ra = struct.unpack_from("<4I", boot, 8)
        offset = 2048 + ((kernel_len + 2047) // 2048) * 2048
        if not (65536 < kernel_len and ka == 0x40000000 and ra == 0x46000000
                and ramdisk_len > 0 and offset + ramdisk_len <= len(boot)
                and len(boot) == offset + ((ramdisk_len + 2047) // 2048) * 2048):
            raise ValueError("unexpected native boot footprint")
        payload = boot[2048:2048 + kernel_len]
        decoder = zlib.decompressobj(31)
        image = decoder.decompress(payload, 28 << 20)
        trailer = decoder.unused_data
        if not decoder.eof or len(trailer) != 65536 or trailer[:4] != b"\xd0\x0d\xfe\xed":
            raise ValueError("invalid native kernel/DTB payload")
        dtb = trailer[:struct.unpack_from(">I", trailer, 4)[0]]
        expected = gzip.decompress(initramfs(busybox, sha(rootfs), fpga, native=(slot, identity, verifier)))
        ramdisk = boot[offset:offset + ramdisk_len]
        decoder = zlib.decompressobj(31)
        if (sha(image) != manifest["kernel_image_sha256"] or sha(dtb) != manifest["dtb_sha256"]
                or decoder.decompress(ramdisk, len(expected) + 1) != expected or not decoder.eof or decoder.unused_data
                or boot != boot_image(header, image, dtb, ramdisk, compressed_image=payload[:-65536])):
            raise ValueError("native boot image does not bind the intended runtime and slot")
    return manifest, files


def install_native_locked(adb, args, manifest, files, name):
    """First-install only. Never move, import or erase an old enrollment."""
    store = STORE + "/native-ab-v1"
    next_data = "/data/.encore-native-next"
    adb.shell(f'for p in {STORE} {STATE} {next_data}; do test ! -e "$p"; test ! -L "$p"; done')
    for mount, required in (("/cache", (2 * len(files["rootfs.tar.gz"]) >> 10) + 8192), ("/data", 8192)):
        if int(adb.shell(f"df -Pk {mount} | awk 'END {{print $4}}'")) < required:
            raise ValueError(f"insufficient space for native install on {mount}")
    paths = {"boot_a": "/dev/block/mmcblk0p10", "boot_b": "/dev/block/mmcblk0p11",
             "recovery": P12, "misc": "/dev/block/mmcblk0p8"}
    backups = {name: adb.shell(f"cat {partition}") for name, partition in paths.items()}
    misc = backups["misc"]
    if (any(len(backups[slot]) != 16 << 20 for slot in ("boot_a", "boot_b", "recovery"))
            or sha(backups["recovery"]) != args.twrp_sha256 or len(misc) < 1024
            or misc[:32] != bytes(32) or misc[0x360:0x365] != b"\0ABB\1"):
        raise ValueError("unexpected recovery backup, normal-boot command or Amazon boot control")
    args.backup_dir.mkdir(mode=0o700)
    for filename, data in [*((f"{key}.img", data) for key, data in backups.items()),
                           ("SHA256SUMS", "".join(f"{sha(data)}  {key}.img\n" for key, data in backups.items()).encode())]:
        path = args.backup_dir / filename
        with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as out:
            out.write(data)
            out.flush()
            os.fsync(out.fileno())
        if sha(path.read_bytes()) != sha(data):
            raise RuntimeError("host rollback backup read-back failed")
    for directory in (args.backup_dir, args.backup_dir.parent):
        fd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    adb.shell(f"umask 077; mkdir -p {store}/slots/a {store}/slots/b; mkdir {next_data}")
    for slot in "ab":
        adb.write(f"{store}/slots/{slot}/rootfs.tar.gz", files["rootfs.tar.gz"])
    adb.write(f"{store}/highest-revision", f"{manifest['package_revision']}\n".encode())
    adb.write(f"{next_data}/config.json", json.dumps({"schema": 1, "name": name}).encode() + b"\n")
    adb.shell(f"mv {next_data} {STATE}; sync")
    # Keep the live USB recovery session open until all readbacks pass.
    # Start with B, so A remains stock during the first boot-store write.
    for slot in ("boot_b", "boot_a"):
        image = files[slot + ".img"]
        partition = paths[slot]
        expected = sha(image + backups[slot][len(image):])
        if adb.shell(f"sha256sum {partition}").decode().split()[0] != sha(backups[slot]):
            raise RuntimeError(f"{slot} changed since backup; stay in recovery")
        adb.shell(f"dd of={partition} bs=2048; sync", image)
        if adb.shell(f"sha256sum {partition}").decode().split()[0] != expected:
            raise RuntimeError(f"{slot} readback failed; stay in recovery and restore verified backups")
    if adb.shell(f"sha256sum {P12}").decode().split()[0] != args.twrp_sha256:
        raise RuntimeError("recovery changed; do not reboot")
    # Both payloads are durable. Select A, seed B as last-good fallback, and
    # write only the slot-control sector. eMMC readback also covers its neighbors.
    after = misc[:0x365] + bytes((0x8f, 0x8e)) + misc[0x367:]
    sector = f"{store}/.install-control-sector"
    if after != misc:
        adb.write(sector, after[512:1024])
    if adb.shell(f"cat {paths['misc']}") != misc:
        raise RuntimeError("MISC changed during install; do not reboot")
    if after != misc:
        adb.shell(f"dd if={sector} of={paths['misc']} bs=512 seek=1 count=1; sync")
        if adb.shell(f"cat {paths['misc']}") != after:
            raise RuntimeError("native boot control readback failed; do not reboot")
        adb.shell(f"rm {sector}; sync")
    print("Both seed slots staged and verified. Recovery unchanged. No Wi-Fi or HA pairing imported; no reboot performed.")


def boot_compatibility(image, dtb):
    # Do not allow runtime-only OTA across a different recovery kernel or DTB.
    # Bump the domain if the recovery/runtime protocol changes incompatibly.
    return sha(b"encore-biscuit-recovery-v1\0" + bytes.fromhex(sha(image) + sha(dtb)))


def starter_version(core, revision):
    if (not isinstance(core, str) or len(core) > 128 or not re.fullmatch(
            r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?", core)
            or any(p.isdigit() and len(p) > 1 and p.startswith("0")
                   for p in core.partition("+")[0].partition("-")[2].split("."))
            or type(revision) is not int or not 0 < revision < 2**63):
        raise ValueError("expected core SemVer and a positive signed-64-bit revision")
    return f"{core} {revision}"


def check_stock_firmware(adb):
    result = adb.shell("if sh -s check 2>&1; then echo ENCORE_STOCK_OK; else echo ENCORE_STOCK_FAILED; fi",
                       STOCK_FIRMWARE.read_bytes()).decode(errors="replace").splitlines()
    if not result or result[-1] != "ENCORE_STOCK_OK":
        details = [line for line in result if line.startswith("Stock radio firmware: ")]
        raise ValueError(details[-1] if details else "could not verify the stock radio firmware in TWRP")
    print("Verified the supported stock radio firmware in system_a and system_b (read-only).")


def load_bundle(path, digest):
    data = path.read_bytes()
    if sha(data) != digest:
        raise ValueError("starter bundle SHA256 does not match the reviewed local build")
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        limits = {"manifest.json": 1 << 20, "rootfs.tar.gz": 96 << 20,
                  "recovery.img": 16 << 20, "header.bin": 2048}
        if sorted(archive.namelist()) != sorted(limits) or any(
                m.file_size > limits[m.filename] for m in archive.infolist()):
            raise ValueError("invalid starter bundle members or size")
        manifest = json.loads(archive.read("manifest.json"))
        rootfs, recovery, header = (archive.read(n) for n in ("rootfs.tar.gz", "recovery.img", "header.bin"))
    recovery_rearm = manifest.get("recovery_rearm", False)
    if (manifest.get("format") != FORMAT or manifest.get("backend") != "incubator"
            or type(manifest.get("package_revision")) is not int or manifest["package_revision"] < 1
            or type(manifest.get("api_key_provisioning", False)) is not bool
            or type(recovery_rearm) is not bool or (recovery_rearm and "core_version" not in manifest)
            or manifest.get("files") != {"rootfs.tar.gz": sha(rootfs), "recovery.img": sha(recovery),
                                         "header.bin": TEMPLATE_SHA256}):
        raise ValueError("invalid ARM64 starter manifest")
    signed = None
    with tarfile.open(fileobj=io.BytesIO(rootfs), mode="r:gz") as archive:
        entries = {}
        size = 0
        for member in archive:
            p = PurePosixPath(member.name)
            if (member.name != str(p) or str(p) == "." or p.is_absolute() or ".." in p.parts
                    or member.name in entries
                    or not (member.isfile() or member.isdir() or member.issym())
                    or (p.parts[0] in ("dev", "data", "store", "run", "tmp") and not member.isdir())):
                raise ValueError(f"unsafe starter rootfs entry: {member.name}")
            entries[member.name] = member
            size += member.size
            if size > 96 << 20:
                raise ValueError("starter rootfs exceeds RAM budget")
        for name in entries:
            if any(str(p) in entries and not entries[str(p)].isdir() for p in PurePosixPath(name).parents):
                raise ValueError("non-directory parent in starter rootfs")

        def read(name):
            if name not in entries or not entries[name].isfile():
                raise ValueError("missing regular starter rootfs file: " + name)
            return archive.extractfile(entries[name]).read()

        if read("etc/encore-base") != (FORMAT + "\n").encode():
            raise ValueError("wrong starter rootfs identity")
        if read("usr/local/sbin/encore-stock-firmware") != STOCK_FIRMWARE.read_bytes():
            raise ValueError("starter stock firmware policy differs from installer")
        for name in ("encore-satellite", "wmtup", "btbridge"):
            native_elf(read("usr/local/bin/" + name), static=True)
        for name in ("sbin/init", "etc/inittab", "etc/encore/boot.sh", "usr/local/sbin/slotctl",
                     "usr/local/sbin/runtime-service", "usr/local/sbin/encore-health"):
            read(name)
        busybox, fpga = read("bin/busybox.static"), read("lib/firmware/i2s_to_spi_v34.bin")
        if sha(busybox) != BUSYBOX_SHA256 or sha(fpga) != FPGA_SHA256:
            raise ValueError("unexpected BusyBox or microphone firmware")
        if "core_version" in manifest:
            core, revision = manifest["core_version"], manifest["package_revision"]
            if read("etc/encore-arm64-version") != (starter_version(core, revision) + "\n").encode():
                raise ValueError("starter version marker mismatch")
            if sha(read("etc/ssl/certs/ca-certificates.crt")) != CA_SHA:
                raise ValueError("unexpected ARM64 base or CA bundle")
            verifier = read("usr/local/bin/arm64-verify")
            native_elf(verifier, static=True)
            signed = (core, revision, verifier)
    # Reconstruct the entire recovery image, not just its header and outer digest.
    # The ramdisk must be the starter policy for this exact rootfs and retained kernel.
    if len(recovery) < 2048 or recovery[:8] != b"ANDROID!":
        raise ValueError("invalid starter recovery header")
    kernel_len = struct.unpack_from("<I", recovery, 8)[0]
    if not 65536 < kernel_len <= len(recovery) - 2048:
        raise ValueError("invalid starter kernel length")
    payload = recovery[2048:2048 + kernel_len]
    decoder = zlib.decompressobj(31)
    image = decoder.decompress(payload, 28 << 20)
    if not decoder.eof or len(decoder.unused_data) != 65536:
        raise ValueError("missing appended starter DTB")
    # The DTB's own length includes its trailing zero bytes.
    dtb = decoder.unused_data[:struct.unpack_from(">I", decoder.unused_data, 4)[0]]
    if "input_lock_sha256" in manifest:
        compatibility = boot_compatibility(image, dtb)
        if (signed is None or not isinstance(manifest["input_lock_sha256"], str)
                or not re.fullmatch(r"[a-f0-9]{64}", manifest["input_lock_sha256"])
                or manifest.get("kernel_image_sha256") != sha(image) or manifest.get("dtb_sha256") != sha(dtb)
                or manifest.get("retained_base_sha256") != compatibility):
            raise ValueError("invalid locked-build boot identity")
        signed += (compatibility,)
    elif sha(image) != IMAGE_SHA or sha(dtb) != DTB_SHA or (signed and manifest.get("retained_base_sha256") != BASE_SHA):
        raise ValueError("starter requires the exact retained stpfix1 Image and DTB")
    ramdisk_offset = 2048 + ((kernel_len + 2047) // 2048) * 2048
    ramdisk_size = struct.unpack_from("<I", recovery, 16)[0]
    ramdisk = recovery[ramdisk_offset:ramdisk_offset + ramdisk_size]
    expected = gzip.decompress(initramfs(busybox, sha(rootfs), fpga, starter=True, signed=signed,
                                         recovery_rearm=recovery_rearm))
    decoder = zlib.decompressobj(31)
    if (decoder.decompress(ramdisk, len(expected) + 1) != expected or not decoder.eof or decoder.unused_data
            or recovery != boot_image(header, image, dtb, ramdisk, compressed_image=payload[:-65536])):
        raise ValueError("starter recovery does not match the audited boot envelope and rootfs")
    return manifest, rootfs, recovery


def install_locked(adb, args, manifest, rootfs, recovery, name):
    revision = manifest["package_revision"]
    fresh = args.command == "install-arm64"
    archive = f"archive-arm32-before-{revision}"
    old = f"{STORE}/{archive}"
    old_state = f"/data/encore-{archive}"
    pending = f"{STORE}/.arm64-transition"
    next_data = "/data/.encore-arm64-next"
    # No resume guessing. If interrupted, use the retained archive/host backups
    # under TWRP; do not open or reuse partly published state automatically.
    if fresh:
        adb.shell(f"for p in {STORE} {STATE} {next_data}; do test ! -e \"$p\"; test ! -L \"$p\"; done")
    else:
        adb.shell(f"for p in {STORE} {STORE}/slots {STORE}/firmware; do test -d \"$p\"; test ! -L \"$p\"; done; "
                  f"for p in {old} {old_state} {pending} {next_data}; do test ! -e \"$p\"; test ! -L \"$p\"; done; "
                  f"test ! -L {STATE}; if test -e {STATE}; then test -d {STATE}; fi; "
                  f"test -s {STORE}/firmware/WMT_SOC.cfg; test -s {STORE}/firmware/WIFI_RAM_CODE_8163; "
                  f"set -- {STORE}/firmware/*_hdr.bin; test -s \"$1\"; "
                  f"for p in format active highest-revision twrp.img twrp.img.sha256 linux.img linux.img.sha256; do "
                  f"test -f {STORE}/\"$p\"; test ! -L {STORE}/\"$p\"; done")
        if adb.shell(f"cat {STORE}/format").strip().decode() != LEGACY:
            raise ValueError("transition requires existing legacy Encore; not an update or retry")
        if adb.shell(f"cat {STORE}/active").strip() not in (b"a", b"b"):
            raise ValueError("invalid legacy active slot")
        if revision <= int(adb.shell(f"cat {STORE}/highest-revision")):
            raise ValueError("starter revision must exceed the installed high-water mark")
    store_bytes = 2 * len(rootfs) + len(recovery)
    if fresh:
        store_bytes += 16 << 20
    for mount, required in (("/cache", (store_bytes + 1023) // 1024 + 8192), ("/data", 8192)):
        free = int(adb.shell(f"df -Pk {mount} | awk 'END {{print $4}}'"))
        if free < required:
            raise ValueError(f"insufficient free space on {mount}: {free} KiB, need {required} KiB")
    current = adb.shell(f"cat {P12}")
    if len(current) != 16 << 20:
        raise ValueError("unexpected recovery partition size")
    twrp = current if fresh else adb.shell(f"cat {STORE}/twrp.img")
    if sha(twrp) != args.twrp_sha256 or len(twrp) != 16 << 20:
        raise ValueError("saved TWRP differs from the independently verified backup")
    if not fresh:
        linux = adb.shell(f"cat {STORE}/linux.img")
        for label, data in (("twrp", twrp), ("linux", linux)):
            if adb.shell(f"cat {STORE}/{label}.img.sha256").split()[0] != sha(data).encode():
                raise ValueError(f"saved {label} image checksum mismatch")
    args.backup_dir.mkdir(mode=0o700)
    checksums = {}

    def backup(filename, data):
        path = args.backup_dir / filename
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as out:
            out.write(data)
            out.flush()
            os.fsync(out.fileno())
        if sha(path.read_bytes()) != sha(data):
            raise RuntimeError("private host backup read-back failed")
        checksums[filename] = sha(data)

    backups = [("current-p12.img", current), ("misc.img", adb.shell("cat /dev/block/mmcblk0p8"))]
    if not fresh:
        backups += [("known-twrp.img", twrp), ("legacy-linux.img", linux),
                    ("legacy-slots.tar", adb.shell(f"tar -cf - -C {STORE} slots format active highest-revision"))]
    for filename, data in backups:
        if not data:
            raise RuntimeError("empty host backup: " + filename)
        backup(filename, data)
    if adb.shell(f"if test -d {STATE}; then echo present; fi").strip() == b"present":
        data = adb.shell("tar -cf - -C /data encore")
        if not data:
            raise RuntimeError("empty legacy data backup")
        backup("legacy-data.tar", data)
    else:
        backup("legacy-data-absent.txt", b"/data/encore was absent\n")
    # Older starter bundles require an existing key to boot. New ones begin
    # unpaired and let Home Assistant choose the permanent key after Action.
    key = None
    if not manifest.get("api_key_provisioning", False):
        key = base64.b64encode(secrets.token_bytes(32)) + b"\n"
        backup("encore-api-key.txt", key)
    backup("SHA256SUMS", "".join(f"{digest}  {filename}\n" for filename, digest in checksums.items()).encode())
    for directory in (args.backup_dir, args.backup_dir.parent):
        fd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    if fresh:
        adb.shell(f"umask 077; mkdir {STORE}")
        adb.write(f"{STORE}/twrp.img", twrp)
        adb.write(f"{STORE}/twrp.img.sha256", f"{sha(twrp)}  twrp.img\n".encode())
    adb.shell(f"umask 077; chmod 700 {STORE}; mkdir {pending} {pending}/slots "
              f"{pending}/slots/a {pending}/slots/b {next_data}")
    for slot in ("a", "b"):
        adb.write(f"{pending}/slots/{slot}/rootfs.tar.gz", rootfs)
        adb.write(f"{pending}/slots/{slot}.state", b"held\n")
    adb.write(f"{pending}/recovery.img", recovery)
    adb.write(f"{pending}/manifest.json", json.dumps(manifest, sort_keys=True).encode() + b"\n")
    adb.write(f"{next_data}/config.json", json.dumps({"schema": 1, "name": name}).encode() + b"\n")
    if key is not None:
        adb.write(f"{next_data}/noise.psk", key)
    adb.write(f"{pending}/active", b"a\n")
    adb.write(f"{pending}/highest-revision", f"{revision}\n".encode())
    adb.write(f"{pending}/format", (FORMAT + "\n").encode())
    # Remove the old boot selector before moving either filesystem's state.
    # Publish the new one only after p16 is durable and p12 has passed read-back.
    if not fresh:
        adb.shell(f"umask 077; mkdir {old}; mv {STORE}/format {old}/; sync; "
                  f"mv {STORE}/slots {STORE}/active {STORE}/highest-revision {old}/; chmod 700 {old}/slots; sync; "
                  f"if test -d {STATE}; then chmod 700 {STATE}; mv {STATE} {old_state}; fi")
    adb.shell(f"mv {next_data} {STATE}; sync; "
              f"mv {pending}/slots {pending}/highest-revision {pending}/active {STORE}/; sync")
    if sha(adb.shell(f"cat {P12}")) != sha(current):
        raise RuntimeError("p12 changed during install; selector remains unpublished; stay in TWRP")
    adb.shell(f"dd if={pending}/recovery.img of={P12} bs=4096; sync")
    if sha(adb.shell(f"cat {P12}")) != sha(recovery + current[len(recovery):]):
        raise RuntimeError("p12 read-back failed; selector remains unpublished; stay in TWRP")
    adb.shell(f"mv {pending}/format {STORE}/format; sync")
    print("ARM64 starter staged with both slots held. No reboot or activation performed.")
