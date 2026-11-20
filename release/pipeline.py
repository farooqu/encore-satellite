#!/usr/bin/env python3
"""Retain inspected artifacts and approve channel feeds.

No builds, key generation, device access or implicit publication. See
publishing.md for receipt review, protected signing and publication steps.
"""
import argparse
import base64
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import zipfile

from biscuit_dev import identity as biscuit_dev_identity
from check import ROOT, SEMVER, validate, validate_ledger

REPOSITORY = "farooqu/encore-satellite"
RECEIPT_TYPE = "encore-release-receipt+jws"
ANDROID_FEED_TYPE = "encore-android-update+jws"
ARM64_STARTER = "encore-biscuit-arm64-starter-v1"
NATIVE_STARTER = "encore-biscuit-native-ab-v1"
ARM64_FEED_TYPE = "encore-linux-update+jws"
BISCUIT_ARM64_ROOT = bytes.fromhex("302a300506032b6570032100") + base64.b64decode(
    "Ggx1WlubkJjSPNpKoaEDzPZYMzUxbiPunioZcmvxsuc=")  # Same root as BiscuitARM64Root in platforms/linuxupdate.


def arm64_starter(receipt):
    return receipt.get("compatibility", {}).get("format") in {ARM64_STARTER, NATIVE_STARTER}


def native_starter(receipt):
    return receipt.get("compatibility", {}).get("format") == NATIVE_STARTER


def require(condition, message):
    if not condition:
        raise ValueError(message)


def unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f"duplicate JSON field: {key}")
        result[key] = value
    return result


def read_json(path):
    return json.loads(path.read_bytes(), object_pairs_hook=unique)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def run(*command):
    return subprocess.check_output(command, cwd=ROOT, stderr=subprocess.PIPE)


def encode(value):
    return base64.urlsafe_b64encode(value).rstrip(b"=")


def decode(value):
    require(re.fullmatch(rb"[A-Za-z0-9_-]+", value), "invalid JWS encoding")
    return base64.urlsafe_b64decode(value + b"=" * (-len(value) % 4))


def sign(payload, kind, key, output):
    message = encode(canonical({"alg": "EdDSA", "typ": kind})) + b"." + encode(canonical(payload))
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "message"
        path.write_bytes(message)
        signature = run("openssl", "pkeyutl", "-sign", "-rawin", "-inkey", str(key), "-in", str(path))
    require(len(signature) == 64, "expected Ed25519 private key")
    with output.open("xb") as stream:
        stream.write(message + b"." + encode(signature) + b"\n")


def verify_jws(path, kind, public_key):
    public = run("openssl", "pkey", "-pubin", "-in", str(public_key), "-pubout", "-outform", "DER")
    require(len(public) == 44 and public.startswith(bytes.fromhex("302a300506032b6570032100")), "expected Ed25519 public root")
    data = path.read_bytes().strip()
    require(len(data) <= 128 * 1024, "oversized JWS")
    header, payload, signature = data.split(b".")
    require(decode(header) == canonical({"alg": "EdDSA", "typ": kind}), "wrong protected header")
    with tempfile.TemporaryDirectory() as directory:
        message, sig = Path(directory) / "message", Path(directory) / "signature"
        message.write_bytes(header + b"." + payload)
        sig.write_bytes(decode(signature))
        require(sig.stat().st_size == 64, "expected Ed25519 signature")
        run("openssl", "pkeyutl", "-verify", "-rawin", "-pubin", "-inkey", str(public_key),
            "-in", str(message), "-sigfile", str(sig))
    return json.loads(decode(payload), object_pairs_hook=unique)


def tag(receipt):
    if arm64_starter(receipt):
        return f"biscuit-{receipt['core_version']}"
    return f"{receipt['target']}-r{receipt['revision']}"


def asset_url(receipt, name):
    return f"https://github.com/{REPOSITORY}/releases/download/{tag(receipt)}/{name}"


def record(path):
    require(path.is_file() and not path.is_symlink(), f"expected regular file: {path}")
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", path.name), "unsafe asset filename")
    return {"name": path.name, "size": path.stat().st_size, "sha256": digest(path)}


def load_biscuit_starter(path, artifact_sha256):
    sys.path.insert(0, str(ROOT / "devices/biscuit/packaging"))
    from kernel61.starter_install import load_bundle
    return load_bundle(path, artifact_sha256)


def biscuit_lock(source):
    sys.path.insert(0, str(ROOT / "devices/biscuit/packaging"))
    from kernel61.locked_build import read_lock
    data = run("git", "show", f"{source}:devices/biscuit/packaging/kernel61/ci-inputs.json")
    lock = read_lock(data)
    require(lock["external_bundle_tag"] is not None, "ARM64 external inputs are not approved at the receipt source")
    return hashlib.sha256(data).hexdigest(), lock["sha256"]


