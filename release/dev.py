#!/usr/bin/env python3
"""Plan green-main dev cohorts offline; apply/build only behind explicit setup.

Versions and revisions live in targets.json and the existing append-only ledger.
Only retain --execute publishes assets; feed activation remains separate.
Offline signing never builds, downloads, uploads or generates keys.
"""
import argparse
import copy
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import time
import zlib

import pipeline as p
from check import ROOT, select, validate, validate_ledger

TARGETS = ("biscuit", "android-generic")
CONFIG = ROOT / "release/dev-automation.json"


def git(*args):
    return p.run("git", *args).decode().strip()


def inventory():
    registry = validate(p.read_json(ROOT / "release/targets.json"))
    ledger = validate_ledger(p.read_json(ROOT / "release/ledger.json"), registry)
    p.require({t["id"] for t in registry["targets"] if t["status"] == "experimental"} == set(TARGETS),
              "review the automated cohort when experimental targets change")
    return registry, ledger


def config():
    value = p.read_json(CONFIG)
    p.require(value["schema"] == 1 and all(type(value[name]) is bool for name in ("builds_enabled", "publishing_enabled")),
              "invalid dev automation policy")
    p.require(value.get("metadata_signing") == "offline", "metadata keys must remain outside automatic CI")
    return value


def paths(commit):
    # --root admits the first commit; -m handles merge commits conservatively.
    return p.run("git", "diff-tree", "--root", "-m", "--no-commit-id", "--name-only", "-r", "-z", commit).decode().strip("\0").split("\0")


def metadata_commit(commit):
    message = git("show", "-s", "--format=%B", commit)
    marker = re.search(r"^Encore-Dev-Source: ([0-9a-f]{40})$", message, re.M)
    if not marker:
        return False
    allowed = {"release/targets.json", "release/ledger.json"}
    changed = paths(commit)
    return (git("rev-list", "--parents", "-n1", commit).split()[1:] == [marker[1]]
            and bool(changed) and all(name in allowed or re.fullmatch(r"release/changelogs/[0-9A-Za-z.+-]+\.md", name)
                                      for name in changed))


def published_sources(releases):
    """Only immutable, published per-target tags establish changelog baselines."""
    previous = {target: None for target in TARGETS}
    highest = {target: 0 for target in TARGETS}
    for item in releases:
        match = re.fullmatch(r"(biscuit|android-generic)-r([1-9][0-9]*)", item["tag_name"])
        if not match or item.get("draft") or item.get("immutable") is not True:
            continue
        target, revision = match[1], int(match[2])
        if revision > highest[target]:
            previous[target] = git("rev-parse", "--verify", f"refs/tags/{item['tag_name']}^{{commit}}")
            highest[target] = revision
    return previous


def ci_ready(workflow_runs, source, repository=p.REPOSITORY):
    # The caller supplies runs from each expected workflow's specific API URL.
    for runs in workflow_runs:
        matching = [r for r in runs if r["head_sha"] == source and r["event"] == "push"
                    and r["head_branch"] == "main" and r["head_repository"]["full_name"] == repository]
        if not matching:
            return False
        latest = max(matching, key=lambda r: (r["run_number"], r["run_attempt"]))
        if latest["status"] != "completed" or latest["conclusion"] != "success":
            return False
    return len(workflow_runs) == 2


