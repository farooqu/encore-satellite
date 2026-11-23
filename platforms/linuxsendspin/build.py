#!/usr/bin/env python3
"""Locked SDK build and redistribution kit. No Docker daemon is required.

Host:  sendspin.sh --target host --out build/sendspin-host
Dot:   sendspin.sh --target arm64 (current) or --target armv7 (legacy default)
Only downloads exact commits and the checksum-locked Zig userspace compiler.
"""
import argparse
import fcntl
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
ARM = {
    "armv7": ("armv7-musl.cmake", "-target arm-linux.3.18-musleabihf -mcpu=cortex_a7"),
    "arm64": ("arm64-musl.cmake", "-target aarch64-linux.6.1-musl -mcpu=cortex_a53"),
}


def run(*args, **kw):
    kw.setdefault("check", True)
    return subprocess.run([str(a) for a in args], **kw)


def output(*args):
    return subprocess.check_output([str(a) for a in args], text=True).strip()


def sha(path):
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def checkout(dest, spec):
    if not (dest / ".git").exists():
        dest.mkdir(parents=True, exist_ok=True)
        run("git", "init", "-q", dest)
        run("git", "-C", dest, "remote", "add", "origin", spec["url"])
    # Refuse local changes rather than silently incorporating unrecorded source.
    if output("git", "-C", dest, "status", "--porcelain", "--untracked-files=all"):
        raise RuntimeError(f"modified dependency: {dest}")
    if run("git", "-C", dest, "cat-file", "-e", spec["commit"], stdout=subprocess.DEVNULL,
           stderr=subprocess.DEVNULL, check=False).returncode != 0:
        run("git", "-C", dest, "fetch", "--quiet", "--depth=1", "origin", spec["commit"])
    run("git", "-C", dest, "checkout", "--quiet", "--detach", spec["commit"])
    if output("git", "-C", dest, "rev-parse", "HEAD") != spec["commit"]:
        raise RuntimeError("dependency revision mismatch")


def add_tree(tar, path, arc):
    def normalized(info):
        if {".git", "__pycache__"}.intersection(Path(info.name).parts):
            return None
        info.uid = info.gid = 0
        info.uname = info.gname = ""
        info.mtime = 0
        return info
    tar.add(path, arcname=arc, filter=normalized)


def prepare_zig(cache, spec):
    archive = cache / "zig.tar.xz"
    if not archive.exists():
        urllib.request.urlretrieve(spec["url"], archive)
    # Verify and extract the same open archive. Never trust an extracted cache:
    # both the executable and the runtime sources copied to the kit come here.
    with archive.open("rb") as raw:
        if hashlib.file_digest(raw, "sha256").hexdigest() != spec["sha256"]:
            raise RuntimeError("Zig archive checksum mismatch; remove corrupt cache file")
        raw.seek(0)
        with tempfile.TemporaryDirectory(prefix="zig-extract-", dir=cache) as tmp:
            with tarfile.open(fileobj=raw) as tar:
                tar.extractall(tmp, filter="data")
            zig = cache / spec["directory"]
            if zig.is_symlink() or zig.is_file():
                zig.unlink()
            elif zig.exists():
                shutil.rmtree(zig)
            shutil.move(str(Path(tmp) / spec["directory"]), zig)
    return zig


