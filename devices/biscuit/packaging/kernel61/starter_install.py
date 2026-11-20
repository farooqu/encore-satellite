"""Verify and stage a held Biscuit Linux 6.1/AArch64 installation."""

import base64
import gzip
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
            if manifest.get("retained_base_sha256") != BASE_SHA or sha(read("etc/ssl/certs/ca-certificates.crt")) != CA_SHA:
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
    if sha(image) != IMAGE_SHA or sha(dtb) != DTB_SHA:
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