def plan(previous=None):
    registry, ledger = inventory()
    source = git("rev-parse", "HEAD")
    previous = previous if previous is not None else {target: None for target in TARGETS}
    p.require(set(previous) == set(TARGETS), "explicit baseline for every cohort member required")
    changes = {}
    for target, base in previous.items():
        if base:
            p.require(re.fullmatch(r"[0-9a-f]{40}", base), "full baseline SHA required")
            p.run("git", "merge-base", "--is-ancestor", base, source)
        commits = git("rev-list", "--reverse", f"{base}..{source}" if base else source).splitlines()
        changes[target] = [{"source": commit, "subject": git("show", "-s", "--format=%s", commit)}
                           for commit in commits if not metadata_commit(commit)
                           and target in select(paths(commit), registry)["affected_targets"]]
    eligible = any(changes.values()) and not metadata_commit(source)
    match = re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-dev\.(0|[1-9][0-9]*)", registry["core_version"])
    p.require(not eligible or match, "automation advances only an explicitly selected X.Y.Z-dev.N line")
    version = ".".join(match.group(1, 2, 3)) + f"-dev.{int(match[4]) + 1}" if eligible else None
    revisions = {t["id"]: t["package_revision"] + 1 for t in registry["targets"] if t["id"] in TARGETS} if eligible else {}
    policy = config()
    return {"schema": 1, "source": source, "previous": previous, "eligible": eligible,
            "core_version": version, "reservation": f"main-dev-{source}" if eligible else None,
            "revisions": revisions, "changes": changes,
            "builds_enabled": policy["builds_enabled"],
            "inputs_configured": policy["inputs"] is not None and bool(policy["distribution_review"]),
            "publishing_enabled": policy["publishing_enabled"]}


def notes(value):
    lines = [f"# {value['core_version'] or 'No dev release'}", "",
             "Experimental dev cohort; not stable qualification. No device installation is performed.", "",
             f"Source: https://github.com/{p.REPOSITORY}/commit/{value['source']}", ""]
    for target in TARGETS:
        base = value["previous"][target]
        lines += [f"## {target}", "", f"Revision: {value['revisions'].get(target, 'not allocated')}", ""]
        if base:
            lines += [f"Changes: https://github.com/{p.REPOSITORY}/compare/{base}...{value['source']}", ""]
        else:
            lines += ["Initial retained release: changes from repository inception.", ""]
        for entry in value["changes"][target]:
            subject = re.sub(r"([\\`*_{}\[\]<>])", r"\\\1", entry["subject"])
            lines.append(f"- {subject} ([{entry['source'][:10]}](https://github.com/{p.REPOSITORY}/commit/{entry['source']}))")
        if not value["changes"][target]:
            lines.append("- No target-specific changes; rebuilt for the shared core identity.")
        lines.append("")
    return "\n".join(lines)


def apply(value):
    p.require(value == plan(value["previous"]), "stale or modified dev plan; re-plan on current main")
    p.require(value["eligible"] and value["builds_enabled"], "dev builds are disabled or nothing changed")
    p.require(not git("status", "--porcelain", "--untracked-files=normal"), "clean committed source required")
    # Verify all configured retained inputs before consuming a reservation.
    validate_inputs()
    registry, ledger = inventory()
    old = copy.deepcopy(ledger)
    registry["core_version"] = value["core_version"]
    for target in registry["targets"]:
        if target["id"] in TARGETS:
            target["package_revision"] = value["revisions"][target["id"]]
    ledger["reservations"].append({"id": value["reservation"], "core_version": value["core_version"],
                                    "backend": "incubator", "channel": "dev", "revisions": value["revisions"],
                                    "source_commit": value["source"]})
    validate_ledger(ledger, validate(registry), previous=old)
    changelog = ROOT / "release/changelogs" / f"{value['core_version']}.md"
    p.require(not changelog.exists(), "changelog identity already exists")
    for name, data in (("targets.json", registry), ("ledger.json", ledger)):
        (ROOT / "release" / name).write_text(json.dumps(data, indent=2) + "\n")
    changelog.parent.mkdir(exist_ok=True)
    changelog.write_text(notes(value))


def dependency_paths(root=None):
    # Bind pre-reviewed dependency source closure to the recipes/manifests that
    # select it. Native kernel/Sendspin provenance has additional real loaders.
    root = root or ROOT
    return sorted({"go.mod", "go.sum", "devices/biscuit/packaging/base.sh",
                   "devices/biscuit/packaging/kernel.sh", "devices/biscuit/packaging/kernel-inputs.json",
                   "devices/biscuit/packaging/sendspin.sh", "platforms/linuxsendspin/build.py",
                   "platforms/linuxsendspin/dependencies.lock.json", "devices/android-generic/build-android",
                   *[str(path.relative_to(root)) for path in (root / "devices/android-generic").rglob("*.gradle.kts")
                     if "build" not in path.relative_to(root).parts], "devices/android-generic/gradle.properties"})