def verify_biscuit_sources(receipt, manifest, sources):
    _, pins = biscuit_lock(receipt["source"])
    with tarfile.open(sources["alpine"]) as archive:
        for name in ("alpine-source.tar.gz", "go-source.tar.gz"):
            with archive.extractfile(name) as stream:
                require(hashlib.file_digest(stream, "sha256").hexdigest() == pins[name], f"unexpected {name}")
        for name in ("go.mod", "go.sum"):
            require(archive.extractfile(name).read() == run("git", "show", f"{receipt['source']}:{name}"),
                    f"source kit {name} differs from receipt source")
    with tarfile.open(sources["kernel"]) as archive:
        kernel = json.load(archive.extractfile("manifest.json"), object_pairs_hook=unique)
        require(kernel["source_sha256"] == pins["linux-source.tar.gz"] and
                kernel["files"]["Image"]["sha256"] == manifest["kernel_image_sha256"] and
                kernel["files"]["biscuit-arm64-probe.dtb"]["sha256"] == manifest["dtb_sha256"],
                "kernel source record differs from the locked build and starter")


def inspect_artifact(receipt, path, aapt="aapt", apksigner="apksigner"):
    if receipt["target"] == "biscuit":
        if native_starter(receipt):
            manifest, _ = inspect_native_pair(receipt, path)
            return {"format": manifest["format"], "base_sha256": manifest["base_sha256"]}
        manifest, rootfs, _ = load_biscuit_starter(path, receipt["artifact"]["sha256"])
        lock_sha, _ = biscuit_lock(receipt["source"])
        require(manifest.get("input_lock_sha256") == lock_sha, "starter external lock differs from receipt source")
        require(receipt["ota"] == {"name": "rootfs.tar.gz", "size": len(rootfs),
                                  "sha256": hashlib.sha256(rootfs).hexdigest()}, "OTA bytes differ from starter rootfs")
        require(manifest.get("recovery_rearm") is True and manifest.get("api_key_provisioning") is True
                and (manifest.get("core_version"), manifest["package_revision"]) ==
                (receipt["core_version"], receipt["revision"]), "wrong ARM64 starter identity or install policy")
        return {"format": manifest["format"], "base_sha256": manifest["retained_base_sha256"]}
    badging = run(aapt, "dump", "badging", str(path)).decode()
    certificates = run(apksigner, "verify", "--verbose", "--print-certs", str(path)).decode()
    manifest = run(aapt, "dump", "xmltree", str(path), "AndroidManifest.xml").decode()
    package = re.search(r"^package: name='([^']+)' versionCode='([0-9]+)' versionName='([^']+)'", badging, re.M)
    sdk = re.search(r"^sdkVersion:'([0-9]+)'", badging, re.M)
    abis = re.search(r"^native-code: (.+)$", badging, re.M)
    certs = re.findall(r"^Signer #[0-9]+ certificate SHA-256 digest: ([0-9a-f]{64})$", certificates, re.M)
    require(package and sdk and abis and len(certs) == 1, "missing APK identity/SDK/ABI/single signer")
    require(package[1] == "com.encore.satellite.incubator" and int(package[2]) == receipt["revision"], "wrong APK identity")
    require("application-debuggable" not in badging, "debug APK is not distributable")
    require("launchable-activity: name='com.encore.satellite.MainActivity'" in badging, "APK has no Encore launcher")
    require("uses-permission: name='android.permission.REQUEST_INSTALL_PACKAGES'" in badging, "APK cannot request update installation")
    # Read the compiled manifest, not an operator-supplied core version claim.
    require(re.search(r'com\.encore\.core_version.*?\n\s*A: android:value[^\n]*="'
                      + re.escape(receipt["core_version"]) + r'"', manifest), "APK embedded core mismatch")
    require(int(sdk[1]) >= 26, "unsupported APK minimum SDK")
    apk_abis = re.findall(r"'([^']+)'", abis[1])
    require(set(apk_abis) == {"arm64-v8a", "armeabi-v7a"}, "release APK must contain both ARM ABIs")
    with zipfile.ZipFile(path) as apk:
        require(all(f"lib/{abi}/libgojni.so" in apk.namelist() for abi in apk_abis), "APK missing Go engine for an advertised ABI")
    return {"application_id": package[1], "version": package[3], "min_sdk": int(sdk[1]),
            "abis": apk_abis, "certificate_sha256": certs[0]}


