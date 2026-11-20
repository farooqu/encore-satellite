"""Assemble the Android-specific evidence and source kit for a Dev receipt."""

import argparse
import base64
import gzip
import json
import os
from pathlib import Path
import re
import shutil
import tarfile
import tempfile

import pipeline

ROOT = pipeline.ROOT
BUILD = ROOT / "build/android"
APP = ROOT / "devices/android-generic/app/build"


def add_sources(output):
    goroot = Path(pipeline.run("go", "env", "GOROOT").decode().strip())
    native = [BUILD / "sendspindroid.tar.gz", *sorted((BUILD / "aec").glob("*.tar.xz")),
              *sorted((BUILD / "aec/src/subprojects/packagecache").glob("*"))]
    gradle = APP / "release-dependency-sources"
    pipeline.require(len(native) >= 4 and gradle.is_dir() and any(gradle.rglob("*.jar")),
                     "missing Android dependency sources from this assembly")
    # The full dependency graph includes extra checksums. Keep those in a copy
    # so collecting sources does not change the checkout the receipt covers.
    with tempfile.TemporaryDirectory(dir=output.resolve().parent) as scratch:
        module_file = Path(scratch) / "go.mod"
        for path in (module_file, module_file.with_suffix(".sum")):
            shutil.copyfile(pipeline.ROOT / path.name, path)
        modules = pipeline.run("go", "mod", "download", "-modfile", str(module_file), "-json", "all").decode().lstrip()
    # The download command writes a stream of individual JSON objects.
    decoder = json.JSONDecoder()
    with output.open("xb") as raw, gzip.GzipFile(fileobj=raw, filename="", mode="wb", mtime=0) as compressed, \
            tarfile.open(fileobj=compressed, mode="w") as archive:
        def normalized(entry):
            entry.uid = entry.gid = entry.mtime = 0
            entry.uname = entry.gname = ""
            return entry

        def add(path, name):
            archive.add(path, name, filter=normalized)

        for name in ("src", "VERSION", "LICENSE", "PATENTS"):
            add(goroot / name, f"go-toolchain/{name}")
        while modules:
            module, end = decoder.raw_decode(modules)
            add(Path(module["Zip"]), f"go-modules/{module['Path']}/@v/{module['Version']}.zip")
            modules = modules[end:].lstrip()
        for path in native:
            add(path, f"native/{path.name}")
        add(gradle, "maven")


def verify_trust(apk):
    base = os.environ["ENCORE_UPDATE_BASE_URL"]
    key = os.environ["ENCORE_UPDATE_PUBLIC_KEY"]
    pipeline.require(base.startswith("https://") and len(base64.b64decode(key, validate=True)) == 32,
                     "expected Android HTTPS base and raw Ed25519 public key")
    manifest = pipeline.run("aapt", "dump", "xmltree", str(apk), "AndroidManifest.xml").decode()
    for name, expected in (("com.encore.update_base_url", base), ("com.encore.update_public_key", key)):
        values = re.findall(r'A: android:name[^\n]*="' + re.escape(name)
                            + r'"[^\n]*\n\s*A: android:value[^\n]*?="([^"\n]*)"', manifest)
        pipeline.require(values == [expected], f"APK embedded {name} mismatch")


def prepare(work, output):
    apk, repeat = work / "encore-android.apk", work / "repeat.apk"
    verify_trust(apk)
    compatibility = pipeline.inspect_artifact({"target": "android-generic",
        "core_version": os.environ["ENCORE_CORE_VERSION"], "revision": int(os.environ["ENCORE_VERSION_CODE"])}, apk)
    pipeline.require(compatibility["version"] == os.environ["ENCORE_VERSION_NAME"], "APK versionName mismatch")
    certificate = os.environ["ANDROID_CERTIFICATE_SHA256"]
    pipeline.require(compatibility["certificate_sha256"] == certificate, "APK signer mismatch")
    hashes = [pipeline.digest(path) for path in (apk, repeat)]
    pipeline.require(hashes[0] == hashes[1], "repeat assembly must yield identical APK bytes")
    dependencies = work / "android-dependencies-source.tar.gz"
    add_sources(dependencies)
    evidence = work / "evidence.json"
    toolchain = "; ".join((pipeline.run("go", "version").decode().strip(),
                           pipeline.run("java", "--version").decode().splitlines()[0],
                           "Android SDK/Build Tools 36, NDK 28.0.13004108"))
    evidence.write_bytes(pipeline.canonical({
        "source": pipeline.run("git", "rev-parse", "HEAD").decode().strip(),
        "actions_run": os.environ.get("GITHUB_RUN_ID"), "assembly_sha256": hashes,
        "toolchain": toolchain, "apk": compatibility,
        "android_update_trust": {"base_url": os.environ["ENCORE_UPDATE_BASE_URL"],
                                 "public_key": os.environ["ENCORE_UPDATE_PUBLIC_KEY"]},
    }) + b"\n")
    pipeline.prepare({"target": "android-generic", "identity": "main", "artifact": str(apk),
                      "reproduced_artifact": str(repeat), "evidence": str(evidence), "toolchain": toolchain,
                      "recipe": ".github/workflows/android.yml", "review": "approved-for-distribution",
                      "certificate_sha256": certificate, "sources": {"dependencies": str(dependencies)}}, output)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("work", type=Path)
    parser.add_argument("output", nargs="?", type=Path, help="omit to verify the trust embedded in one APK")
    args = parser.parse_args()
    if args.output is None:
        verify_trust(args.work)
    else:
        prepare(args.work, args.output)
