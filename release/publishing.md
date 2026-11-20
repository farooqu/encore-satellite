# Development builds, immutable releases and explicit feed selection

Biscuit now builds only the Linux 6.1/AArch64 starter. The Linux 3.18/ARMv7
build and mixed Biscuit/Android cohort workflows are retired. Old reservations
and provenance remain historical records, not authorization to ship an old ZIP.

Building, signing, publishing immutable assets and activating an update feed
are separate steps. Biscuit's main-push workflow prepares unsigned candidates
only, once approved inputs are pinned. Android has its own APK checks and
protected signing workflow. Neither workflow activates a feed.

## Biscuit native A/B GitHub signing

The keyless Biscuit build uploads `biscuit-native-unsigned` after an exact main
build with the external inputs below. Its receipt binds the seed ZIP, the OTA
ZIP, the build evidence and matching sources. Both ZIPs must have the same
two boot images and the same runtime; repeat builds must match both ZIPs.
The seed is for a fresh install. The OTA is for existing native installs only.

Use the manual **Sign a reviewed Biscuit native candidate** workflow on
protected `main`, with the build run ID, exact source SHA and the separately
reviewed receipt SHA256. Provide `BISCUIT_METADATA_PRIVATE_KEY_PEM` as an
environment secret and `BISCUIT_METADATA_PUBLIC_KEY_PEM` as a variable in
`biscuit-signing`. The key must match the Ed25519 root compiled into Biscuit.
Restrict the environment to exact `main`, require approval and disable
administrator deployment bypass. Before adding the key, dispatch
`operation=check-gate` and verify it waits for environment approval; after
approval that run performs no checkout, download or secret access.
Then use `operation=sign` on reviewed workflow code.

The pilot uses an owner-approved solo trust model: append-only `main`, owner
review in Amp before direct pushes, and an owner-approved environment gate
that permits self-review. This is not independent review or protection from
compromise of that account; anyone allowed to push to main can change signing
code. Before adding other writers, require reviewed changes and independent
approval or move signing to a separately controlled repository or service.

The signing job never runs candidate code. It uploads `biscuit-native-signed`
with the seed, OTA and signed receipt. Select `stable` or `dev` to also produce
a separate signed feed artifact for that channel; select `none` for the base
if it does not need a feed. GitHub signing does not publish the release or
activate the feed. The existing retain and activate workflows accept the new
receipt format with target `biscuit` and core version; native feeds are stored
under `biscuit-native-ab/incubator/` so they cannot affect recovery installs.