def inspect_native_pair(receipt, path):
    sys.path.insert(0, str(ROOT / "devices/biscuit/packaging"))
    from kernel61.starter_install import NATIVE_FILES, load_native_bundle, native_members
    manifest, files = load_native_bundle(path, receipt["artifact"]["sha256"])
    lock_sha, _ = biscuit_lock(receipt["source"])
    require(manifest["input_lock_sha256"] == lock_sha, "native seed external lock differs from receipt source")
    require((manifest["core_version"], manifest["package_revision"]) == (receipt["core_version"], receipt["revision"]),
            "wrong native seed version")
    ota = path.with_name(receipt["ota"]["name"])
    require(record(ota) == receipt["ota"] and native_members(ota, NATIVE_FILES) == files,
            "native OTA does not match the seed")
    return manifest, files


def validate_receipt(receipt):
    require(receipt["schema"] == 1 and receipt["repository"] == REPOSITORY, "wrong receipt schema/repository")
    require(receipt["target"] in {"biscuit", "android-generic"} and receipt["backend"] == "incubator", "wrong target/backend")
    require(type(receipt["revision"]) is int and 0 < receipt["revision"] <= 2100000000, "invalid revision")
    require(SEMVER.fullmatch(receipt["core_version"]) and re.fullmatch(r"[0-9a-f]{40}", receipt["source"]), "invalid core/source")
    require(receipt["toolchain"].strip() and receipt["recipe"].strip(), "exact toolchain/build recipe required")
    require(receipt["built_channel"] == "dev", "retain original dev build identity")
    require(receipt["reproduction"] == "byte-identical-reassembly", "repeat-assembly evidence required")
    review = "requires-signed-approval" if native_starter(receipt) else "approved-for-distribution"
    require(receipt["review"] == review, "source/notices/cohort review required")
    required = {"encore", "kernel", "alpine"} if receipt["target"] == "biscuit" else {"encore", "dependencies"}
    if "identity" in receipt:
        require(receipt["target"] == "android-generic" and receipt["identity"] == "main" and "reservation" not in receipt,
                "invalid main-derived receipt identity")
    if receipt["target"] == "biscuit":
        suffix = "-native-seed.zip" if native_starter(receipt) else ".zip"
        require(arm64_starter(receipt) and "reservation" not in receipt
                and receipt["artifact"]["name"] == f"encore-biscuit-arm64-{receipt['core_version']}{suffix}",
                "Biscuit requires an ARM64 starter named for its public dev version")
    require(set(receipt["sources"]) == required, "matching corresponding-source roles required")
    names = []
    items = [receipt["artifact"], receipt["evidence"], *receipt["sources"].values()]
    if arm64_starter(receipt):
        name, limit = ((f"encore-biscuit-arm64-{receipt['core_version']}-native-ota.zip", 129 << 20)
                       if native_starter(receipt) else ("rootfs.tar.gz", 96 << 20))
        require(receipt["ota"]["name"] == name and receipt["ota"]["size"] <= limit, "invalid Biscuit OTA asset")
        items.append(receipt["ota"])
    for item in items:
        name = item["name"]
        require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", name) and name not in {"receipt.json", "receipt.jws"}, "unsafe/reserved asset name")
        require(type(item["size"]) is int and item["size"] > 0 and re.fullmatch(r"[0-9a-f]{64}", item["sha256"]), "invalid asset size/hash")
        names.append(name)
    require(len(names) == len(set(names)), "duplicate asset names")
    require(receipt["artifact"]["size"] <= 256 * 1024 * 1024, "artifact exceeds download limit")
    compatibility = receipt["compatibility"]
    if receipt["target"] == "biscuit":
        require(set(compatibility) == {"format", "base_sha256"}
                and re.fullmatch(r"[0-9a-f]{64}", compatibility["base_sha256"]), "invalid Linux compatibility")
    else:
        require(set(compatibility) == {"application_id", "version", "min_sdk", "abis", "certificate_sha256"}
                and compatibility["application_id"] == "com.encore.satellite.incubator"
                and compatibility["version"] and type(compatibility["min_sdk"]) is int
                and compatibility["min_sdk"] >= 26
                and sorted(compatibility["abis"]) == ["arm64-v8a", "armeabi-v7a"]
                and re.fullmatch(r"[0-9a-f]{64}", compatibility["certificate_sha256"]), "invalid Android compatibility")
    return receipt