def validate_inputs(source_root=None):
    policy = config()
    inputs = policy["inputs"]
    p.require(policy["builds_enabled"] and isinstance(policy["distribution_review"], str)
              and policy["distribution_review"].strip() and isinstance(inputs, dict), "approved dev distribution/input setup required")
    source_root = source_root or ROOT
    p.require(inputs["dependency_sha256"] == {name: p.digest(source_root / name) for name in dependency_paths(source_root)},
              "dependency recipes changed; review/retain corresponding sources before releasing")
    p.require(set(inputs["files"]) == {"base", "kernel", "sendspin", "alpine", "android-dependencies"}, "complete retained input set required")
    names = []
    for item in inputs["files"].values():
        p.require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", item["name"])
                  and type(item["size"]) is int and item["size"] > 0
                  and re.fullmatch(r"[0-9a-f]{64}", item["sha256"]), "invalid retained input identity")
        # Only permanent versioned release assets in this repository. The owner
        # must approve their source completeness and immutability during setup.
        p.require(re.fullmatch(r"https://github\.com/farooqu/encore-satellite/releases/download/[A-Za-z0-9._-]+/"
                              + re.escape(item["name"]), item["url"]), "versioned immutable input URL required")
        names.append(item["name"])
    p.require(len(names) == len(set(names)), "retained input basenames must be unique")
    return policy


def initial_plan(source_tree):
    """Select existing reservations without altering their source or allocating."""
    value = p.read_json(ROOT / "release/initial-dev.json")
    p.require(value["schema"] == 1 and re.fullmatch(r"[0-9a-f]{40}", value["source"]), "pinned initial source required")
    p.require(set(value["revisions"]) == set(value["reservations"]) == set(value["expected_artifact_sha256"]) == set(TARGETS),
              "complete initial cohort required")
    p.require(all(sha is None or re.fullmatch(r"[0-9a-f]{64}", sha)
                  for sha in value["expected_artifact_sha256"].values()), "invalid prior candidate digest")
    p.require(not git("status", "--porcelain", "--untracked-files=normal"), "clean committed tooling required")
    source_tree = source_tree.resolve()
    p.require(p.run("git", "-C", str(source_tree), "rev-parse", "--show-toplevel").decode().strip() == str(source_tree),
              "initial source must be a checkout root")
    p.require(p.run("git", "-C", str(source_tree), "rev-parse", "HEAD").decode().strip() == value["source"]
              and not p.run("git", "-C", str(source_tree), "status", "--porcelain", "--untracked-files=normal").strip(),
              "clean exact initial source checkout required")
    p.run("git", "merge-base", "--is-ancestor", value["source"], "HEAD")
    current_registry, current_ledger = inventory()
    registry = validate(p.read_json(source_tree / "release/targets.json"))
    ledger = validate_ledger(p.read_json(source_tree / "release/ledger.json"), registry)
    validate_ledger(current_ledger, current_registry, previous=ledger)
    p.require(registry["core_version"] == current_registry["core_version"] == value["core_version"], "initial core is stale")
    for target in TARGETS:
        for catalog in (registry, current_registry):
            entry = next(t for t in catalog["targets"] if t["id"] == target)
            p.require(entry["package_revision"] == value["revisions"][target], "initial revision is not current")
        reservation = next(r for r in ledger["reservations"] if r["id"] == value["reservations"][target])
        p.require(reservation["core_version"] == value["core_version"] and reservation["channel"] == "dev"
                  and reservation["revisions"].get(target) == value["revisions"][target], "initial reservation mismatch")
        p.require(not git("tag", "--list", f"{target}-r{value['revisions'][target]}"), "initial release tag already exists")
    validate_inputs(source_tree)
    value["builder_source"] = git("rev-parse", "HEAD")
    value["previous"] = {target: None for target in TARGETS}
    commits = git("rev-list", "--reverse", value["source"]).splitlines()
    value["changes"] = {target: [{"source": commit, "subject": git("show", "-s", "--format=%s", commit)}
                                 for commit in commits if not metadata_commit(commit)
                                 and target in select(paths(commit), registry)["affected_targets"]] for target in TARGETS}
    return value


