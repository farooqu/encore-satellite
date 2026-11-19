"""Assemble a private RAM-root trial, optionally inside an explicit offline header.

No device tools, installation commands, persistent slots or release feed exist
in this path. Input packages are retained userspace dependencies, not releases
of this experiment. Their original manifest is retained without relabelling it.
"""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import shlex
import stat
import struct
import subprocess
import sys
import tarfile
import zipfile

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from kernel61.build import COMMIT, SOURCE_SHA256, validate_payload
from package import arm_elf, cpio

FORMAT = "encore-biscuit-kernel61-trial-v1"


def sha(data):
    return hashlib.sha256(data).hexdigest()


def boot_image(template, payload, ramdisk, system_map, template_metadata=None):
    """Keep unit-supplied addresses; reject layouts outside the audited DT RAM.

    The caller checks the pinned DT memory/reservation tuples separately. Unlike
    LibreEcho's Radar image generator this does not add an MTK kernel header,
    pick new load addresses or generate a board's recovery header from scratch.
    """
    if not 2048 <= len(template) <= 16 << 20 or template[:8] != b"ANDROID!":
        raise ValueError("expected a bounded offline Android v0 recovery template")
    ks, ka, rs, ra, second, _, tags, page, version, _ = struct.unpack_from("<10I", template, 8)
    # Offset 40 is header_version (older headers: unused/dt_size). Offset 44
    # is os_version in AOSP v0, NOT another version or a required zero word.
    # Preserve it verbatim; inherited metadata does not attest this trial's OS.
    if page not in (2048, 4096, 8192, 16384) or second or version or not ks or not rs:
        raise ValueError("unsupported recovery header/sections")
    align = lambda n: (n + page - 1) // page * page
    saved_size = len(template)
    if template_metadata is not None:
        evidence = template_metadata
        if len(template) != page or evidence["header_page_bytes"] != page or evidence["header_page_sha256"] != sha(template):
            raise ValueError("header-only evidence does not match the supplied page")
        saved_size = evidence["full_saved_file_bytes"]
        digest = evidence["full_saved_file_sha256"]
        if (not isinstance(saved_size, int) or not page < saved_size <= 16 << 20
                or len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest)):
            raise ValueError("invalid owner-attested saved image identity")
        first = bytes.fromhex(evidence["kernel_first48_hex"])
        if len(first) != 48 or sha(first) != evidence["kernel_first48_sha256"]:
            raise ValueError("invalid owner-attested raw kernel prefix")
        # Verify framing against the received header, not duplicate labels in
        # metadata. The original payload/full-file hash remain owner-attested.
        bounds = evidence["section_bounds"]
        end = page + ks
        ramdisk_offset = page + align(ks)
        if bounds != {"kernel": {"offset": page, "end": end, "aligned_end": ramdisk_offset},
                      "ramdisk": {"offset": ramdisk_offset, "end": ramdisk_offset + rs,
                                  "aligned_end": ramdisk_offset + align(rs)},
                      "second": {"offset": ramdisk_offset + align(rs), "size": 0},
                      "all_sections_within_saved_file": True}:
            raise ValueError("owner-attested section bounds differ from header")
    else:
        first = template[page:page + 48]
    if page + align(ks) + align(rs) > saved_size:
        raise ValueError("truncated template")
    if len(template) < page or first[36:40] != struct.pack("<I", 0x016F2818):
        raise ValueError("template must use a raw ARM zImage without an MTK wrapper")
    symbols = {fields[2]: int(fields[0], 16) for line in system_map.splitlines()
               if len(fields := line.split()) == 3 and fields[2] in ("_text", "_end")}
    if set(symbols) != {"_text", "_end"} or not 0 < symbols["_end"] - symbols["_text"] < 48 << 20:
        raise ValueError("invalid kernel memory map")
    kernel_end = ka + max(len(payload), symbols["_end"] - symbols["_text"])
    # These bounds are checked against this artifact's DT before invocation.
    # The ARM image links at c0008000; the physical entry is 40008000.
    ramdisk_end = ra + len(ramdisk)
    ramdisk_in_ram = any(lo <= ra < ramdisk_end <= hi for lo, hi in
                         ((0x43030000, 0x44400000), (0x44600000, 0x60000000)))
    if (symbols["_text"] != 0xC0008000 or ka != 0x40008000 or kernel_end > 0x43000000
            or not ramdisk_in_ram or not 0x44600000 <= tags <= 0x60000000 - 65536
            or (ra < tags + 65536 and tags < ramdisk_end)):
        raise ValueError("template load ranges overlap or differ from the audited RAM layout")
    header = bytearray(template[:page])
    command = (bytes(header[64:576]).split(b"\0")[0] +
               bytes(header[608:1632]).split(b"\0")[0]).decode("ascii")
    words = shlex.split(command)
    if "bootopt=64S3,32N2,32N2" not in words:
        raise ValueError("template does not explicitly select the ARM32 boot path")
    words = [w for w in words if not w.startswith(("skip_initramfs", "root=", "dm=", "init=", "rdinit=", "resume=", "rootflags=", "rootfstype="))]
    command = " ".join(words + ["rdinit=/init"]).encode("ascii")
    if len(command) > 511:
        raise ValueError("sanitized recovery command line is too long")
    header[64:576] = command.ljust(512, b"\0")
    header[608:1632] = bytes(1024)
    struct.pack_into("<I", header, 8, len(payload))
    struct.pack_into("<I", header, 16, len(ramdisk))
    digest = hashlib.sha1()
    for data in (payload, ramdisk, b""):
        digest.update(data)
        digest.update(struct.pack("<I", len(data)))
    header[576:608] = digest.digest().ljust(32, b"\0")
    image = bytes(header) + payload.ljust(align(len(payload)), b"\0") + ramdisk.ljust(align(len(ramdisk)), b"\0")
    if len(image) > 16 << 20:
        raise ValueError("trial image exceeds the 16 MiB partition bound")
    return image