def validate_source(receipt):
    source = receipt["source"]
    if receipt.get("identity") == "main":
        require((receipt["core_version"], receipt["revision"]) == biscuit_dev_identity(source),
                "Android receipt differs from its main-derived identity")
        return
    if arm64_starter(receipt):
        require((receipt["core_version"], receipt["revision"]) == biscuit_dev_identity(source),
                "ARM64 receipt differs from its main-derived identity")
        names = {"kernel": "kernel-source.tar.gz", "alpine": "alpine-go-source.tar.gz"}
        require(all(receipt["sources"][role]["name"] == name for role, name in names.items()),
                "expected same-commit ARM64 source kits")
        return
    registry = validate(json.loads(run("git", "show", f"{source}:release/targets.json")))
    ledger = validate_ledger(json.loads(run("git", "show", f"{source}:release/ledger.json")), registry)
    reservation = next(r for r in ledger["reservations"] if r["id"] == receipt["reservation"])
    require("private_arm64_zip_sha256" not in reservation, "private install reservation cannot authorize release publication")
    target = next(t for t in registry["targets"] if t["id"] == receipt["target"])
    require(reservation["core_version"] == registry["core_version"] == receipt["core_version"]
            and reservation["revisions"][receipt["target"]] == receipt["revision"] == target["package_revision"],
            "receipt differs from source inventory/reservation")


def prepare(spec, output, aapt="aapt", apksigner="apksigner"):
    starter = spec["target"] == "biscuit"
    main_android = "identity" in spec
    if main_android:
        require(spec["target"] == "android-generic" and spec["identity"] == "main" and "reservation" not in spec,
                "Android main identity cannot be combined with a reservation")
    require(not run("git", "status", "--porcelain", "--untracked-files=normal"), "commit source before preparing a candidate")
    source = run("git", "rev-parse", "HEAD").decode().strip()
    if starter:
        require(spec.get("format") in {ARM64_STARTER, NATIVE_STARTER} and "reservation" not in spec,
                "expected ARM64 starter without reservation")
    if starter or main_android:
        core, revision = biscuit_dev_identity(source)
        require(revision <= 2_100_000_000, "main-derived revision exceeds limit")
    else:
        registry = validate(read_json(ROOT / "release/targets.json"))
        ledger = validate_ledger(read_json(ROOT / "release/ledger.json"), registry)
        reservation = next(r for r in ledger["reservations"] if r["id"] == spec["reservation"])
        require("private_arm64_zip_sha256" not in reservation, "private install reservation cannot authorize a release receipt")
        core, revision = reservation["core_version"], reservation["revisions"][spec["target"]]
        require(core == registry["core_version"], "reservation core differs from built source")
    paths = {"artifact": Path(spec["artifact"]), "evidence": Path(spec["evidence"])}
    reproduced = Path(spec["reproduced_artifact"])
    require(not paths["artifact"].samefile(reproduced)
            and record(paths["artifact"])["sha256"] == record(reproduced)["sha256"], "repeat assembly must use a distinct byte-identical output")
    sources = {role: Path(path) for role, path in spec["sources"].items()}
    require("encore" not in sources, "Encore source is generated from the exact Git commit")
    require(not output.exists(), "output directory must not exist")
    # Deterministic archive metadata comes from Git, gzip has no build-clock/name.
    buffer = io.BytesIO()
    with gzip.GzipFile(fileobj=buffer, mode="wb", filename="", mtime=0) as compressed:
        compressed.write(run("git", "archive", "--format=tar", source))
    archive = buffer.getvalue()
    receipt = {"schema": 1, "repository": REPOSITORY, "target": spec["target"],
               "backend": "incubator", "revision": revision,
               "core_version": core, "source": source, "built_channel": "dev",
               "reproduction": "byte-identical-reassembly",
               "toolchain": spec["toolchain"], "recipe": spec["recipe"], "review": spec["review"],
               "artifact": record(paths["artifact"]), "evidence": record(paths["evidence"]),
               "sources": {role: record(path) for role, path in sources.items()}}
    receipt["sources"]["encore"] = {"name": "encore-source.tar.gz", "size": len(archive),
                                          "sha256": hashlib.sha256(archive).hexdigest()}
    if starter and spec["format"] == NATIVE_STARTER:
        ota = Path(spec["ota"])
        repeat_ota = Path(spec["reproduced_ota"])
        require(ota.resolve().parent == paths["artifact"].resolve().parent and not ota.samefile(repeat_ota)
                and digest(ota) == digest(repeat_ota), "native OTA must be a matching, repeatable build output")
        paths["ota"] = ota
        receipt["compatibility"] = {"format": NATIVE_STARTER}
        receipt["ota"] = record(ota)
        manifest, _ = inspect_native_pair(receipt, paths["artifact"])
        verify_biscuit_sources(receipt, manifest, sources)
    elif starter:
        manifest, rootfs, _ = load_biscuit_starter(paths["artifact"], receipt["artifact"]["sha256"])
        receipt["ota"] = {"name": "rootfs.tar.gz", "sha256": hashlib.sha256(rootfs).hexdigest(), "size": len(rootfs)}
        from kernel61.starter_build import source_hashes
        hashes = source_hashes()
        tracked = set(run("git", "ls-files", "-z").decode().split("\0"))
        require(hashes.keys() <= tracked and manifest.get("sources") == hashes,
                "ARM64 build inputs differ from the clean release source")
        receipt["compatibility"] = {"format": ARM64_STARTER}
        verify_biscuit_sources(receipt, manifest, sources)
    elif main_android:
        receipt["identity"] = "main"
    else:
        receipt["reservation"] = spec["reservation"]
    receipt["compatibility"] = inspect_artifact(receipt, paths["artifact"], aapt, apksigner)
    validate_receipt(receipt)
    validate_source(receipt)
    if receipt["target"] == "android-generic":
        require(receipt["compatibility"]["certificate_sha256"] == spec["certificate_sha256"], "unexpected APK signer")
    output.mkdir(parents=True)
    for path in [*paths.values(), *sources.values()]:
        shutil.copyfile(path, output / path.name)
    if starter and not native_starter(receipt):
        (output / receipt["ota"]["name"]).write_bytes(rootfs)
    (output / "encore-source.tar.gz").write_bytes(archive)
    (output / "receipt.json").write_bytes(canonical(receipt) + b"\n")
    print(f"Retained {tag(receipt)}; receipt SHA256 {digest(output / 'receipt.json')}; not signed or published")