def fetch_inputs(output):
    policy = validate_inputs()
    output.mkdir(parents=True, exist_ok=True)
    # No floating Alpine repositories or source-kit regeneration during release.
    for role, item in policy["inputs"]["files"].items():
        destination = output / item["name"]
        if not destination.exists():
            # Inspect the actual input release's immutable flag, not its URL.
            release_tag = item["url"].split("/")[-2]
            release = json.loads(p.run("gh", "api", f"repos/{p.REPOSITORY}/releases/tags/{release_tag}"))
            p.require(release.get("immutable") is True and release.get("draft") is False, "input release must be immutable/published")
            subprocess.run(["curl", "--fail", "--silent", "--show-error", "--location", "--proto", "=https",
                            "--proto-redir", "=https", item["url"], "--output", str(destination)], check=True)
        p.require(p.record(destination) == {key: item[key] for key in ("name", "size", "sha256")}, f"retained input changed: {role}")
    return {role: output / item["name"] for role, item in policy["inputs"]["files"].items()}


def build(output, aapt, apksigner, initial=None):
    """Assemble both targets twice from reviewed retained inputs; no uploading."""
    policy = validate_inputs()
    registry, ledger = inventory()
    source = git("rev-parse", "HEAD")
    p.require(not git("status", "--porcelain", "--untracked-files=normal"), "clean committed source required")
    if initial is None:
        p.require(metadata_commit(source), "automated reservation commit required")
        reservation = ledger["reservations"][-1]
        p.require(reservation["id"] == "main-dev-" + git("rev-parse", "HEAD^"), "unexpected dev reservation")
        reservations = {target: reservation["id"] for target in TARGETS}
    else:
        p.require(source == initial["source"], "initial source changed")
        reservations = initial["reservations"]
    revisions = {t["id"]: t["package_revision"] for t in registry["targets"] if t["id"] in TARGETS}
    p.require(not output.exists(), "candidate output must be new")
    work = ROOT / "build/dev-work"
    p.require(not work.exists(), "use a fresh release checkout for each assembly attempt")
    work.mkdir(parents=True)
    inputs = fetch_inputs(work / "inputs")
    for role in ("kernel", "sendspin"):
        (work / role).mkdir()
        with tarfile.open(inputs[role], "r:gz") as archive:
            archive.extractall(work / role, filter="data")
        # Preserve original provenance names inside the native artifact, but
        # give the separately delivered source kits distinct asset basenames.
        shutil.copyfile(work / role / "source.tar.gz", work / f"{role}-source.tar.gz")
    for role in ("alpine", "android-dependencies"):
        shutil.copyfile(inputs[role], work / f"{role}-source.tar.gz")
    subprocess.run(["make", "check", "biscuit-check"], cwd=ROOT, check=True)
    artifacts = {}
    for attempt in (1, 2):
        subprocess.run(["make", "biscuit-build"], cwd=ROOT, check=True)
        bundle = work / f"biscuit-{attempt}.zip"
        subprocess.run([sys.executable, "devices/biscuit/packaging/package.py", "--base", str(inputs["base"]),
                        "--binary", "build/biscuit/encore-satellite", "--wmtup", "build/biscuit/wmtup", "--btbridge", "build/biscuit/btbridge",
                        "--kernel-artifact", str(work / "kernel"), "--sendspin-artifact", str(work / "sendspin"),
                        "--revision", str(revisions["biscuit"]), "--output", str(bundle)], cwd=ROOT, check=True)
        # A clean Gradle rebuild and a regenerated Go AAR, not a copied APK.
        # The pinned Android plugin exposes unit tests for the debug variant.
        subprocess.run(["devices/android-generic/build-android", "clean", ":app:assembleRelease",
                        ":app:testDebugUnitTest", ":app:lintRelease"], cwd=ROOT, check=True,
                       env={**os.environ, "ENCORE_VERSION_CODE": str(revisions["android-generic"]),
                            "ENCORE_VERSION_NAME": registry["core_version"]})
        apk = work / f"android-{attempt}.apk"
        shutil.copyfile(ROOT / "devices/android-generic/app/build/outputs/apk/release/app-release.apk", apk)
        artifacts[attempt] = {"biscuit": bundle, "android-generic": apk}
    hashes = {target: [p.digest(artifacts[attempt][target]) for attempt in (1, 2)] for target in TARGETS}
    p.require(all(pair[0] == pair[1] for pair in hashes.values()), "non-reproducible cohort; do not sign/publish either target")
    if initial:
        for target, expected in initial["expected_artifact_sha256"].items():
            p.require(expected is None or hashes[target][0] == expected,
                      f"prior {target} candidate differs; preserve its identity and coordinate a new reservation")
    evidence = work / "evidence.json"
    evidence.write_bytes(p.canonical({"source": source, "distribution_review": policy["distribution_review"],
                                      "inputs": policy["inputs"], "assembly_sha256": hashes,
                                      "initial_plan": initial,
                                      "actions_run": os.environ.get("GITHUB_RUN_ID"),
                                      "android_update_trust": {"base_url": os.environ.get("ENCORE_UPDATE_BASE_URL"),
                                                               "public_key": os.environ.get("ENCORE_UPDATE_PUBLIC_KEY")},
                                      "toolchain": {"go": git_tool("go", "version"), "java": git_tool("java", "--version"),
                                                    "python": sys.version, "zlib": zlib.ZLIB_RUNTIME_VERSION,
                                                    "runner_image": os.environ.get("ImageVersion", "not recorded")},
                                      "checks": ["make check biscuit-check", "testDebugUnitTest lintRelease"],
                                      "hardware_qualified": False}) + b"\n")
    for target in TARGETS:
        spec = {"target": target, "reservation": reservations[target], "artifact": str(artifacts[1][target]),
                "reproduced_artifact": str(artifacts[2][target]), "evidence": str(evidence),
                "toolchain": git_tool("go", "version") + "; SDK36/NDK28.0.13004108/Java21; see evidence and pinned source recipes",
                "recipe": "python3 release/dev.py build; tooling source " + (initial["builder_source"] if initial else source),
                "review": "approved-for-distribution", "certificate_sha256": os.environ.get("EXPECTED_CERTIFICATE"),
                "sources": {"kernel": str(work / "kernel-source.tar.gz"), "sendspin": str(work / "sendspin-source.tar.gz"),
                            "alpine": str(work / "alpine-source.tar.gz")} if target == "biscuit" else
                           {"dependencies": str(work / "android-dependencies-source.tar.gz")}}
        spec_path = work / f"{target}.json"
        spec_path.write_bytes(p.canonical(spec))
        p.prepare(spec_path, output / target, aapt, apksigner)
    handoff = {"schema": 1, "source": source,
               "receipts": {target: p.digest(output / target / "receipt.json") for target in TARGETS}}
    (output / "handoff.json").write_bytes(p.canonical(handoff) + b"\n")
    summary = f"Candidate source: {source}\n\nOffline handoff SHA256: `{p.digest(output / 'handoff.json')}`\n"
    print(summary)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as stream:
            stream.write(summary)