def compiler_provenance(build):
    # File API reflects CMake's configured compilers, including an existing
    # cache (which takes precedence over CC/CXX and the shell's c++ command).
    reply = build / ".cmake/api/v1/reply"
    index = json.loads(sorted(reply.glob("index-*.json"))[-1].read_text())
    toolchains = json.loads((reply / index["reply"]["toolchains-v1"]["jsonFile"]).read_text())
    commands = json.loads((build / "compile_commands.json").read_text())
    cache = {}
    for line in (build / "CMakeCache.txt").read_text().splitlines():
        if line and not line.startswith(("#", "//")) and "=" in line:
            key, value = line.split("=", 1)
            cache[key.split(":", 1)[0]] = value
    compilers = {}
    for toolchain in toolchains["toolchains"]:
        language = toolchain["language"]
        if language not in ("C", "CXX"):
            continue
        extensions = {"." + ext for ext in toolchain["sourceFileExtensions"]}
        sample = next(c for c in commands if Path(c["file"]).suffix in extensions)
        # Keep the generated command, including ARG1, target and compile flags.
        # CMake omits launchers from compile_commands, so record that cache
        # setting separately in its original CMake-list syntax.
        compilers[language] = {
            **toolchain["compiler"], "compile_command": sample,
            "launcher": cache.get(f"CMAKE_{language}_COMPILER_LAUNCHER", ""),
        }
    if set(compilers) != {"C", "CXX"}:
        raise RuntimeError("missing configured C/C++ compiler provenance")
    return compilers


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", choices=["host", *ARM], default="armv7")
    parser.add_argument("--out", type=Path)
    parser.add_argument("--cache", type=Path, default=ROOT / ".amp/in/orb-backup/sendspin-build-cache")
    parser.add_argument("--jobs", type=int, default=4)
    args = parser.parse_args()
    cache = args.cache.resolve()
    default = "sendspin-arm64" if args.target == "arm64" else "sendspin"
    out = (args.out or ROOT / "build/biscuit" / default).resolve()
    cache.mkdir(parents=True, exist_ok=True)
    # Hold one persistent lock inode through checkout, extraction, compilation,
    # source-kit copying and provenance. A second invocation must not replace
    # the first one's verified toolchain while it is still in use.
    with (cache / ".build.lock").open("a") as guard:
        fcntl.flock(guard, fcntl.LOCK_EX)
        build_locked(args, cache, out)