def prepare_native_ci(build, output):
    version, _ = biscuit_dev_identity(run("git", "rev-parse", "HEAD").decode().strip())
    asset = f"encore-biscuit-arm64-{version}"
    seed, ota = f"{asset}-native-seed.zip", f"{asset}-native-ota.zip"
    prepare({"target": "biscuit", "format": NATIVE_STARTER, "review": "requires-signed-approval",
             "artifact": str(build / "primary" / seed), "reproduced_artifact": str(build / "repeat" / seed),
             "ota": str(build / "primary" / ota), "reproduced_ota": str(build / "repeat" / ota),
             "evidence": str(build / "evidence.txt"), "toolchain": "pinned in CI workflow and external-input lock",
             "recipe": "devices/biscuit/packaging/kernel61/locked_build.py",
             "sources": {"kernel": str(build / "sources/kernel-source.tar.gz"),
                         "alpine": str(build / "sources/alpine-go-source.tar.gz")}}, output)


def verify(directory, public_key, expected):
    require(digest(directory / "receipt.json") == expected, "receipt SHA256 mismatch")
    receipt = validate_receipt(read_json(directory / "receipt.json"))
    if arm64_starter(receipt):
        require(run("openssl", "pkey", "-pubin", "-in", str(public_key), "-pubout", "-outform", "DER") ==
                BISCUIT_ARM64_ROOT, "wrong public key for Biscuit ARM64")
    require(verify_jws(directory / "receipt.jws", RECEIPT_TYPE, public_key) == receipt, "receipt signature payload mismatch")
    verify_assets(directory, receipt, signed=True)
    return receipt


def verify_assets(directory, receipt, *, signed):
    """Check the same retained bytes before offline signing and after transfer."""
    items = [receipt["artifact"], receipt["evidence"], *receipt["sources"].values()]
    if arm64_starter(receipt):
        items.append(receipt["ota"])
    expected = {"receipt.json", *(i["name"] for i in items)}
    if signed:
        expected.add("receipt.jws")
    require({p.name for p in directory.iterdir()} == expected, "missing/unrecorded release assets")
    for item in items:
        require(record(directory / item["name"]) == item, f"asset changed: {item['name']}")
    validate_source(receipt)
    if arm64_starter(receipt):
        path = directory / receipt["artifact"]["name"]
        require(inspect_artifact(receipt, path) == receipt["compatibility"],
                "ARM64 starter compatibility mismatch")
        with zipfile.ZipFile(path) as zipfile_:
            manifest = json.loads(zipfile_.read("manifest.json"), object_pairs_hook=unique)
        verify_biscuit_sources(receipt, manifest, {role: directory / item["name"] for role, item in receipt["sources"].items()})
    require(gzip.decompress((directory / receipt["sources"]["encore"]["name"]).read_bytes()) ==
            run("git", "archive", "--format=tar", receipt["source"]), "Encore archive differs from exact source")