For the pilot, install the first exact signed seed with the
[native installer](../devices/biscuit/packaging/kernel61/README.md#signed-native-ab-pilot).
After fresh enrollment, retain a newer ordinary release and activate its
signed **native** feed on the selected channel. Install through Home Assistant.
USB verification checks that the new version is running and confirmed, that
the former slot still holds its previous version, and that enrollment survives
a further reboot. A later ordinary update writes the opposite slot. Do not
mirror the new version onto both slots; that discards the rollback version.

## Biscuit ARM64 public dev starter

The ARM64 starter is a signed fresh-install download, not an OTA feed entry.
It uses plain SemVer: for example, tag `biscuit-0.1.7`, title
`Biscuit ARM64 0.1.7`, and asset `encore-biscuit-arm64-0.1.7.zip`.
`biscuit_dev.py` calculates the patch number from the first-parent distance on
`main` from the private dev.5 anchor. The same source gets the same
version on rerun; skipped numbers are fine. Do not force-push main, move the
anchor, rename older releases, or use the private R22 reservation. The device
still gets an internal increasing anti-rollback number above legacy revisions.
Neither a new registry entry nor an Android build is needed.

Dev is the initial publication channel, not a version suffix. The intended
promotion selects the same immutable version for the release channel without
rebuilding or retagging. For runtime OTA, such a promotion would sign new
channel metadata for the same rootfs bytes, not put the starter ZIP on a feed.
Release promotion is not implemented by the automatic dev workflow.

1. `devices/biscuit/packaging/kernel61/ci-inputs.json` locks only external
   bytes. CI fetches the pinned upstream Linux source and Go archives from
   their upstream URLs. Once approved for this use, retain the exact other
   four files (`alpine-apks.tar.gz`, `alpine-source.tar.gz`, `apk-tools-static.apk`,
   `audited-header.bin`) at an immutable tag and set `external_bundle_tag` in a
   reviewed commit. Null leaves GitHub artifact assembly disabled. Do not
   republish the upstream kernel input archive or include stock radio firmware.
   For a private, local build, supply all seven locked files directly to
   `locked_build.py --inputs`; no release tag or upload is required.
2. After Foundation checks, `biscuit-arm64-dev.yml` checks out the exact main
   commit, builds Linux and the DTB from that tree's recipe, and constructs
   Alpine from the locked APKs, not from an earlier Encore image. It emits one
   version's complete starter ZIP and matching unsigned runtime payload, with
   byte-identical repeat assemblies, the two corresponding source kits and
   evidence. Dispatch with a full first-parent main SHA to replay that commit
   and its lock. This checks repeat packaging; it does not claim identical
   rebuilds across arbitrary build hosts. Retention and approval of matching
   source and notices still require review before any distribution.
   Each starter works for a first Encore install on supported hardware without
   an earlier Encore version. Legacy installs can do a one-time fresh install
   via recovery/fastboot. The runtime archive is the ZIP's exact `rootfs.tar.gz`,
   not a delta or a signed update offer. Runtime OTA only accepts matching
   recovery compatibility (kernel, DTB and recovery protocol). Treat incompatible
   releases as a maintenance/recovery install, not a runtime feed update for
   the installed cohort. Never rewrite the sole recovery partition via OTA.
3. Qualify the **exact** ZIP before publication: fresh enrollment and HA
   reconnect, explicit first recovery entry, normal restart, unplug/replug
   after re-arm, and recovery/rollback. Keep both stock system slots intact.
   Document the power-cut window before re-arm. This ZIP is distinct from the
   p12-only repair for an enrolled R22.
4. The keyless build workflow must never receive the Echo Dot private key.
   The manual signing job above is restricted to native candidates. The
   recovery-based workflow below uses an independently controlled signer.

Without the key present, run `pipeline.py prepare` from a clean checkout of the
built SHA with `target: biscuit`, `format: encore-biscuit-arm64-starter-v1`, no
reservation, `artifact`, `reproduced_artifact`, `evidence`, `toolchain`, `recipe`,
`review` and source roles `kernel` and `alpine` (Encore source is generated).
Transfer the exact approved candidate and receipt hash to the offline signer.
Run `sign-receipt --receipt-sha256` with the independently reviewed receipt
hash, using trusted tooling rather than code from the candidate checkout.
The signing checkout must have the receipt commit in its
Git object database for checks, but must not execute code from that commit
unless separately reviewed. Then verify and preview `pipeline.py publish` in
a separate keyless session; use `--execute` only after explicit publication
approval. The receipt binds both the ZIP and its exact extracted `rootfs.tar.gz`;
the public release tag is created only at publication. Never feed the ZIP to
the updater.

To install, use `pipeline.py verify` from the release source checkout with the
approved public key and reviewed receipt hash. Then pass the signed receipt's
`artifact.sha256` for the ZIP to the existing guarded installer. See the
[ARM64 install guide](../devices/biscuit/packaging/kernel61/README.md).
Compatible signed **runtime** updates replace the rootfs in the inactive Encore
slot without rewriting recovery or the stock partitions. Use the feed procedure
below with the Biscuit signing root and `target=biscuit` in `activate-feed.yml`;
enter the exact core version from the immutable `biscuit-<version>` tag. Feed URLs
use `biscuit-arm64` and point at the standalone `rootfs.tar.gz` asset. Keep the
offer limited to runtimes qualified with the installed recovery kernel. Kernel
or recovery init changes still need a separate recovery operation.

## Android APK preparation

Follow [Android signing and updates](android-updates.md) to build and sign the
candidate. Prepare the receipt from the exact clean reserved source using
`pipeline.py prepare`, with `target: android-generic`, the reservation ID,
APK and distinct byte-identical repeat assembly, build evidence, the expected
certificate SHA-256, and the matching `dependencies` source archive. Use the
SDK `aapt` and `apksigner` for inspection. The pipeline generates the Encore
source archive from Git. Sign the verified receipt offline with the Android
metadata key, not the APK signing key. Retain both binary and source assets.

## Explicit immutable asset publication

Only after approval to upload these exact files, create a **draft** staging
Release and upload the flat prepared directory with `gh release create` (use
`--draft --target <full-source-sha>`) and `gh release upload`, without `--clobber`.
This is an external upload even though it is a draft; ask before doing it. Never
use the final version tag for staging.

Dispatch `retain-release.yml` on main, supplying the staging tag, target and
reviewed receipt SHA256, initially with `publish=false`. After its validation
and environment review, separately authorize a dispatch with `publish=true`.
The workflow verifies signatures, exact asset set/sizes/hashes, immutable source
and reservation, source ancestry on main, and the Android certificate. It checks
repository immutability, refuses any existing final tag or draft, creates a new
draft at the exact source SHA, uploads **all** binary/source/evidence assets,
downloads and hash-compares them, then publishes with `latest=false`. It confirms
the published release's `immutable=true`. No feed is written. Retained releases
remain labelled experimental/prerelease even when a later qualified stable feed
selects their eligible non-prerelease core; GitHub's badge is not qualification.

Never retry a failed publication blindly. A failure can leave an uploaded draft
or a completed immutable release. Inspect its tag, source SHA, assets and
immutability first. The workflow intentionally refuses to overwrite/resume a
draft automatically; finishing a matching partial draft requires a reviewed
manual recovery. Different bytes require a new reservation, never `--clobber`.

## Feed selection, promotion and renewal

Download the exact immutable release assets into a new directory and verify
the receipt using the independently retained hash and platform public root.
Offline, sign the channel selection with that platform's **existing** metadata
key. Example for dev, with a chosen Unix expiry no more than seven days ahead:

```sh
python3 release/pipeline.py feed build/retained --public-key /private/platform-public.pem \
  --receipt-sha256 "$RECEIPT_SHA256" --channel dev --expires "$EXPIRES" \
  --key /private/platform-metadata.pem --output build/dev.jws
```

The feed contains only the existing device contract; its binary URL is the
immutable versioned GitHub Release asset. No `latest`, staging or expiring URL
is signed. For stable, change `--channel stable` and the output filename; it
rejects prerelease cores, including **0.1.1-dev.1**. Stable promotion signs a new
entry selecting the same already-reviewed bytes/revision, without rebuilding,
rewriting the receipt, changing APK identity or relabelling bundle provenance.
Stable can be verified with `activate=false`, but this workflow refuses to
activate it even if publication is enabled. Retaining release assets does not
select a channel. Enabling Stable requires a separate policy change and
safety/support qualification.

Before the first feed dry run, confirm `refs/heads/release-feeds` does not
exist. With separate approval, bootstrap it with a parentless empty commit,
not a branch cut from `main`:

```sh
tree=$(git mktree </dev/null) &&
  checkpoint=$(git commit-tree "$tree" -m 'Initialize empty feed checkpoints') &&
  git push origin "$checkpoint:refs/heads/release-feeds"
```

The push must not be forced. If the branch already exists, inspect and preserve
its checkpoints. Leave Pages off during bootstrap; when approved, select
**GitHub Actions** as the Pages source, not branch deployment. The activation
workflow archives the checkpoint tree for Pages, so it must never contain the
repository checkout. Bootstrap does not create or serve a feed.

After permission to upload the signed entry, put `dev.jws` or `stable.jws` in a
new draft feed-staging Release. Dispatch `activate-feed.yml` on main with the
Android revision or Biscuit core version, channel, staging tag and receipt hash,
initially `activate=false`.
It requires a published immutable matching artifact, re-downloads/verifies all
its sources/assets, validates the signed entry against the receipt and root,
and checks freshness and existing per-channel checkpoints. Actual Dev activation
also requires `publishing_enabled=true`, `RELEASE_SETUP_APPROVED=true` in
`release-feeds`, and the environment approval. Only an explicitly approved
`activate=true` dispatch commits history and deploys Pages.

Entries live at `<base>/<target>/incubator/{dev,stable}.jws`. Other channels and
targets remain untouched. Revisions and core precedence cannot decrease on a
channel. An equal revision permits only a new issued/expiry pair for the exact
same identity; issue times strictly increase. A higher revision with equal core
can be retained/selected but existing clients correctly do not offer it to an
equal-core installation. Both higher revision **and** strictly newer core are
required for ordinary OTA. Android signer/ABI checks still
apply; this pipeline does not create a base migration or downgrade exception.

For renewal, sign the same receipt/channel with a later issue/expiry time and
repeat this approval flow. Old signed entries remain under
`history/<target>/<channel>/<issued>.jws`. No empty feed, automatic fallback or
automatic installer is introduced. A missing/unqualified channel stays absent.
Expired entries stop working; indefinite offline signing without renewal is not
a working update service. Channel selection on devices still never installs.

If a Pages deployment fails after saving checkpoints, re-run with the **exact
same signed feed** before expiry; staging is idempotent only when its saved
history agrees. Pushes never force or discard other feeds. For withdrawal,
obtain incident approval, stop advertising the affected entry and retain its
immutable evidence/sources; clients may retain cached entries until expiry.
There is no instant cryptographic revocation protocol. Publish a corrected,
centrally reserved newer identity when appropriate; never move a channel back.

After a real deployment, fetch the exact HTTPS feed and every binary/source
asset, check signatures/hashes/expiry and exercise the real client rejection
paths before claiming live OTA delivery. The offline tests do not establish
GitHub permissions, protected-key signing, Pages propagation or device behavior.

## Safe local checks

```sh
python3 -m unittest discover -s release -p 'test_*.py' -v
python3 release/check.py --base origin/main --head HEAD
go test ./platforms/linuxupdate ./platforms/android/update
actionlint .github/workflows/biscuit-arm64-dev.yml .github/workflows/retain-release.yml .github/workflows/activate-feed.yml .github/workflows/android.yml
```

Tests use disposable metadata keys, isolated synthetic Git repositories and
device bundle fixtures; Android SDK output fixtures test parsing/rejection only.
They do not sign with the owner's key, upload firmware, publish a release/feed,
modify shared settings or operate a device.
