#!/usr/bin/env python3
"""Build the Dot Linux environment, including the project-built kernel."""

import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import stat
import struct
import subprocess
import tarfile
import zipfile
import zlib

import kernel_artifact

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
FORMAT = "encore-biscuit-v1"


def sha(data):
    return hashlib.sha256(data).hexdigest()


def arm_elf(data, static=False):
    if (data[:6] != b"\x7fELF\x01\x01" or len(data) < 52
            or struct.unpack_from("<H", data, 18)[0] != 40):
        raise ValueError("expected a little-endian 32-bit ARM ELF")
    if static:
        off = struct.unpack_from("<I", data, 28)[0]
        size, count = struct.unpack_from("<HH", data, 42)
        if size < 32 or off + size * count > len(data):
            raise ValueError("invalid ELF program headers")
        if any(struct.unpack_from("<I", data, off + size * i)[0] == 3 for i in range(count)):
            raise ValueError("initramfs busybox must be statically linked")


def cpio(files):
    """Linux newc archive. Only our explicit files/directories/symlinks enter it."""
    out = bytearray()
    for ino, (name, mode, data) in enumerate([*files, ("TRAILER!!!", 0, b"")], 1):
        name = name.encode() + b"\0"
        fields = (ino, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(name), 0)
        out += b"070701" + b"".join(f"{x:08x}".encode() for x in fields) + name
        out += b"\0" * (-len(out) % 4)
        out += data
        out += b"\0" * (-len(out) % 4)
    return gzip.compress(bytes(out), mtime=0)


def device_trees(kernel):
    # Biscuit appends exactly two FDT blobs. Match their complete bytes, not a
    # board-name substring that could accept a different memory/device layout.
    start = kernel.find(b"\xd0\x0d\xfe\xed")
    while start >= 0:
        trees, end = [], start
        while kernel[end:end+4] == b"\xd0\x0d\xfe\xed" and end + 40 <= len(kernel):
            size = struct.unpack_from(">I", kernel, end + 4)[0]
            if size < 40 or end + size > len(kernel):
                break
            trees.append(kernel[end:end+size])
            end += size
        if end == len(kernel) and len(trees) == 2:
            return trees
        start = kernel.find(b"\xd0\x0d\xfe\xed", start + 1)
    raise ValueError("expected biscuit kernel with two complete appended device trees")


def validate_kernel(kernel):
    if len(kernel) < 48 or len(kernel) > 16 * 1024 * 1024 or struct.unpack_from("<I", kernel, 36)[0] != 0x016f2818:
        raise ValueError("expected a bounded ARM zImage-dtb, not a boot image or ELF")
    device_trees(kernel)
    # IKCONFIG is inside the compressed kernel, not necessarily visible in the
    # zImage. Bound both decompressions; never trust an unrelated config sidecar.
    start = kernel.find(b"\x1f\x8b\x08")
    while start >= 0:
        try:
            stream = zlib.decompressobj(31)
            payload = stream.decompress(kernel[start:], 64 * 1024 * 1024)
            marker = payload.find(b"IKCFG_ST") if stream.eof else -1
            if marker >= 0:
                config_stream = zlib.decompressobj(31)
                config = config_stream.decompress(payload[marker+8:], 1024 * 1024)
                if config_stream.eof and config_stream.unused_data.startswith(b"IKCFG_ED"):
                    lines = config.decode("ascii").splitlines()
                    if not {"CONFIG_BT=y", "CONFIG_BT_HCIVHCI=y", 'CONFIG_ARCH_MTK_PROJECT="biscuit"'} <= set(lines):
                        raise ValueError("replacement kernel lacks biscuit Bluetooth/VHCI built-ins")
                    if "# CONFIG_BT_LE is not set" in lines:
                        raise ValueError("replacement kernel explicitly disables LE")
                    return
        except (zlib.error, UnicodeDecodeError):
            pass
        start = kernel.find(b"\x1f\x8b\x08", start + 1)
    raise ValueError("replacement kernel has no readable embedded IKCONFIG")