def metadata(receipt, channel, issued, expires):
    require(channel in {"dev", "stable"}, "invalid channel")
    require(channel != "stable" or "-" not in receipt["core_version"].split("+", 1)[0], "stable cannot advertise a prerelease")
    require(type(issued) is int and type(expires) is int and issued > 0 and 0 < expires - issued <= 7 * 86400, "invalid feed lifetime")
    if arm64_starter(receipt):
        ota = receipt["ota"]
        if native_starter(receipt):
            return {"target": "biscuit-native-ab", "backend": "incubator", "channel": channel,
                    "format": NATIVE_STARTER, "base_sha256": receipt["compatibility"]["base_sha256"],
                    "core_version": receipt["core_version"], "revision": receipt["revision"],
                    "url": asset_url(receipt, ota["name"]), "size": ota["size"], "sha256": ota["sha256"],
                    "issued": issued, "expires": expires}
        return {"target": "biscuit-arm64", "backend": "incubator", "channel": channel,
                "format": "encore-biscuit-arm64-rootfs-v1", "base_sha256": receipt["compatibility"]["base_sha256"],
                "core_version": receipt["core_version"], "revision": receipt["revision"],
                "url": asset_url(receipt, ota["name"]), "size": ota["size"], "sha256": ota["sha256"],
                "issued": issued, "expires": expires}
    require(receipt["target"] == "android-generic", "unsupported feed target")
    artifact = receipt["artifact"]
    result = {"target": receipt["target"], "backend": receipt["backend"], "channel": channel,
              "core_version": receipt["core_version"], "revision": receipt["revision"],
              "url": asset_url(receipt, artifact["name"]), "size": artifact["size"],
              "sha256": artifact["sha256"], "issued": issued, "expires": expires,
              **receipt["compatibility"]}
    result.update(source=receipt["source"], toolchain=receipt["toolchain"])
    return result


def core_key(version):
    # SemVer comparison, matching the existing clients (build metadata ignored).
    match = SEMVER.fullmatch(version)
    require(match is not None, "invalid core SemVer")
    prerelease = match[4]
    return (*map(int, match.group(1, 2, 3)), prerelease is None,
            tuple((0, int(part)) if part.isdigit() else (1, part) for part in prerelease.split(".")) if prerelease else ())


def stage_feed(receipt, feed, public_key, site, channel, now=None):
    now = int(time.time()) if now is None else now
    feed_type = ARM64_FEED_TYPE if arm64_starter(receipt) else ANDROID_FEED_TYPE
    signed = verify_jws(feed, feed_type, public_key)
    require(len(feed.read_bytes()) <= 32 * 1024, "feed exceeds device limit")
    require(signed == metadata(receipt, channel, signed["issued"], signed["expires"]), "feed differs from retained artifact")
    require(signed["issued"] <= now + 300 and signed["expires"] > now, "future or expired feed")
    target = signed["target"]
    destination = site / target / "incubator" / f"{channel}.jws"
    history = site / "history" / target / channel / f"{signed['issued']}.jws"
    previous_entries = list(history.parent.glob("*.jws"))
    if previous_entries:
        require(all(re.fullmatch(r"[1-9][0-9]*", p.stem) for p in previous_entries), "invalid feed history filename")
        latest = max(previous_entries, key=lambda p: int(p.stem))
        require(destination.is_file() and destination.read_bytes() == latest.read_bytes(), "current feed differs from latest retained checkpoint")
    else:
        require(not destination.exists(), "current feed has no retained checkpoint")
    if destination.exists():
        if destination.read_bytes() == feed.read_bytes():
            require(history.is_file() and history.read_bytes() == feed.read_bytes(), "missing/changed feed history")
            return  # Retry a failed Pages deploy without changing a checkpoint.
        previous = verify_jws(destination, feed_type, public_key)
        require((previous["target"], previous["backend"], previous["channel"]) ==
                (target, "incubator", channel), "previous feed identity mismatch")
        require(signed["revision"] >= previous["revision"] and signed["issued"] > previous["issued"], "feed revision/issue time must not roll back")
        if signed["revision"] == previous["revision"]:
            require({k: v for k, v in signed.items() if k not in {"issued", "expires"}} ==
                    {k: v for k, v in previous.items() if k not in {"issued", "expires"}}, "renewal cannot replace identity/bytes")
        else:
            require(core_key(signed["core_version"]) >= core_key(previous["core_version"]), "feed core must not roll back")
    require(not history.exists(), "feed history is append-only")
    history.parent.mkdir(parents=True, exist_ok=True)
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(feed, history)
    shutil.copyfile(feed, destination)
    (site / ".nojekyll").touch()