def git_tool(*command):
    return p.run(*command).decode().strip()


def cohort(directory, expected, public=None):
    """Bind both retained targets to an independently reviewed CI handoff hash."""
    allowed = {"handoff.json", *TARGETS}
    if public is not None:
        allowed.update(f"{target}-{channel}.jws" for target in TARGETS for channel in ("dev", "stable"))
    p.require({path.name for path in directory.iterdir()} <= allowed, "unrecorded handoff files")
    p.require(p.digest(directory / "handoff.json") == expected, "handoff SHA256 mismatch")
    handoff = p.read_json(directory / "handoff.json")
    p.require(handoff["schema"] == 1 and set(handoff["receipts"]) == set(TARGETS), "incomplete cohort")
    receipts = {}
    for target in TARGETS:
        candidate = directory / target
        expected_receipt = handoff["receipts"][target]
        p.require(p.digest(candidate / "receipt.json") == expected_receipt, "receipt SHA256 mismatch")
        receipt = p.validate_receipt(p.read_json(candidate / "receipt.json"))
        p.require((receipt["target"], receipt["source"]) == (target, handoff["source"]), "candidate source/cohort mismatch")
        p.run("git", "merge-base", "--is-ancestor", receipt["source"], "HEAD")
        if public is None:
            p.verify_assets(candidate, receipt, signed=False)
        else:
            p.verify(candidate, public[target], expected_receipt)
        receipts[target] = receipt
    # Each receipt already validates its own reservation against the same exact
    # source inventory. The initial targets legitimately have separate entries.
    p.require(len({r["core_version"] for r in receipts.values()}) == 1, "mixed core cohort")
    return receipts