def boot_image(recovery, ramdisk, replacement=None):
    # Android v0 header, as used by biscuit TWRP. Preserve this unit's load
    # addresses and require identical DTBs when replacing its kernel.
    if len(recovery) < 608 or recovery[:8] != b"ANDROID!":
        raise ValueError("recovery is not an Android boot image")
    ks, _, rs, _, second, _, _, page, version = struct.unpack_from("<9I", recovery, 8)
    if version != 0 or page not in (2048, 4096, 8192, 16384) or not ks or not rs or second:
        raise ValueError("expected biscuit TWRP v0 image without a second stage")
    aligned = lambda n: (n + page - 1) // page * page
    if page + aligned(ks) + aligned(rs) > len(recovery):
        raise ValueError("truncated recovery image")
    header = bytearray(recovery[:page])
    kernel = recovery[page:page + ks]
    if replacement is not None:
        if device_trees(kernel) != device_trees(replacement):
            raise ValueError("replacement kernel device trees differ from this unit's recovery; refusing flash")
        kernel = replacement
        ks = len(kernel)
        struct.pack_into("<I", header, 8, ks)
    # Check the saved image too: live recovery may have been booted temporarily
    # with a different kernel. Interface presence cannot authenticate its backup.
    validate_kernel(kernel)
    # A boot-slot image has skip_initramfs supplied by its bootloader. Encore
    # therefore uses recovery, not both Android boot slots.
    import shlex
    command = bytes(header[64:576]).split(b"\0")[0].decode("ascii")
    words = shlex.split(command)
    command = " ".join(w for w in words if not w.startswith(("skip_initramfs", "root=", "dm=")))
    if len(command.encode()) > 511:
        raise ValueError("recovery command line too long")
    header[64:576] = command.encode().ljust(512, b"\0")
    struct.pack_into("<I", header, 16, len(ramdisk))
    digest = hashlib.sha1()
    for blob in (kernel, ramdisk, b"", b""):
        digest.update(blob)
        digest.update(struct.pack("<I", len(blob)))
    header[576:608] = digest.digest().ljust(32, b"\0")
    image = bytes(header) + kernel.ljust(aligned(ks), b"\0") + ramdisk.ljust(aligned(len(ramdisk)), b"\0")
    if len(image) > 16 * 1024 * 1024:
        raise ValueError("Encore recovery image exceeds biscuit's 16 MiB partition")
    return image


def load_sendspin(directory):
    directory = Path(directory)
    record = json.loads((directory / "provenance.json").read_bytes())
    source = ROOT / "platforms/linuxsendspin"
    inputs = [source / "build.py", source / "dependencies.lock.json", source / "bridge_linux.go",
              HERE / "sendspin.sh", *sorted(p for p in (source / "native").rglob("*") if p.is_file())]
    expected = {str(p.relative_to(ROOT)): sha(p.read_bytes()) for p in inputs}
    if (record.get("schema") != 1 or record.get("target") != "armv7"
            or record.get("dependencies") != json.loads((source / "dependencies.lock.json").read_bytes())
            or record.get("integration_sha256") != expected):
        raise ValueError("Sendspin artifact inputs mismatch; rebuild with make biscuit-sendspin")
    artifacts = record.get("artifacts", {})
    if not {"encore-sendspin", "source.tar.gz", "notices/README.txt"} <= artifacts.keys():
        raise ValueError("Sendspin artifact lacks binary, source kit or notices")
    files = {}
    for name, digest in artifacts.items():
        path = PurePosixPath(name)
        if path.is_absolute() or ".." in path.parts or (directory / name).is_symlink():
            raise ValueError("unsafe Sendspin artifact path")
        data = (directory / name).read_bytes()
        if sha(data) != digest:
            raise ValueError(f"Sendspin artifact checksum mismatch: {name}")
        files[name] = data
    arm_elf(files["encore-sendspin"], static=True)
    if not struct.unpack_from("<I", files["encore-sendspin"], 36)[0] & 0x400:
        raise ValueError("Sendspin requires ARM hard-float ABI")
    return record, files