def build_locked(args, cache, out):
    out.mkdir(parents=True, exist_ok=True)
    lock = json.loads((HERE / "dependencies.lock.json").read_text())
    inputs = [HERE / "build.py", HERE / "dependencies.lock.json", HERE / "bridge_linux.go",
              ROOT / "devices/biscuit/packaging/sendspin.sh"]
    inputs += sorted(p for p in (HERE / "native").rglob("*") if p.is_file())
    integration_hashes = {str(p.relative_to(ROOT)): sha(p) for p in inputs}
    sources = {}
    for name, spec in lock["sources"].items():
        dest = cache / "sources" / spec.get("parent", name)
        checkout(dest, spec)
        sources[name] = dest
    env = os.environ.copy()
    env["SOURCE_DATE_EPOCH"] = "0"
    zig = None
    if args.target in ARM:
        zig = prepare_zig(cache, lock["zig"])
        env["ZIG_EXE"] = str(zig / "zig")
        env["ZIG_GLOBAL_CACHE_DIR"] = str(cache / "zig-cache")
    build = cache / ("build-" + args.target)
    query = build / ".cmake/api/v1/query/toolchains-v1"
    query.parent.mkdir(parents=True, exist_ok=True)
    query.touch()
    flags = f"-ffile-prefix-map={ROOT}=/src -ffile-prefix-map={cache}=/build"
    configure = ["cmake", "-S", HERE / "native", "-B", build, "-G", "Ninja",
                 "-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_EXPORT_COMPILE_COMMANDS=ON",
                 f"-DSDK_SOURCE={sources['sdk']}",
                 "-DFETCHCONTENT_FULLY_DISCONNECTED=ON", "-DSENDSPIN_USE_CCACHE=OFF",
                 f"-DCMAKE_C_FLAGS={flags}", f"-DCMAKE_CXX_FLAGS={flags}"]
    # Explicit flags include the target: CMAKE_*_FLAGS overrides toolchain INIT.
    if zig:
        toolchain, target = ARM[args.target]
        configure[-2:] = [f"-DCMAKE_C_FLAGS={target} {flags}", f"-DCMAKE_CXX_FLAGS={target} {flags}"]
        configure += [f"-DCMAKE_TOOLCHAIN_FILE={HERE / 'native' / toolchain}", "-DENCORE_STATIC=ON"]
    for name in ["arduinojson", "ixwebsocket", "micro_flac", "noise_c"]:
        configure.append(f"-DFETCHCONTENT_SOURCE_DIR_{name.upper()}={sources[name]}")
    run(*configure, env=env)
    compilers = compiler_provenance(build)
    run("cmake", "--build", build, "--target", "encore-sendspin", "-j", args.jobs, env=env)
    if zig:
        run(zig / "zig", "objcopy", "--strip-all", build / "encore-sendspin", out / "encore-sendspin")
    else:
        shutil.copy2(build / "encore-sendspin", out / "encore-sendspin")
    shutil.copyfile(HERE / "dependencies.lock.json", out / "dependencies.lock.json")
    if zig:
        binary = out / "encore-sendspin"
        if args.target == "arm64":
            with binary.open("rb") as stream:
                header = stream.read(20)
            if header[:6] != b"\x7fELF\x02\x01" or int.from_bytes(header[18:20], "little") != 183:
                raise RuntimeError("expected a little-endian AArch64 ELF companion")
        headers = output("readelf", "-l", binary)
        dynamic = output("readelf", "-d", binary)
        if "INTERP" in headers or "NEEDED" in dynamic:
            raise RuntimeError("ARM companion unexpectedly needs a dynamic loader")
    notices = out / "notices"
    notices.mkdir(exist_ok=True)
    records = dict(sources)
    if zig:
        records["zig"] = zig
    for name, src in records.items():
        for p in sorted(src.rglob("*")):
            if not p.is_file() or ".git" in p.parts:
                continue
            if name == "zig" and "mingw" in p.parts:
                continue
            if any(x in p.name.lower() for x in ["license", "copying", "copyright", "notice"]):
                target = notices / name / p.relative_to(src)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(p, target)
    (notices / "README.txt").write_text(
        "Official Sendspin companion; player/controller only. Sources and per-file crypto/runtime\n"
        "notices are included in source.tar.gz. No Opus, TLS, ALSA or HTTP sink is shipped.\n"
        "See provenance.json and dependencies.lock.json for exact revisions and source URLs.\n")
    # Source kit: complete locked dependency trees, integration/build code, and
    # (ARM only) the bundled userspace/runtime sources used by the Zig toolchain.
    with (out / "source.tar.gz").open("wb") as raw:
        with gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0, compresslevel=6) as gz:
            with tarfile.open(fileobj=gz, mode="w") as tar:
                for name, src in sources.items():
                    if name != "micro_ogg":  # already inside micro_flac
                        add_tree(tar, src, "sources/" + name)
                add_tree(tar, HERE, "integration/platforms/linuxsendspin")
                add_tree(tar, ROOT / "devices/biscuit/packaging/sendspin.sh", "integration/devices/biscuit/packaging/sendspin.sh")
                if zig:
                    for rel in ["LICENSE", "lib/libc/musl", "lib/libc/include", "lib/libcxx", "lib/libcxxabi", "lib/libunwind", "lib/compiler_rt", "lib/compiler_rt.zig", "lib/std", "lib/include"]:
                        if (zig / rel).exists():
                            add_tree(tar, zig / rel, "toolchain/" + rel)
    if integration_hashes != {str(p.relative_to(ROOT)): sha(p) for p in inputs}:
        raise RuntimeError("production inputs changed during build; rerun before packaging")
    provenance = {"schema": 1, "target": args.target, "dependencies": lock,
                  "integration_sha256": integration_hashes,
                  "configure": [str(x) for x in configure], "source_date_epoch": 0,
                  "compiler": "{path} ({id} {version})".format(**compilers["CXX"]),
                  "compilers": compilers,
                  "cmake": output("cmake", "--version"), "ninja": output("ninja", "--version"),
                  "artifacts": {str(p.relative_to(out)): sha(p) for p in sorted(out.rglob("*"))
                                if p.is_file() and p.name != "provenance.json"}}
    (out / "provenance.json").write_text(json.dumps(provenance, indent=2) + "\n")
    print(json.dumps({"directory": str(out), "binary_sha256": sha(out / "encore-sendspin")}))


if __name__ == "__main__":
    main()