def rootfs_archive(base, runtime, kernel_record, *, elf_check=arm_elf):
    entries = {}
    with tarfile.open(fileobj=io.BytesIO(base), mode="r:gz") as archive:
        for info in archive:
            name = info.name.removeprefix("./").rstrip("/")
            path = PurePosixPath(name)
            if not name or name == ".":
                continue
            if (path.is_absolute() or ".." in path.parts or str(path) != name
                    or name in entries or not (info.isfile() or info.isdir() or info.issym())):
                raise ValueError("unsafe/duplicate userspace member: " + name)
            if (name.startswith(("dev/", "data/", "store/", "run/", "tmp/", "root/.ssh/", "etc/dropbear/", "var/lib/bluetooth/"))
                    and not info.isdir() and name != "root/.ssh/authorized_keys"):
                raise ValueError("userspace contains runtime state/identity: " + name)
            data = archive.extractfile(info).read() if info.isfile() else b""
            entries[name] = info, data
    # Do not build a tar that can write through an archived directory symlink.
    for name in entries:
        for parent in PurePosixPath(name).parents:
            if str(parent) in entries and not entries[str(parent)][0].isdir():
                raise ValueError("non-directory archive parent: " + str(parent))
    for name in ("sbin/wpa_supplicant", "sbin/wpa_cli", "lib/apk/db/installed",
                 "usr/bin/dbus-daemon", "usr/bin/dbus-uuidgen", "usr/lib/bluetooth/bluetoothd",
                 "usr/sbin/dropbear", "usr/bin/dropbearkey", "usr/bin/flock",
                 "usr/sbin/iptables-legacy", "usr/sbin/ip6tables-legacy",
                 "usr/sbin/iptables-legacy-restore", "usr/sbin/ip6tables-legacy-restore"):
        if name not in entries:
            raise ValueError("userspace missing required utility/inventory: " + name)

    def add(name, data, mode=0o644, kind=tarfile.REGTYPE, link=""):
        info = tarfile.TarInfo(name)
        info.mode, info.type, info.linkname = mode, kind, link
        info.size = len(data)
        entries[name] = info, data

    native_helpers = ("bin/busybox.static", "usr/local/bin/wmtup", "usr/local/bin/btbridge")
    if runtime is not None:
        native_helpers += ("usr/local/bin/encore-sendspin",)
    for name in native_helpers:
        elf_check(entries[name][1], static=True)
    busybox = entries["bin/busybox.static"][1]
    if runtime is not None:
        elf_check(runtime, static=True)
        add("usr/local/bin/encore-satellite", runtime, 0o755)
    # Rebuild service composition from source, not inherited historical scripts.
    for name in ("slotctl", "encore-recovery", "encore-health"):
        entries.pop("usr/local/sbin/" + name, None)
    entries.pop("etc/encore-package.json", None)
    for source, target in (("run", "encore-run"), ("ssh", "encore-ssh"),
                           ("network", "encore-network"), ("firewall", "encore-firewall")):
        add("usr/local/sbin/" + target, (HERE.parent / source).read_bytes(), 0o755)
    add("etc/encore/dhcp.sh", (HERE.parent / "dhcp.sh").read_bytes(), 0o755)
    bluetooth = (HERE.parent / "bluetooth").read_text()
    if bluetooth.count("/proc/idme/bt_mac_addr") != 1:
        raise ValueError("Bluetooth identity source contract changed; review DT-IDME integration")
    # Vendor idme.c exposes each /idme child node's `value` property through
    # procfs. The 6.1 connectivity import consumes DT values directly instead.
    bluetooth = bluetooth.replace("/proc/idme/bt_mac_addr", "/sys/firmware/devicetree/base/idme/bt_mac_addr/value")
    add("usr/local/sbin/encore-bluetooth", bluetooth.encode(), 0o755)
    add("usr/local/sbin/encore-console", (HERE / "usb-rescue").read_bytes(), 0o755)
    add("usr/local/sbin/usb-rescue", (HERE / "usb-rescue").read_bytes(), 0o755)
    add("sbin/init", (HERE / "trial-init").read_bytes(), 0o755)
    add("etc/encore/boot.sh", (HERE / "boot.sh").read_bytes(), 0o755)
    add("etc/inittab", b"".join(("::respawn:/usr/local/sbin/" + name + "\n").encode()
                              for name in ("encore-console", "encore-run", "encore-bluetooth", "encore-ssh", "encore-network")) +
        b"::once:/etc/encore/boot.sh\n" +
        b"::shutdown:/bin/busybox.static sync\n")
    add("etc/encore-base", (FORMAT + "\n").encode())
    add("etc/encore-kernel.json", json.dumps(kernel_record, sort_keys=True).encode())
    add("usr/share/licenses/encore/KERNEL-SOURCE.md", (HERE / "README.md").read_bytes())
    # The trial uses no saved identity, credentials, bonds or update-trust file.
    add("root/.ssh/authorized_keys", b"", 0o777, tarfile.SYMTYPE, "/run/encore/ssh/authorized_keys")
    for name in ("dev", "sys", "proc", "run", "run/encore", "data", "store", "tmp"):
        add(name, b"", 0o755, tarfile.DIRTYPE)
    if sum(len(data) for _, data in entries.values()) > 96 << 20:
        raise ValueError("rootfs exceeds the RAM-root budget (96 MiB files, 128 MiB tmpfs)")
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w") as archive:
        for name, (info, data) in sorted(entries.items()):
            info.name, info.uid, info.gid, info.mtime = name, 0, 0, 0
            info.uname = info.gname = "root"
            archive.addfile(info, io.BytesIO(data) if info.isfile() else None)
    return gzip.compress(buf.getvalue(), mtime=0), busybox