def build(base, binary, wmtup, btbridge, revision, output, kernel_directory, sendspin_directory):
    provenance, kernel = kernel_artifact.load(kernel_directory)
    validate_kernel(kernel)
    kernel_record = json.dumps(provenance, sort_keys=True).encode()
    music_record, music_files = load_sendspin(sendspin_directory)
    entries = {}
    with tarfile.open(base, "r:gz") as archive:
        for member in archive:
            name = member.name.removeprefix("./").rstrip("/")
            if not name or name == ".":
                continue
            if PurePosixPath(name).is_absolute() or ".." in PurePosixPath(name).parts:
                raise ValueError("unsafe base archive path")
            if name.startswith("dev/"):
                continue  # mdev populates a private tmpfs at boot, not archive nodes
            if not (member.isfile() or member.isdir() or member.issym() or member.islnk()):
                raise ValueError("unsupported base archive member")
            data = archive.extractfile(member).read() if member.isfile() or member.islnk() else b""
            if member.islnk():
                member.type, member.size, member.linkname = tarfile.REGTYPE, len(data), ""
            entries[name] = (member, data)

    def add(name, data, mode=0o644, kind=tarfile.REGTYPE, link=""):
        info = tarfile.TarInfo(name)
        info.mode, info.type, info.linkname = mode, kind, link
        info.size = len(data) if kind == tarfile.REGTYPE else 0
        entries[name] = info, data

    for required in ("bin/busybox.static", "sbin/wpa_supplicant", "sbin/wpa_cli", "lib/apk/db/installed",
                     "usr/bin/dbus-daemon", "usr/bin/dbus-uuidgen", "usr/lib/bluetooth/bluetoothd",
                     "usr/sbin/dropbear", "usr/bin/dropbearkey", "usr/bin/flock",
                     "usr/sbin/iptables-legacy", "usr/sbin/ip6tables-legacy",
                     "usr/sbin/iptables-legacy-restore", "usr/sbin/ip6tables-legacy-restore"):
        if required not in entries:
            raise ValueError(f"base missing {required}; use packaging/base.sh")
    # Never import a builder's host identity or login keys into a device bundle.
    if any(name.startswith(("etc/dropbear/", "root/.ssh/", "data/encore/ssh/"))
           and (info.isfile() or info.issym()) for name, (info, _) in entries.items()):
        raise ValueError("base must not contain SSH keys or Dropbear host identity")
    busybox = entries["bin/busybox.static"][1]
    arm_elf(busybox, static=True)
    for path in (binary, wmtup, btbridge):
        arm_elf(Path(path).read_bytes(), static=True)
    add("usr/local/bin/encore-satellite", Path(binary).read_bytes(), 0o755)
    add("usr/local/bin/wmtup", Path(wmtup).read_bytes(), 0o755)
    add("usr/local/bin/btbridge", Path(btbridge).read_bytes(), 0o755)
    add("usr/local/bin/encore-sendspin", music_files["encore-sendspin"], 0o755)
    add("etc/encore-sendspin.json", json.dumps(music_record, sort_keys=True).encode())
    add("usr/share/licenses/encore/sendspin/dependencies.lock.json",
        (ROOT / "platforms/linuxsendspin/dependencies.lock.json").read_bytes())
    for name, data in music_files.items():
        if name.startswith("notices/"):
            add("usr/share/licenses/encore/sendspin/" + name.removeprefix("notices/"), data)
    for source, dest in (("boot.sh", "etc/encore/boot.sh"), ("dhcp.sh", "etc/encore/dhcp.sh"),
                         ("slotctl", "usr/local/sbin/slotctl"), ("run", "usr/local/sbin/encore-run"),
                         ("console", "usr/local/sbin/encore-console"),
                         ("bluetooth", "usr/local/sbin/encore-bluetooth"),
                         ("ssh", "usr/local/sbin/encore-ssh"),
                         ("firewall", "usr/local/sbin/encore-firewall"),
                         ("network", "usr/local/sbin/encore-network"),
                         ("recovery", "usr/local/sbin/encore-recovery"),
                         ("health", "usr/local/sbin/encore-health")):
        add(dest, (HERE / source).read_bytes(), 0o755)
    add("sbin/init", b"", 0o777, tarfile.SYMTYPE, "/bin/busybox.static")
    add("etc/inittab", b"::sysinit:/etc/encore/boot.sh\n::respawn:/usr/local/sbin/encore-run\n"
        b"::respawn:/usr/local/sbin/encore-console\n"
        b"::respawn:/usr/local/sbin/encore-bluetooth\n"
        b"::respawn:/usr/local/sbin/encore-ssh\n"
        b"::respawn:/usr/local/sbin/encore-network\n"
        b"::once:/usr/local/sbin/encore-health\n::ctrlaltdel:/bin/busybox.static reboot\n"
        b"::shutdown:/bin/busybox.static sync\n")
    add("etc/encore-base", (FORMAT + "\n").encode())
    add("etc/encore-kernel.json", kernel_record)
    add("usr/share/licenses/encore/KERNEL-SOURCE.md", (HERE / "KERNEL-SOURCE.md").read_bytes())
    with tarfile.open(Path(kernel_directory) / "source.tar.gz", "r:gz") as source:
        add("usr/share/licenses/encore/Linux-COPYING", source.extractfile("src/COPYING").read())
    add("etc/resolv.conf", b"")
    # Locked password does not lock Dropbear public-key authentication. The
    # supervisor supplies only validated keys with forwarding restrictions.
    add("etc/passwd", b"root:x:0:0:root:/root:/bin/sh\nmessagebus:x:100:101:messagebus:/run/dbus:/sbin/nologin\n")
    add("etc/group", b"root:x:0:root\nmessagebus:x:101:messagebus\n")
    add("etc/shadow", b"root:!:0:0:99999:7:::\n", 0o600)
    add("etc/shells", b"/bin/sh\n")
    for directory in ("root", "root/.ssh"):
        add(directory, b"", 0o700, tarfile.DIRTYPE)
    add("root/.ssh/authorized_keys", b"", 0o777, tarfile.SYMTYPE, "/run/encore/ssh/authorized_keys")
    for directory in ("proc", "sys", "dev", "dev/pts", "run", "run/encore", "tmp", "data", "store", "etc/encore"):
        add(directory, b"", 0o1777 if directory == "tmp" else 0o755, tarfile.DIRTYPE)
    add("usr/share/licenses/encore/LICENSE", (ROOT / "LICENSE").read_bytes())
    for path in (ROOT / "licenses").glob("*"):
        add("usr/share/licenses/encore/" + path.name, path.read_bytes())
    for path in HERE.glob("*.txt"):
        add("usr/share/licenses/encore/" + path.name, path.read_bytes())
    for path in (ROOT / "platforms/linux").rglob("*LICENSE*"):
        if path.is_file():
            add("usr/share/licenses/encore/linux-" + path.name, path.read_bytes())
    if (ROOT / "platforms/linux/NOTICE").is_file():
        add("usr/share/licenses/encore/linux-NOTICE", (ROOT / "platforms/linux/NOTICE").read_bytes())
    wake = ROOT / "core/wake/mixednet"
    for path in wake.rglob("*"):
        if path.is_file() and (path.name == "NOTICE" or path.name.startswith("LICENSE")):
            add("usr/share/licenses/encore/mixednet/" + str(path.relative_to(wake)), path.read_bytes())
    add("usr/share/licenses/encore/mwwfront/NOTICE", (ROOT / "core/wake/mwwfront/NOTICE").read_bytes())
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w") as archive:
        for name, (info, data) in sorted(entries.items()):
            info.name, info.uid, info.gid, info.mtime = name, 0, 0, 0
            info.uname = info.gname = "root"
            archive.addfile(info, io.BytesIO(data) if info.isfile() else None)
    rootfs = gzip.compress(buf.getvalue(), mtime=0)
    files = [(d, stat.S_IFDIR | 0o755, b"") for d in ("bin", "sbin", "usr", "usr/local", "usr/local/sbin", "dev", "proc", "sys", "run", "store", "newroot")]
    files += [("bin/busybox.static", stat.S_IFREG | 0o755, busybox),
              ("bin/sh", stat.S_IFLNK | 0o777, b"busybox.static"),
              ("init", stat.S_IFREG | 0o755, (HERE / "init").read_bytes()),
              ("usr/local/sbin/slotctl", stat.S_IFREG | 0o755, (HERE / "slotctl").read_bytes())]
    # slotctl's shell commands must exist before busybox --install can run.
    for app in ("cat", "sync", "mv"):
        files.append(("bin/" + app, stat.S_IFLNK | 0o777, b"busybox.static"))
    ramdisk = cpio(files)
    base_record = {"userspace_sha256": sha(Path(base).read_bytes()),
                   "kernel_inputs_sha256": provenance["inputs_sha256"],
                   "kernel_sha256": sha(kernel), "initramfs_sha256": sha(ramdisk)}
    payload = {"rootfs.tar.gz": rootfs, "initramfs.gz": ramdisk,
               "kernel.zImage-dtb": kernel, "kernel.json": kernel_record}
    manifest = {"schema": 1, "target": "biscuit", "backend": "incubator", "format": FORMAT, "channel": "dev",
                "core_version": json.loads((ROOT / "release/targets.json").read_text())["core_version"],
                "package_revision": revision,
                "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
                "source_dirty": bool(subprocess.check_output(["git", "status", "--porcelain", "--untracked-files=normal"], cwd=ROOT)),
                "base": base_record,
                "base_sha256": sha(json.dumps(base_record, sort_keys=True).encode()),
                "files": {name: sha(data) for name, data in payload.items()}}
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_STORED) as archive:
        for name, data in {"manifest.json": json.dumps(manifest, sort_keys=True).encode(), **payload}.items():
            # Like the tar/cpio payloads, ZIP headers must not record the build clock.
            archive.writestr(zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0)), data)
    print(f"Development bundle: {output}\nSHA256: {sha(Path(output).read_bytes())}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", type=Path, required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--wmtup", type=Path, required=True)
    parser.add_argument("--btbridge", type=Path, required=True)
    parser.add_argument("--kernel-artifact", type=Path, required=True, help="verified project kernel artifact directory")
    parser.add_argument("--sendspin-artifact", type=Path, required=True, help="locked static ARMv7 companion artifact directory")
    parser.add_argument("--revision", type=int, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.revision < 1:
        parser.error("development package revision must be positive")
    build(args.base, args.binary, args.wmtup, args.btbridge, args.revision, args.output, args.kernel_artifact, args.sendspin_artifact)
