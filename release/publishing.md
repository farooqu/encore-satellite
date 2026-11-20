# Development builds, immutable releases and explicit feed selection

Biscuit now builds only the Linux 6.1/AArch64 starter. The Linux 3.18/ARMv7
build and mixed Biscuit/Android cohort workflows are retired. Old reservations
and provenance remain historical records, not authorization to ship an old ZIP.

Building, signing, publishing immutable assets and activating an update feed
are separate steps. Biscuit's main-push workflow prepares unsigned candidates
only, once approved inputs are pinned. Android has its own APK checks and
protected signing workflow. Neither workflow activates a feed.

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

1. Once the pinned inputs and their distribution are approved, retain them in
   an immutable public input release and commit its tag in
   `devices/biscuit/packaging/kernel61/ci-inputs.json` with the `source_sha256`
   map and any changes to the builder's existing digest pins. It must contain `Image`,
   `biscuit-arm64-probe.dtb`, `audited-header.bin`,
   `rootfs.tar.gz`, and `ca-certificates.crt`. Following successful Foundation
   checks for a same-repository main push, `biscuit-arm64-dev.yml` builds and
   compares both the starter ZIP and standalone runtime archive across two
   assemblies of that exact source. Each version's starter is a complete first
   install on a supported Dot; it does not require an earlier Encore version.
   Installs predating this release path use a one-time fresh install through
   recovery/fastboot. They are not an OTA compatibility target.
   The standalone archive is byte-for-byte the ZIP's `rootfs.tar.gz`, not a
   delta from the previous build. It is an unsigned runtime OTA payload, not an
   update offer, and does not update the recovery kernel. Both fields set to
   `null` skip assembly. To change inputs, publish a new immutable tag and update the pin in
   a reviewed commit. Do not move the old tag or change a GitHub variable.
   Manual dispatch only produces an unsigned candidate, even after setup.
2. Include `kernel-source.tar.gz` (matching Linux 6.1 source/config/patches)
   and `alpine-go-source.tar.gz` (matching Alpine/Go corresponding source and
   notices) in that reviewed immutable input release. Use those exact filenames
   as `source_sha256` keys, with lowercase SHA-256 of each approved archive.
   The build checks both archives before assembly; a missing or mismatched
   archive prevents a CI candidate. Preparation, signing and publication also
   check both archive names and hashes against the exact receipt commit. Matching
   hashes establish byte identity, not source completeness. Do not distribute
   stock radio firmware.
3. Qualify the **exact** ZIP before publication: fresh enrollment and HA
   reconnect, explicit first recovery entry, normal restart, unplug/replug
   after re-arm, and recovery/rollback. Keep both stock system slots intact.
   Document the power-cut window before re-arm. This ZIP is distinct from the
   p12-only repair for an enrolled R22.
4. Keep the Echo Dot private key outside this GitHub repository, including its
   Actions environments. Direct pushes to `main` can change workflow and
   signing code, so even a main-only environment is not a trust boundary.
   CI builds unsigned candidates only. Automatic signed publication needs an
   independently controlled signer and publisher; don't enable it here.

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
Stable safety/support qualification must be approved separately.

After permission to upload the signed entry, put `dev.jws` or `stable.jws` in a
new draft feed-staging Release. Dispatch `activate-feed.yml` on main with the
Android revision, channel, staging tag and receipt hash, initially `activate=false`.
It requires a published immutable matching artifact, re-downloads/verifies all
its sources/assets, validates the signed entry against the receipt and root,
and checks freshness and existing per-channel checkpoints. Only an explicitly
approved `activate=true` dispatch commits history and deploys Pages.

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