def assemble(kernel_directory, userspace_bundle, userspace_sha256, output, template=None, template_sha256=None,
             template_metadata=None, metadata_sha256=None, rescue_only=False):
    bundle = userspace_bundle.read_bytes()
    if sha(bundle) != userspace_sha256:
        raise ValueError("retained userspace checksum mismatch")
    with zipfile.ZipFile(io.BytesIO(bundle)) as archive:
        retained = json.loads(archive.read("manifest.json"))
        base = archive.read("rootfs.tar.gz")
        if sha(base) != retained["files"]["rootfs.tar.gz"]:
            raise ValueError("retained rootfs checksum mismatch")
    record = json.loads((kernel_directory / "manifest.json").read_bytes())
    if (record["status"] != "NON_RELEASE_NOT_FOR_FLASHING" or record["installable"] is not False
            or record["kernel_commit"] != COMMIT or record["source_sha256"] != SOURCE_SHA256
            or record["runtime_build_tag"] != "biscuit_kernel61"):
        raise ValueError("not an Encore 6.1 experiment artifact")
    if not record["runtime_sources"] or any(
            sha((HERE.parents[3] / name).read_bytes()) != digest
            for name, digest in record["runtime_sources"].items()):
        raise ValueError("runtime sources differ from this checkout; rebuild the experiment")
    files = {}
    for name in ("zImage", "biscuit.dtb", "zImage-dtb", "encore-satellite", "System.map", "effective.config"):
        data = (kernel_directory / name).read_bytes()
        if sha(data) != record["files"][name]["sha256"] or len(data) != record["files"][name]["bytes"]:
            raise ValueError("kernel artifact mismatch: " + name)
        files[name] = data
    validate_payload(files["zImage"], files["biscuit.dtb"], files["zImage-dtb"])
    config = set(files["effective.config"].decode().splitlines())
    required = {f"CONFIG_{key}=y" for key in ("DEVTMPFS", "TMPFS", "EXT4_FS", "BLK_DEV_INITRD", "RD_GZIP", "USB_CONFIGFS_ACM")}
    if not required <= config:
        raise ValueError("kernel lacks RAM-root/rescue built-ins")
    # Refuse DT drift rather than extrapolate fixed load-address safety bounds.
    for node, expected in (("/memory", "0 40000000 0 20000000"),
                           ("/reserved-memory/atf-reserved-memory@43000000", "0 43000000 0 30000"),
                           ("/reserved-memory/ramoops@44400000", "0 44400000 0 200000")):
        actual = subprocess.check_output(["fdtget", "-tx", str(kernel_directory / "biscuit.dtb"), node, "reg"], text=True).strip()
        if actual != expected:
            raise ValueError("DT memory layout changed: " + node)
    reserved = "/reserved-memory"
    dtb = str(kernel_directory / "biscuit.dtb")
    nodes = subprocess.check_output(["fdtget", "-l", dtb, reserved], text=True).splitlines()
    if set(nodes) != {"atf-reserved-memory@43000000", "ramoops@44400000", "consys-reserve-memory"}:
        raise ValueError("DT reserved-memory nodes changed")
    dynamic = reserved + "/consys-reserve-memory"
    props = subprocess.check_output(["fdtget", "-p", dtb, dynamic], text=True).splitlines()
    if set(props) != {"compatible", "no-map", "size", "alignment"}:
        raise ValueError("DT dynamic CONSYS reservation changed")
    for prop in ("size", "alignment"):
        if subprocess.check_output(["fdtget", "-tx", dtb, dynamic, prop], text=True).strip() != "0 200000":
            raise ValueError("DT dynamic CONSYS allocation changed")
    # The pinned ARM arm_memblock_init reserves the kernel/initrd before
    # early_init_fdt_scan_reserved_mem allocates this 2 MiB dynamic region.
    reserve_offset = struct.unpack_from(">I", files["biscuit.dtb"], 16)[0]
    if files["biscuit.dtb"][reserve_offset:reserve_offset + 16] != bytes(16):
        raise ValueError("DT explicit memory reservation table changed")
    rootfs, busybox = rootfs_archive(base, files["encore-satellite"], record)
    init = (HERE / "init").read_bytes().replace(b"@ROOTFS_SHA256@", sha(rootfs).encode())
    init = init.replace(b"@RESCUE_ONLY@", b"1" if rescue_only else b"0")
    initfiles = [(d, stat.S_IFDIR | 0o755, b"") for d in ("bin", "sbin", "usr", "usr/local", "usr/local/sbin", "dev", "dev/pts", "proc", "sys", "run", "store", "newroot")]
    initfiles += [("bin/busybox.static", stat.S_IFREG | 0o755, busybox),
                  ("bin/sh", stat.S_IFLNK | 0o777, b"busybox.static"),
                  ("init", stat.S_IFREG | 0o755, init),
                  ("usr/local/sbin/usb-rescue", stat.S_IFREG | 0o755, (HERE / "usb-rescue").read_bytes())]
    ramdisk = cpio(initfiles)
    result = {"rootfs.tar.gz": rootfs, "initramfs.gz": ramdisk, "kernel.zImage-dtb": files["zImage-dtb"]}
    if (template is None) != (template_sha256 is None):
        raise ValueError("offline recovery template and explicit SHA256 must be supplied together")
    if (template_metadata is None) != (metadata_sha256 is None) or (template_metadata is not None and template is None):
        raise ValueError("header-only metadata requires a template and explicit metadata SHA256")
    evidence = None
    if template_metadata is not None:
        data = template_metadata.read_bytes()
        if sha(data) != metadata_sha256:
            raise ValueError("offline recovery metadata checksum mismatch")
        evidence = json.loads(data)
    if template is not None:
        envelope = template.read_bytes()
        if sha(envelope) != template_sha256:
            raise ValueError("offline recovery template checksum mismatch")
        result["trial-recovery.img"] = boot_image(envelope, files["zImage-dtb"], ramdisk, files["System.map"].decode(), evidence)
    output.mkdir(parents=True, exist_ok=False)
    for name, data in result.items():
        (output / name).write_bytes(data)
    manifest = {"schema": 1, "format": FORMAT, "target": "biscuit", "backend": "incubator",
                "status": "NON_RELEASE_NOT_FOR_FLASHING", "installable": False,
                "boot_mode": "rescue-only" if rescue_only else "full-service-usb-first",
                "userspace_input_sha256": userspace_sha256, "retained_userspace_manifest": retained,
                "kernel_artifact_manifest": record, "offline_template_sha256": template_sha256,
                "offline_metadata_sha256": metadata_sha256,
                "template_verification": ("local full-image checksum/framing" if evidence is None else
                                          "local header/metadata checksums; original payload and full-image identity owner-attested") if template else None,
                "owner_attested_saved_image": ({"bytes": evidence["full_saved_file_bytes"],
                                                "sha256": evidence["full_saved_file_sha256"]} if evidence else None),
                "inherited_header_word_at_44": (f"0x{struct.unpack_from('<I', envelope, 44)[0]:08x}" if template else None),
                "assembly_inputs": {p.name: sha(p.read_bytes()) for p in sorted(HERE.iterdir()) if p.is_file()},
                "service_inputs": {n: sha((HERE.parent / n).read_bytes()) for n in ("bluetooth", "run", "ssh", "network", "dhcp.sh", "firewall", "package.py")},
                "store_policy": "p15 ro,noload; RAM root/data; no MISC, recovery, expdb or slot writes; OTA not constructed",
                "files": {n: {"bytes": len(d), "sha256": sha(d)} for n, d in result.items()},
                "unqualified": ["actual boot/header/USB enumeration", "radio identity and firmware startup", "audio/privacy/ring/thermal hardware", "RAM pressure and reboot recovery"],
                "missing": [] if template else ["unit-specific offline recovery template; no boot image generated"]}
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print("NON_RELEASE_NOT_FOR_FLASHING:", output)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kernel-artifact", type=Path, required=True)
    parser.add_argument("--userspace-bundle", type=Path, required=True)
    parser.add_argument("--userspace-sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--recovery-template", type=Path)
    parser.add_argument("--template-sha256")
    parser.add_argument("--template-metadata", type=Path, help="owner-attested framing for a header-only template")
    parser.add_argument("--metadata-sha256")
    parser.add_argument("--rescue-only", action="store_true", help="enter initramfs USB rescue without mounting store or starting userspace services")
    args = parser.parse_args()
    assemble(args.kernel_artifact, args.userspace_bundle, args.userspace_sha256,
             args.output, args.recovery_template, args.template_sha256,
             args.template_metadata, args.metadata_sha256, args.rescue_only)
