"""Build an unsigned Biscuit starter and OTA payload from this checkout and locked inputs."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
FILES = {"alpine-apks.tar.gz", "alpine-source.tar.gz", "apk-tools-static.apk", "audited-header.bin",
         "go-linux-amd64.tar.gz", "go-source.tar.gz", "linux-source.tar.gz"}


def read_lock(data):
    lock = json.loads(data)
    if not isinstance(lock, dict):
        raise ValueError("invalid Biscuit external-input lock")
    tag = lock.get("external_bundle_tag")
    hashes = lock.get("sha256")
    if (set(lock) != {"external_bundle_tag", "sha256"}
            or (tag is not None and (not isinstance(tag, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", tag)))
            or not isinstance(hashes, dict) or set(hashes) != FILES
            or not all(isinstance(h, str) and re.fullmatch(r"[a-f0-9]{64}", h) for h in hashes.values())):
        raise ValueError("invalid Biscuit external-input lock")
    return lock


def verify_inputs(directory, hashes):
    for name, expected in sorted(hashes.items()):
        with (directory / name).open("rb") as source:
            if hashlib.file_digest(source, "sha256").hexdigest() != expected:
                raise ValueError(f"external input changed: {name}")


def pack(output, directory, names):
    subprocess.run(["tar", "--sort=name", "--mtime=@0", "--owner=0", "--group=0", "--numeric-owner",
                    "--hard-dereference", "-C", directory, "-czf", output, "--", *names], check=True)


def build_base(inputs, work, kernel):
    apks, base = work / "apks", work / "base"
    apks.mkdir()
    base.mkdir()
    with tarfile.open(inputs / "alpine-apks.tar.gz") as archive:
        members = archive.getmembers()
        if (not members or len({m.name for m in members}) != len(members)
                or not all(m.isfile() and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]*\.apk", m.name) for m in members)):
            raise ValueError("expected only distinct Alpine APKs")
        archive.extractall(apks, members=members, filter="data")
    apk = work / "apk.static"
    with tarfile.open(inputs / "apk-tools-static.apk", ignore_zeros=True) as archive:
        apk.write_bytes(archive.extractfile("sbin/apk.static").read())
    apk.chmod(0o755)
    keys = work / "keys"
    keys.mkdir()
    (keys_apk,) = apks.glob("alpine-keys-*.apk")
    with tarfile.open(keys_apk, ignore_zeros=True) as archive:
        for member in archive:
            if member.isfile() and re.fullmatch(r"etc/apk/keys/[A-Za-z0-9@._+-]+\.rsa\.pub", member.name):
                (keys / Path(member.name).name).write_bytes(archive.extractfile(member).read())
    # The pinned input bundle predates SSH removal; do not install its Dropbear APKs.
    packages = sorted(path for path in apks.iterdir() if not path.name.startswith("dropbear-"))
    # APK scripts cannot run as target executables here. Set up only the
    # accounts and directories the packaged boot scripts need.
    subprocess.run([apk, "--arch", "aarch64", "--root", base, "--keys-dir", keys,
                    "--repositories-file", "/dev/null", "--no-network", "--no-scripts", "--initdb",
                    "add", *packages], check=True)
    (base / "etc/passwd").write_text("root:x:0:0:root:/root:/bin/sh\nmessagebus:x:100:101:messagebus:/run/dbus:/sbin/nologin\n")
    (base / "etc/group").write_text("root:x:0:root\nmessagebus:x:101:messagebus\n")
    (base / "etc/shadow").write_text("root:!:0:0:99999:7:::\n")
    (base / "etc/shadow").chmod(0o600)
    (base / "etc/resolv.conf").write_bytes(b"")
    # The APK base includes static devices; switch_root moves the live devtmpfs.
    shutil.rmtree(base / "dev")
    for name in ("dev", "sys", "proc", "run", "run/encore", "data", "store", "tmp", "lib/firmware"):
        (base / name).mkdir(parents=True, exist_ok=True)
    shutil.copyfile(kernel / "src/firmware/i2s_to_spi_v34.bin", base / "lib/firmware/i2s_to_spi_v34.bin")
    for name in ("Image", "biscuit-arm64-probe.dtb"):
        shutil.copyfile(kernel / name, work / name)
    shutil.copyfile(inputs / "audited-header.bin", work / "audited-header.bin")
    pack(work / "rootfs.tar.gz", base, sorted(p.name for p in base.iterdir()))
    return base / "etc/ssl/certs/ca-certificates.crt"


def source_kits(inputs, kernel, sendspin, sources, env):
    sources.mkdir()
    kit = sources.parent / "kernel-sources"
    kit.mkdir()
    (kernel / "src").rename(kit / "linux")
    for name in ("manifest.json", "effective.config", "probe.dts"):
        shutil.copyfile(kernel / name, kit / name)
    pack(sources / "kernel-source.tar.gz", kit, ["linux", "effective.config", "manifest.json", "probe.dts"])
    kit = sources.parent / "alpine-go-sources"
    kit.mkdir()
    for name in ("alpine-source.tar.gz", "go-source.tar.gz"):
        shutil.copyfile(inputs / name, kit / name)
    # Retain the companion's full corresponding source under the existing
    # userspace source role; none of it needs to occupy the target RAM root.
    shutil.copyfile(sendspin / "source.tar.gz", kit / "sendspin-source.tar.gz")
    for name in ("go.mod", "go.sum"):
        shutil.copyfile(ROOT / name, kit / name)
    # Go verifies module zip contents against this checkout's go.sum.
    decoder = json.JSONDecoder()
    modules = subprocess.check_output(["go", "mod", "download", "-json"], cwd=ROOT, env=env, text=True).lstrip()
    while modules:
        module, end = decoder.raw_decode(modules)
        modules = modules[end:].lstrip()
        dest = kit / "modules" / (module["Path"] + "@" + module["Version"])
        dest.mkdir(parents=True)
        for key in ("Zip", "GoMod"):
            path = Path(module[key])
            shutil.copyfile(path, dest / path.name)
    pack(sources / "alpine-go-source.tar.gz", kit, sorted(p.name for p in kit.iterdir()))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--jobs", type=int, default=4)
    args = parser.parse_args()
    inputs, output = args.inputs.resolve(), args.output.resolve()
    source = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT))
    data = (HERE / "ci-inputs.json").read_bytes()
    verify_inputs(inputs, read_lock(data)["sha256"])
    output.mkdir(parents=True)
    work = output / "work"
    work.mkdir()
    with tarfile.open(inputs / "go-linux-amd64.tar.gz") as archive:
        archive.extractall(work, filter="data")
    env = dict(os.environ, PATH=f"{work / 'go/bin'}:{os.environ['PATH']}",
               GOROOT=str(work / "go"), GOTOOLCHAIN="local", GOFLAGS="-mod=readonly")
    kernel = work / "kernel"
    subprocess.run([sys.executable, HERE / "arm64_probe.py", "--source-archive", inputs / "linux-source.tar.gz",
                    "--output", kernel, "--jobs", str(args.jobs)], check=True)
    ca = build_base(inputs, work, kernel)
    from starter_build import build_sendspin
    sendspin = work / "sendspin"
    build_sendspin(sendspin, args.jobs)
    asset = f"encore-biscuit-arm64-{args.version}"
    files = {"starter.zip": f"{asset}.zip", "rootfs.tar.gz": f"{asset}-rootfs.tar.gz",
             "native-seed.zip": f"{asset}-native-seed.zip", "native-ota.zip": f"{asset}-native-ota.zip"}
    for attempt in ("primary", "repeat"):
        dest = output / attempt
        dest.mkdir()
        staging = work / attempt
        subprocess.run([sys.executable, HERE / "starter_build.py", "--sealed-base", work, "--ca-bundle", ca,
                        "--core-version", args.version, "--revision", args.revision, "--output", staging,
                        "--sendspin-directory", sendspin,
                        "--input-lock-sha256", hashlib.sha256(data).hexdigest(), "--native-payloads"], env=env, check=True)
        for old, name in files.items():
            (staging / old).rename(dest / name)
    for name in files.values():
        if (output / "primary" / name).read_bytes() != (output / "repeat" / name).read_bytes():
            raise ValueError(f"non-reproducible assembly: {name}")
    source_kits(inputs, kernel, sendspin, output / "sources", env)
    lines = [f"Unsigned Biscuit ARM64 candidate {args.version}", f"Source: {source}" + (" (dirty worktree)" if dirty else ""),
             f"External lock SHA-256: {hashlib.sha256(data).hexdigest()}",
             subprocess.check_output(["go", "version"], env=env, text=True).strip(),
             subprocess.check_output(["aarch64-linux-gnu-gcc", "-dumpfullversion"], text=True).strip() + " AArch64 GCC"]
    for directory in ("primary", "repeat", "sources"):
        for path in sorted((output / directory).iterdir()):
            with path.open("rb") as stream:
                lines.append(f"{hashlib.file_digest(stream, 'sha256').hexdigest()}  {path.relative_to(output)}")
    (output / "evidence.txt").write_text("\n".join(lines) + "\n")


if __name__ == "__main__":
    main()