def sign(directory, expected, keys, public, aapt, apksigner, channel, issued, expires):
    """Offline handoff: inspect all bytes first; add only signatures/feed files."""
    p.require(all(not path.resolve().is_relative_to(parent.resolve())
                  for path in [*keys.values(), *public.values()] for parent in (ROOT, directory)),
              "signing keys and independently pinned public roots must be outside the checkout and handoff")
    receipts = cohort(directory, expected)
    # Do not approve a receipt merely because the build job supplied its hash.
    # Inspect embedded identities, the centrally committed reservation, sources,
    # artifact bytes and expected APK signer using the signer's trusted tools.
    feeds = {}
    for target, receipt in receipts.items():
        candidate = directory / target
        p.require(p.inspect_artifact(receipt, candidate / receipt["artifact"]["name"], aapt, apksigner)
                  == receipt["compatibility"], "candidate embedded identity mismatch")
        if target == "android-generic":
            p.require(receipt["compatibility"]["certificate_sha256"] == os.environ.get("EXPECTED_CERTIFICATE"), "unexpected APK signer")
        feeds[target] = p.metadata(receipt, channel, issued, expires)
        p.require(not (directory / f"{target}-{channel}.jws").exists(), "feed output already exists")
    # Nothing above reads a private key or writes signatures. Never partially
    # sign a cohort because its second target's bytes/identity were unchecked.
    for target, receipt in receipts.items():
        candidate = directory / target
        p.sign(receipt, p.RECEIPT_TYPE, keys[target], candidate / "receipt.jws")
        p.verify(candidate, public[target], p.digest(candidate / "receipt.json"))
        p.sign(feeds[target], p.FEED_TYPES[target], keys[target], directory / f"{target}-{channel}.jws")
    print("Signed both retained receipts and channel entries; no rebuild, network access or feed activation")


def retain(directory, expected, public, execute):
    """Online/public-key-only handoff; never build or activate a feed."""
    if execute:
        p.require(config()["publishing_enabled"], "publishing is disabled in dev-automation.json")
    receipts = cohort(directory, expected, public)
    # Validate the entire cohort before the first external write. A transfer
    # failure can leave a partial release set; inspect it, never blindly retry.
    for target, receipt in receipts.items():
        p.publish(directory / target, receipt, execute)