def publish(directory, receipt, execute, *, automatic_dev=False):
    release_tag = tag(receipt)
    changelog = f"release/changelogs/{receipt['core_version']}.md"
    if arm64_starter(receipt):
        source = receipt["source"]
        summary = ("Biscuit ARM64 native A/B pilot. The seed ZIP is for a fresh install; the OTA ZIP requires a signed native feed."
                   if native_starter(receipt) else
                   "Biscuit ARM64 development build. Use the ZIP for a fresh install; rootfs.tar.gz requires a signed runtime OTA feed.")
        notes = (summary + "\n\n"
                 f"[Source commit](https://github.com/{REPOSITORY}/commit/{source}) · "
                 f"[Install guide](https://github.com/{REPOSITORY}/blob/{source}/devices/biscuit/packaging/kernel61/README.md)\n")
    elif run("git", "ls-tree", "--name-only", receipt["source"], "--", changelog).strip():
        notes = run("git", "show", f"{receipt['source']}:{changelog}").decode()
    else:
        # Older manually reserved snapshots predate generated changelogs. Do
        # not invent a previous release: include their complete commit history.
        notes = "Initial retained experimental snapshot; not stable qualification. No channel activation.\n\n" + run(
            "git", "log", "--reverse", "--format=- %s (%H)", receipt["source"]).decode()
    commands = [
        ["gh", "release", "create", release_tag, "--repo", REPOSITORY, "--target", receipt["source"],
         "--draft", "--prerelease", "--title", (f"Biscuit ARM64 {receipt['core_version']}" if arm64_starter(receipt)
                                               else f"{release_tag} · {receipt['core_version']}"), "--notes", notes],
        ["gh", "release", "upload", release_tag, "--repo", REPOSITORY,
         *[str(p) for p in sorted(directory.iterdir())]],
        ["gh", "release", "edit", release_tag, "--repo", REPOSITORY, "--draft=false", "--latest=false"],
    ]
    if not execute:
        print(json.dumps({"mode": "dry-run", "commands": commands}, indent=2))
        return
    if not automatic_dev:
        # The manual path uses an admin-read credential only for this check.
        import os
        token = os.environ.get("IMMUTABILITY_READ_TOKEN")
        require(token, "IMMUTABILITY_READ_TOKEN required for repository immutability preflight")
        settings = subprocess.check_output(["gh", "api", f"repos/{REPOSITORY}/immutable-releases"],
                                           env={**os.environ, "GH_TOKEN": token}, stderr=subprocess.PIPE)
        require(json.loads(settings).get("enabled") is True, "enable immutable releases before publication")
    # Existing tag OR draft means stop; inspect any partial failure manually.
    existing = run("gh", "api", f"repos/{REPOSITORY}/git/matching-refs/tags/{release_tag}")
    require(not any(r["ref"] == f"refs/tags/{release_tag}" for r in json.loads(existing)), "release tag already exists")
    releases = json.loads(run("gh", "api", "--paginate", "--slurp", f"repos/{REPOSITORY}/releases?per_page=100"))
    require(not any(r["tag_name"] == release_tag for page in releases for r in page), "release/draft already exists; inspect, never overwrite")
    for command in commands[:2]:
        subprocess.run(command, check=True)
    # Verify remote retained source AND binary bytes before locking the release.
    with tempfile.TemporaryDirectory() as temporary:
        subprocess.run(["gh", "release", "download", release_tag, "--repo", REPOSITORY, "--dir", temporary], check=True)
        require(sorted((record(p) for p in Path(temporary).iterdir()), key=lambda r: r["name"]) ==
                sorted((record(p) for p in directory.iterdir()), key=lambda r: r["name"]), "uploaded assets differ; leave draft unpublished")
    subprocess.run(commands[2], check=True)
    published = json.loads(run("gh", "api", f"repos/{REPOSITORY}/releases/tags/{release_tag}"))
    require(published.get("immutable") is True,
            f"published release immutability not confirmed: https://github.com/{REPOSITORY}/releases/tag/{release_tag}; "
            "inspect the release before retrying; do not activate feeds")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prepare_parser = commands.add_parser("prepare")
    prepare_parser.add_argument("spec", type=Path)
    prepare_parser.add_argument("--output", type=Path, required=True)
    prepare_parser.add_argument("--aapt", default="aapt")
    prepare_parser.add_argument("--apksigner", default="apksigner")
    native = commands.add_parser("prepare-native-ci")
    native.add_argument("build", type=Path)
    native.add_argument("--output", type=Path, required=True)
    android = commands.add_parser("inspect-android", help="verify a signed fresh-install/OTA release APK")
    android.add_argument("apk", type=Path)
    android.add_argument("--revision", required=True, type=int)
    android.add_argument("--version", required=True)
    android.add_argument("--core-version", help="Expected embedded core version (defaults to legacy inventory)")
    android.add_argument("--certificate-sha256", required=True)
    android.add_argument("--aapt", default="aapt")
    android.add_argument("--apksigner", default="apksigner")
    signer = commands.add_parser("sign-receipt")
    signer.add_argument("directory", type=Path)
    signer.add_argument("--key", type=Path, required=True)
    signer.add_argument("--receipt-sha256", help="independently reviewed receipt hash; required for ARM64")
    for name in ("verify", "publish", "feed", "stage-feed"):
        sub = commands.add_parser(name)
        sub.add_argument("directory", type=Path)
        sub.add_argument("--public-key", type=Path, required=True)
        sub.add_argument("--receipt-sha256", required=True)
        if name == "publish":
            sub.add_argument("--execute", action="store_true")
            sub.add_argument("--automatic-dev", action="store_true", help="Use the repository's required immutability setting instead of an admin preflight")
        if name in {"feed", "stage-feed"}:
            sub.add_argument("--channel", choices=("dev", "stable"), required=True)
        if name == "feed":
            sub.add_argument("--issued", type=int, default=int(time.time()))
            sub.add_argument("--expires", type=int, required=True)
            sub.add_argument("--key", type=Path, required=True)
            sub.add_argument("--output", type=Path, required=True)
        if name == "stage-feed":
            sub.add_argument("--feed", type=Path, required=True)
            sub.add_argument("--site", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "prepare":
        prepare(read_json(args.spec), args.output, args.aapt, args.apksigner)
    elif args.command == "prepare-native-ci":
        prepare_native_ci(args.build, args.output)
    elif args.command == "inspect-android":
        receipt = {"target": "android-generic", "revision": args.revision,
                   "core_version": args.core_version or read_json(ROOT / "release/targets.json")["core_version"]}
        require(SEMVER.fullmatch(receipt["core_version"]) and 0 < args.revision <= 2_100_000_000, "invalid core or revision")
        apk = inspect_artifact(receipt, args.apk, args.aapt, args.apksigner)
        require(apk["version"] == args.version, "unexpected APK versionName")
        require(apk["certificate_sha256"] == args.certificate_sha256, "unexpected APK signer")
        print(json.dumps({**apk, "core_version": receipt["core_version"], "revision": args.revision}, sort_keys=True))
    elif args.command == "sign-receipt":
        data = (args.directory / "receipt.json").read_bytes()
        receipt = validate_receipt(json.loads(data, object_pairs_hook=unique))
        main_android = receipt.get("identity") == "main"
        if args.receipt_sha256 is not None or arm64_starter(receipt) or main_android:
            require(hashlib.sha256(data).hexdigest() == args.receipt_sha256, "reviewed receipt SHA256 mismatch or missing")
        if main_android or arm64_starter(receipt):
            verify_assets(args.directory, receipt, signed=False)
        if arm64_starter(receipt):
            require(run("openssl", "pkey", "-in", str(args.key), "-pubout", "-outform", "DER") ==
                    BISCUIT_ARM64_ROOT, "wrong signing key for Biscuit ARM64")
        sign(receipt, RECEIPT_TYPE, args.key, args.directory / "receipt.jws")
    else:
        receipt = verify(args.directory, args.public_key, args.receipt_sha256)
        if args.command == "publish":
            publish(args.directory, receipt, args.execute, automatic_dev=args.automatic_dev)
        elif args.command == "feed":
            feed_type = ARM64_FEED_TYPE if arm64_starter(receipt) else ANDROID_FEED_TYPE
            if arm64_starter(receipt):
                require(run("openssl", "pkey", "-in", str(args.key), "-pubout", "-outform", "DER") ==
                        BISCUIT_ARM64_ROOT, "wrong signing key for Biscuit ARM64")
            sign(metadata(receipt, args.channel, args.issued, args.expires), feed_type, args.key, args.output)
        elif args.command == "stage-feed":
            stage_feed(receipt, args.feed, args.public_key, args.site, args.channel)
        else:
            print(f"Verified {tag(receipt)} and all retained sources/assets")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, StopIteration, subprocess.SubprocessError, zipfile.BadZipFile) as error:
        # Do not echo subprocess stderr: signing tools can include private paths.
        sys.exit(f"Release pipeline refused: {error}")
