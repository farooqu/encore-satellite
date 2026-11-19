"""Assemble the native RAM-root base without saved device state."""
import gzip
import io
import json
from pathlib import Path, PurePosixPath
import tarfile

from kernel61.arm64_probe import native_elf

HERE = Path(__file__).resolve().parent
TRIAL_FORMAT = "encore-biscuit-kernel61-trial-v1"


def rootfs_archive(base, kernel_record):
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

    for name in ("bin/busybox.static", "usr/local/bin/wmtup", "usr/local/bin/btbridge"):
        native_elf(entries[name][1], static=True)
    busybox = entries["bin/busybox.static"][1]
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
    add("usr/local/sbin/usb-rescue", (HERE / "usb-rescue").read_bytes(), 0o755)
    add("sbin/init", (HERE / "arm64-probe-init").read_bytes(), 0o755)
    add("etc/inittab", b"::respawn:/usr/local/sbin/usb-rescue\n")
    add("etc/encore-base", (TRIAL_FORMAT + "\n").encode())
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