def main():
    global ROOT
    parser = argparse.ArgumentParser(description=__doc__)
    subs = parser.add_subparsers(dest="command", required=True)
    planner = subs.add_parser("plan")
    planner.add_argument("--releases", type=Path)
    planner.add_argument("--output", type=Path, required=True)
    planner.add_argument("--github-output", type=Path)
    planner.add_argument("--ci-runs", type=Path, nargs=2)
    allocator = subs.add_parser("apply")
    allocator.add_argument("plan", type=Path)
    subs.add_parser("dependency-hashes")
    builder = subs.add_parser("build")
    builder.add_argument("--output", type=Path, required=True)
    builder.add_argument("--aapt", required=True)
    builder.add_argument("--apksigner", required=True)
    builder.add_argument("--initial-source", type=Path, help="separate clean checkout of the pinned initial source")
    initial = subs.add_parser("initial-plan")
    initial.add_argument("--source-tree", type=Path, required=True)
    initial.add_argument("--releases", type=Path, required=True)
    initial.add_argument("--ci-runs", type=Path, nargs=2, required=True)
    initial.add_argument("--output", type=Path, required=True)
    for name in ("sign", "retain"):
        sub = subs.add_parser(name)
        sub.add_argument("directory", type=Path)
        sub.add_argument("--handoff-sha256", required=True)
        for target in TARGETS:
            sub.add_argument(f"--{target}-public", type=Path, required=True)
            if name == "sign":
                sub.add_argument(f"--{target}-key", type=Path, required=True)
        if name == "sign":
            sub.add_argument("--aapt", required=True)
            sub.add_argument("--apksigner", required=True)
            sub.add_argument("--channel", choices=("dev", "stable"), required=True)
            sub.add_argument("--issued", type=int, default=int(time.time()))
            sub.add_argument("--expires", type=int, required=True)
        else:
            sub.add_argument("--execute", action="store_true")
    args = parser.parse_args()
    if args.command == "plan":
        previous = published_sources(p.read_json(args.releases)) if args.releases else None
        value = plan(previous)
        ready = ci_ready([p.read_json(path)["workflow_runs"] for path in args.ci_runs], value["source"]) if args.ci_runs else False
        args.output.mkdir(parents=True, exist_ok=True)
        (args.output / "plan.json").write_bytes(p.canonical(value) + b"\n")
        (args.output / "changelog.md").write_text(notes(value))
        if args.github_output:
            with args.github_output.open("a") as stream:
                stream.write("enabled=" + str(value["eligible"] and value["builds_enabled"] and value["inputs_configured"] and ready).lower() + "\n")
        print(json.dumps({"eligible": value["eligible"], "builds_enabled": value["builds_enabled"],
                          "inputs_configured": value["inputs_configured"], "publishing_enabled": value["publishing_enabled"],
                          "ci_ready": ready, "proposed_core": value["core_version"], "proposed_revisions": value["revisions"]}))
    elif args.command == "apply":
        apply(p.read_json(args.plan))
    elif args.command == "dependency-hashes":
        print(json.dumps({name: p.digest(ROOT / name) for name in dependency_paths()}, indent=2))
    elif args.command == "initial-plan":
        value = initial_plan(args.source_tree)
        p.require(ci_ready([p.read_json(path)["workflow_runs"] for path in args.ci_runs], value["source"]),
                  "both latest exact-source main checks must pass")
        for release in p.read_json(args.releases):
            for target, revision in value["revisions"].items():
                match = re.fullmatch(re.escape(target) + r"-r([1-9][0-9]*)", release["tag_name"])
                p.require(not match or int(match[1]) < revision, "initial release/draft or newer revision already exists")
        args.output.mkdir(parents=True, exist_ok=False)
        (args.output / "plan.json").write_bytes(p.canonical(value) + b"\n")
        (args.output / "changelog.md").write_text(notes(value))
        print("Existing initial reservations validated; no allocation, build or publication")
    elif args.command in {"sign", "retain"}:
        public = {target: getattr(args, target.replace("-", "_") + "_public") for target in TARGETS}
        if args.command == "sign":
            keys = {target: getattr(args, target.replace("-", "_") + "_key") for target in TARGETS}
            sign(args.directory, args.handoff_sha256, keys, public, args.aapt, args.apksigner,
                 args.channel, args.issued, args.expires)
        else:
            retain(args.directory, args.handoff_sha256, public, args.execute)
    else:
        value = initial_plan(args.initial_source) if args.initial_source else None
        if value:
            # Keep trusted tooling/policy in this checkout; compile, inspect and
            # archive the separate untouched source. No overlays or cherry-picks.
            ROOT = p.ROOT = args.initial_source.resolve()
        build(args.output.resolve(), args.aapt, args.apksigner, value)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, StopIteration, subprocess.SubprocessError, tarfile.TarError) as error:
        sys.exit(f"Dev automation refused: {error}")
