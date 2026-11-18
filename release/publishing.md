# Main-driven dev releases and explicit stable selection

Main-driven dev automation is implemented but **publishing is disabled** in
`dev-automation.json`. Build preparation is independent of publication. Until
the retained inputs and build setup are approved, successful main checks
produce only prospective identities and changelogs in an Actions summary and
30-day text artifact. They do not reserve revisions, sign, upload firmware or
sources, create tags/releases, or write/deploy feeds. A manual dispatch of the
dev workflow is always a preview, even after enabling automation.

After build-setup approval, a relevant green main push prepares the complete dev
cohort: tested repeat-assembled binaries, APK already signed with the separate
Android key, exact source kits, evidence, receipts and changelogs. Metadata keys
stay offline. An approved orb or laptop release session can sign/verify and
publish those exact retained files **without rebuilding**. Asset publication and
dev/stable feed activation are separate explicit actions, never push-triggered.
Stable selection/promotion remains manual. Dev is experimental,
not stable qualification. None of these workflows operates a device. The
separate `biscuit-kernel.yml` automatic-upload hold is unchanged.

The commands below are operator instructions, not a receipt that they were run.
Configuration, external uploads, publication and feed activation each require
the owner's concrete approval. No public source/cohort is frozen by this change.
The initial candidate selection is recorded separately below; coordinate any
new source freeze/allocation centrally. Private17/diagnostic overlays are not
main, public18, or hardware qualification.

## Initial reserved cohort builds in CI

`initial-release.yml` builds the existing core `0.1.1-dev.1`, Biscuit18 and
Android5 reservations without invoking `apply`, allocating19/6, or pushing main.
`initial-dev.json` pins the integrated source commit and each target's existing
ledger entry. This selection file is not another version authority: both the
selected source inventory/ledger and current main inventory/append-only ledger
must agree. Separate source and tooling checkouts keep the original package
identity intact. The immutable source archive contains the selected source;
evidence separately records the reviewed tooling commit and initial plan.

Local Biscuit18 validation consumed that identity. Its SHA256 is pinned in
`initial-dev.json`; CI must reproduce those bytes, not upload the local ZIP or
silently replace it. A different result fails the whole cohort and requires
review/new central reservations, never weakening the comparison or removing a
pin to make a build pass. Android5 has not yet been built, so it has no prior
artifact digest. No metadata key is loaded by this workflow.

Before dispatch, obtain approval for the reviewed immutable input uploads,
complete corresponding-source/distribution review in `dev-automation.json`,
the main-only Umer-reviewed `dev-release-preparation` environment, and existing
`android-signing` environment/trust/certificate settings described below.
Set **`INITIAL_RELEASE_BUILD_SETUP_APPROVED=true`** only after that readback.
Keep **`RELEASE_BUILD_SETUP_APPROVED` unset** until the initial cohort is
retained: enabling it here could let a green main push allocate the next cohort
before the initial build. Leave `publishing_enabled=false`,
`metadata_signing=offline` and publication approval unset. None of this setup is
performed by changing the workflow; an absent gate skips both jobs.

Run these commands only after the workflow is landed and setup is approved.
First validate without building or loading the APK keystore:

```sh copy Copy Initial Preflight
gh workflow run initial-release.yml --ref main -f build_candidate=false
```

Review that run's `initial-dev-plan-<tooling-sha>` artifact and changelog. The
preflight requires both latest main-push checks for the exact selected source,
clean ancestry/identity/recipe checks, and no existing initial release tag,
draft or newer release. Then separately approve one build dispatch:

```sh copy Copy Initial Build
gh workflow run initial-release.yml --ref main -f build_candidate=true
```

The build uses the existing repeat-build/inspection/receipt machinery, under
the protected APK-signing environment, for both targets. Download
`initial-dev-candidate-<selected-source-sha>` and retain the independently
reviewed handoff SHA256 from that run's summary. Changelogs are in the preflight
artifact; release notes for this older source snapshot are generated from its
exact Git history by the existing publisher. Use `dev.py sign` / `retain` below
with this handoff just as for automated cohorts: each receipt verifies its own
ledger entry, while source and core must match across targets. Metadata signing
stays offline and publication/feed activation are still separate. The build job
has read-only repository permission and only uploads 30-day Actions artifacts,
which repository readers can download; this distribution needs setup approval.

Rerunning a workflow attempt is disabled. A new build dispatch also refuses any
earlier initial build job that started, including failed/cancelled builds;
recover exact retained files or coordinate new reservations rather than
rebuilding a possibly consumed identity. This conservative guard uses retained
Actions run/job history, not just unexpired artifacts. Preserve that history;
administrative deletion of runs is not a safe identity reset. Validation-only
dispatches do not consume a CI build attempt. No host workflow has been run just
because these commands are documented.

## What is retained

| Store | Contents | Meaning |
| --- | --- | --- |
| Actions artifacts | Complete dev cohort and handoff hashes after build approval; existing manual Android candidates; 30 days | Intermediate inspection/transfer only, not permanent corresponding-source delivery |
| Owner-local output | Prepared binary, exact Encore source, dependency source kits, evidence, signed receipt | Offline retained candidate; not uploaded by `prepare` or signing |
| Draft staging Release | Explicitly uploaded prepared files or signed feed | Temporary transfer to the manual workflow; mutable, never the download URL in metadata |
| Immutable Release `biscuit-rN` / `android-generic-rN` | All prepared assets, `receipt.json`, `receipt.jws` | Permanent versioned binary/source store; no channel activation |
| `release-feeds` branch and Pages | Current entries and append-only signed history | Explicit dev/stable selection, at most seven days validity |

The receipt uses the existing inventory and append-only reservation ledger. It
binds target/backend, core/revision, clean full source commit, exact toolchain and
recipe, original dev build identity, compatibility, every asset's size/SHA256,
source-kit roles, repeat-assembly result and distribution-review evidence. The
platform's existing Ed25519 metadata key signs it using the distinct
protected type `encore-release-receipt+jws`; device feed types are unchanged.
Metadata signing stays offline; automatic CI has no metadata private-key job.
Android APK signing uses its separate protected keystore. Moving an existing
metadata key into any connected orb/runner is a separate custody approval, not
implied by approving builds or by the existence of a release command.

The Encore archive is generated deterministically from `git archive` at the
exact source commit, with no gzip filename/build clock. Verification compares
its decompressed tar bytes against that Git commit, not an upstream branch URL.
Kernel and Sendspin source-kit hashes must match their provenance inside the
Biscuit bundle. Alpine recipes/patches/source inputs and Android/Go dependency
source closure still need substantive review; a file hash cannot establish
license compliance or prove preferred-source completeness.

`prepare` requires a distinct, byte-identical second assembled artifact. Record
both independent assembly runs in the evidence; passing a copy is not a rebuild.
This establishes repeat assembly from the retained inputs, not an independently
reproducible Alpine/kernel/toolchain build. Existing base and native builders
are not all independently byte-reproducible. Retain exact inputs and original
provenance; never relabel a reused kernel/Sendspin artifact with a new origin.

## Main-driven dev operation

`dev-release.yml` listens for completion of Foundation and Android checks on
main. It independently checks the latest push run/attempt of **both workflows
for the same SHA**, excluding PR/fork/failed/in-progress/cancelled runs. The
later completion can proceed; older sources are coalesced to current main.
Nothing waits for the Dot's audio diagnosis, and nothing imports its private
working tree. Bursts can yield fewer releases than commits, never a deliberately
untested SHA. Every included change remains in the next applicable changelog.

`dev.py plan` reuses the existing impact selector and append-only ledger. It
advances only the explicitly chosen `X.Y.Z-dev.N` line, increasing N. It does not
infer breaking changes or choose a patch/minor/major from commit wording. After
a final core, a maintainer must explicitly choose/reserve the next dev line.
The current preview would propose a version after the existing reservation;
it does **not** allocate or supersede Biscuit18/Android5/core0.1.1-dev.1 today.

Changelogs contain per-target commit subjects/links and compare links since
that target's highest immutable published revision tag. The first release lists
history from inception. Drafts/failed unpublished attempts do not hide changes.
Docs/lab-only changes since the last retained release do not create a cohort;
unknown paths conservatively count as shared impact. A later docs-only push
may still release previously unreleased code once the complete source is green.

The shared core inventory currently requires both experimental targets' latest
reservations to match the same core. Consequently every eligible automated
release reserves **both** next platform revisions and builds both targets, even
for a target-local change. The other target's notes explicitly say it was rebuilt
for shared identity. Lab remains revision zero; `release_supported=false` and
experimental status remain unchanged, not converted into support qualification.

Once enabled, the serialized workflow downloads its own plan, rechecks main,
recomputes the plan, appends one central ledger entry and commits inventory plus
`release/changelogs/<core>.md`. A normal fast-forward push is the allocation
compare-and-swap: concurrent main writes cause failure, never rebasing a planned
version onto different source. The GITHUB_TOKEN metadata push does not trigger
another push workflow; subsequent jobs explicitly test/build its **exact** SHA.
Metadata-only bot commits also have a validated source trailer to prevent loops;
a trailer cannot hide changed device/core code. Existing ledger entries are
never edited and failed reservations remain consumed.

The builder regenerates the Go binaries/AAR and clean signed APK twice, assembles
Biscuit twice using the same retained base/native inputs, and requires matching
hashes for **both**. The retained evidence records input digests, tool/run identity,
checks and both assembly hashes. The dedicated signer inspects embedded bundle/
APK identities and certificate, verifies receipts/source archives and signs
receipts/channel entries offline. A public-key-only release session verifies
the signed handoff, uploads/downloads/verifies complete binary/source releases
and confirms immutability. Separately approved manual feed activation validates
freshness and checkpoint monotonicity before pushing history and deploying
Pages. Neither signing nor asset retention changes a feed. Releases use immutable platform tags
`biscuit-rN` / `android-generic-rN`, with core SemVer in the title and committed
changelog in **each** tag's Release notes. There is no duplicate core-tag version
authority, and no `latest` download URL.

If either build/check fails, no metadata release or feed is published. A network
failure during publication may leave one immutable release and/or a draft for
the other: **no feed is pushed by asset publication**. Activate a shared-core
cohort only after both retained releases are confirmed. Stop and inspect;
never overwrite/rebuild under those reservations. Recover the exact retained
bytes manually, or leave them retained but inactive and use new reservations on
the next eligible main source. Rerunning an old event after main advanced skips
it; it does not recycle a revision. Automatic allocation/build jobs refuse Actions
run attempts after the first; use inspected manual recovery, not "re-run failed
jobs" to rebuild a consumed identity. A Pages failure after checkpoint push uses
the exact-feed recovery in the manual activation section below.

## Setup to enable automatic builds, not publishing (not performed)

Build setup does not require Pages, a metadata signing runner, metadata private
keys in CI or approval to activate a feed. Obtain concrete approval for:

1. **Source/input distribution scope.** Coordinate the first public source freeze
   and central allocation authority with bring-up. Review the experimental dev
   acceptance policy, dependency source closure/notices and retained inputs.
   The FPGA documented-risk exception is not legal clearance, nor approval for
   unrelated blobs. Do not use private17 diagnostic overlays as public inputs.
2. **Immutable bootstrap inputs.** Deliberately upload an approved complete input
   set to immutable versioned GitHub Release assets. This is a separate public
   source/binary upload requiring approval, not restoration of automatic kernel
   uploads. Add `inputs` to `dev-automation.json` with `dependency_sha256` from
   `python3 release/dev.py dependency-hashes`, and a `files` map with these roles:

   | Role | Retained file |
   | --- | --- |
   | `base` | Exact `base.tar.gz`; no floating Alpine reconstruction |
   | `kernel` | gzip tar of the original kernel artifact directory, flat root including provenance and source.tar.gz |
   | `sendspin` | gzip tar of the original native artifact directory, flat root including provenance/notices/source.tar.gz |
   | `alpine` | Complete matching Alpine **and Go dependency** preferred sources, patches, inventory, recipes and notices |
   | `android-dependencies` | Complete matching Android/Maven/Go/gomobile/dependency preferred sources, recipes and notices |

   Each file descriptor is `{ "name": "safe-unique-name.tar.gz", "size": <bytes>,
   "sha256": "<64 lowercase hex>", "url": "https://github.com/farooqu/encore-satellite/releases/download/<fixed-tag>/<name>" }`.
   Record the actual approval reference in `distribution_review`. Null inputs
   intentionally cannot build. Changed dependency manifests/recipes block
   allocation until their source kits and review are refreshed; real kernel/
   native provenance loaders provide additional correspondence checks. Hashes
   alone do not establish a complete/legal source kit. Do not waive failed repeat
   assembly: this is not a claim of independently reproducible upstream/toolchain
   builds or an offline Maven/Alpine bootstrap.
3. **Protected allocation/build environments.** Configure
   `dev-release-preparation` with main-only deployment rules and Umer review.
   Preserve `android-signing` and its existing protected keystore. Confirm its
   `ANDROID_CERTIFICATE_SHA256`, `ANDROID_UPDATE_BASE_URL` (intended HTTPS feed
   base) and `ANDROID_UPDATE_PUBLIC_KEY` (base64 raw32 existing Android metadata
   public root). These are public trust inputs, not private keys; the build
   evidence retains the URL/public root for reproduction. The feed host need
   not be active to build, but must be verified before feed activation. No
   signing key is created or moved by this change. Allow the job token to
   append the metadata commit under approved main rules, without force-push.
   Decide whether those reviews may be relaxed for unattended dev only after
   the first approved end-to-end run. Local/private allocations must still
   coordinate through the same central main ledger; no other checkout may
   allocate independently while the automation owns public allocation.
4. **Build enablement.** Keep checked-in `builds_enabled=true` and, only after
   the preceding setup/allocation authority is approved, set repository variable
   `RELEASE_BUILD_SETUP_APPROVED=true`. Complete reviewed inputs are also
   required. Leave **`publishing_enabled=false`**, `metadata_signing=offline`
   and `RELEASE_SETUP_APPROVED` unset. Main can now allocate/build complete
   candidates without creating public release tags or activating a channel.
   The 30-day Actions upload itself distributes binaries/sources to repository
   readers; approve that exact scope and back up selected candidates before
   expiry. Do not enable it under the assumption that CI retention is private.

No online metadata runner is configured or needed. Before a release, choose an
approved signing session with the existing roots, trusted Python3.12+, OpenSSL3,
Git and SDK36 aapt/apksigner. Keep keys and independently compared public PEMs
outside the checkout. Default to the existing offline machine; using a connected
orb/laptop runner with a key needs its own concrete custody approval. The tools
never generate replacement roots or transfer private keys. Main writers cannot
cause metadata signing merely by pushing code.

There is deliberately no scheduled signing. If main is quiet, dev feeds expire
after seven days; the assigned operator must use the manual offline renewal
path before expiry (normally by day6). A daily unattended renewal service is not
implicitly authorized by main-driven release automation. Checkpoint recovery,
renewal ownership and expiry alerting must be settled before advertising OTA.

## One-time owner configuration (not performed)

1. Land/review this tooling on main before dispatch. Protect main and require
   the release/inventory checks. Keep automatic package selection disabled.
2. Configure **`release-publication`** and **`release-feeds`** environments with
   Umer review, main-only deployment branches and no administrator bypass.
   An absent environment may otherwise be auto-created without reviewers by
   Actions. Do not dispatch until protection is independently read back.
   Preserve the existing `android-signing` environment and key custody.
3. Put **public** PEM/SPKI Ed25519 keys in environment variables
   `BISCUIT_METADATA_PUBLIC_KEY_PEM` and `ANDROID_METADATA_PUBLIC_KEY_PEM` in both
   environments. Compare their raw32 public bytes with the existing backed-up
   roots and device pins; do not generate replacement identities. No private
   metadata key goes in GitHub secrets, chat, source or logs; no Amp secret or
   runner custody changes have been approved. Selection fails on a
   missing target root; it never falls back to the other platform's root.
4. Set `ANDROID_CERTIFICATE_SHA256` in both environments to the independently
   verified dedicated APK certificate. The historical receipt records
   `9c5ce57128b6d0642c6e801a2400d8fc665bb0b9ca2c5a41c059f0181e51505c`;
   confirm it against the actual keystore/candidate before use. Keep signing
   secrets only in the existing `android-signing` environment.
5. Enable GitHub **immutable releases**. Provision `IMMUTABILITY_READ_TOKEN`
   in `release-publication`: a repository-scoped fine-grained token or App
   credential with Administration **read**, not write, plus metadata read.
   The normal job token uploads assets. The extra credential only reads
   `GET /repos/farooqu/encore-satellite/immutable-releases`; publication refuses
   missing/denied/disabled settings. Assign expiry/rotation ownership. This
   separate token is necessary because the standard contents-write job token
   cannot read that administrative setting. Never enter it in a thread.
6. Deliberately bootstrap an empty `release-feeds` branch containing `.nojekyll`
   using an isolated Git directory, not an orphan checkout over main. Configure
   branch rules to prevent deletion/force-push, allowing only the approved feed
   workflow/maintainer to append. Do not create empty signed feeds.
7. Enable Pages with **GitHub Actions** as the source, and authorize deployment
   from `release-feeds` environment/main workflow. The prospective base is
   `https://farooqu.github.io/encore-satellite`; confirm actual HTTPS service before
   provisioning that URL into packages. The branch is a checkpoint store, not
   an automatic Pages branch-push publisher. The explicit deploy step serves it.
8. Assign permanent binary/source retention and off-site backup ownership,
   source-compliance/withdrawal response, and offline feed signing/renewal
   ownership. Review at least daily and renew before expiry (normally by day6).
   There is intentionally no scheduled signing or implicit feed activation.
9. After concrete owner approval/readback of the common setup, set repository
   variable `RELEASE_SETUP_APPROVED=true` to permit explicitly approved manual
   publication/activation, and change checked-in `publishing_enabled` to true.
   These do not enable automatic metadata signing/publication; no such job is
   present. Build setup uses its separate `RELEASE_BUILD_SETUP_APPROVED` gate.

On 2026-11-18, read-only repository inspection reported a public repository,
`has_pages=false`, and only the existing `android-signing` environment (one
required reviewer, custom deployment branch policies). The new preparation,
publication and feed environments are not configured. Repository variable and
secret-name reads returned HTTP403; their configuration is **unknown**, not
assumed absent. Earlier immutability readback also returned HTTP403, so that
setting remains unknown. No settings, credentials or shared branches were
changed. The accepted exact-FPGA policy remains documented-risk acceptance,
not legal clearance. Preserve its record and all existing notices in sources
and packages; it does not cover additional vendor blobs.

## Fast handoff from CI, without rebuilding

After build approval, download artifact `dev-candidate-<reservation-commit>` from
the successful **Main-driven dev candidates** run. Record its source SHA and the
`handoff.json` SHA256 from that independently reviewed run's build summary.
The manifest binds both receipt hashes; each receipt binds all binary/source
bytes. Do not simply trust a replacement hash shipped with a download.

Before disconnecting the signing machine, fetch the exact committed source and
required history into a trusted checkout that contains it, install the trusted
inspection tools, and review the evidence/changelog/source scope. Transfer the
candidate without executing its contents. Pin `EXPECTED_CERTIFICATE` to the
independently verified Android certificate. Then, offline:

```sh
python3 release/dev.py sign build/dev-candidate --handoff-sha256 "$HANDOFF_SHA256" \
  --biscuit-key /private/biscuit-metadata.pem --biscuit-public /private/biscuit-public.pem \
  --android-generic-key /private/android-metadata.pem --android-generic-public /private/android-public.pem \
  --aapt /path/to/36.0.0/aapt --apksigner /path/to/36.0.0/apksigner \
  --channel dev --expires "$EXPIRES"
```

`EXPIRES` is a Unix timestamp at most seven days ahead. The signer rechecks both
targets, the committed reservations, exact source archives, every asset and the
APK certificate **before signing either target**. It creates `receipt.jws` in
each target directory and `<target>-dev.jws` at the cohort root; it changes no
prepared file and runs no compilation, dependency download or publication.
If signing fails, inspect the reason and use a fresh copy of the same verified
candidate; do not silently replace prior signatures or reuse different bytes.

Move only the signed handoff back to an approved connected release session;
leave private keys behind. The public-key-only retention command is initially
an offline dry-run and takes no Android SDK or private-key argument:

```sh
python3 release/dev.py retain build/dev-candidate --handoff-sha256 "$HANDOFF_SHA256" \
  --biscuit-public /public/biscuit-public.pem --android-generic-public /public/android-public.pem
```

After publication-setup approval, the same command with `--execute` publishes
both exact retained releases, with their committed changelogs. It needs the
approved upload credential and read-only immutability credential described
above, and checked-in `publishing_enabled=true`. It does not activate feeds.
Alternatively use `retain-release.yml` separately for each flat target directory
as described below. Transfer/upload time and full verification still remain;
"quick release" means **no rebuild**, not a promise of instant large transfers.

For each target, upload its `<target>-dev.jws` as **`dev.jws`** into its own
approved draft feed-staging release and dispatch `activate-feed.yml` with that
retained revision/receipt hash, starting with `activate=false`. Only activate
after both retained releases and the whole cohort review pass. Stable promotion
and later renewal use the single-target commands below on already-signed
retained receipts; do not re-sign an existing receipt or rename a dev.N core.

## Build and prepare manually when no CI candidate exists

Use Python3.12+, Git and OpenSSL3. Android inspection also needs the trusted SDK
36.0.0 `aapt` and `apksigner` tools. Start from the final **clean committed**
source agreed with the bring-up/release owner. Never import local device fixes
implicitly. Reserve new bytes centrally before building; consume a revision even
if its candidate is abandoned. Serialize real signing attempts centrally; the
offline preparer does not allocate or lock revisions across checkouts. The
publication tag prevents replacing published identities, not concurrent local
builds before first publication.

Build with the existing device recipes. Biscuit uses `make biscuit-base`,
`make biscuit-kernel biscuit-sendspin`, then `make biscuit-package
BISCUIT_REVISION=<reserved>`. Preserve the exact base tar, kernel and Sendspin
artifact directories and their original provenance. Assemble twice from the
same clean source/retained inputs, saving outputs separately. Android uses the
protected manual `android.yml` signed-candidate job or its documented local
release build. Its revision must equal the current central inventory before
keystore loading. Inspect/repeat the actual release APK, not the debug build.

Collect an `evidence.tar.gz` containing exact build commands, tool versions and
container/input digests, complete dependency/package inventory, build and test
logs, both assembly hashes, APK signer/badging/manifest or Linux base records,
source correspondence/notices review, affected-cohort results, hardware
acceptance/known limitations and the concrete distribution approval. Scrub
credentials, device configuration/keys and owner-only radio firmware before any
upload. Shared-code changes require all affected targets' acceptance; publishing
one target is not a waiver of that cohort gate. The offline signer reviews it.

Prepare `build/release-spec.json`, using real local paths and exact toolchain
versions. This is a **template**, not a selected public candidate:

```json
{
  "target": "biscuit",
  "reservation": "first-incubator-dev-biscuit-18",
  "artifact": "build/biscuit/encore-biscuit-18.zip",
  "reproduced_artifact": "build/repeated/encore-biscuit-18.zip",
  "sources": {
    "kernel": "build/retained-inputs/kernel-source.tar.gz",
    "sendspin": "build/retained-inputs/sendspin-source.tar.gz",
    "alpine": "build/retained-inputs/alpine-source-inputs.tar.gz"
  },
  "evidence": "build/evidence.tar.gz",
  "toolchain": "REPLACE with exact compiler, SDK, builder and container identities",
  "recipe": "REPLACE with exact commands and retained-input identifiers",
  "review": "approved-for-distribution"
}
```

For Android use target `android-generic`, reservation `first-incubator-dev`, the
actual release APK/twice-built APK, `certificate_sha256`, and a single supplied
source role `dependencies` containing exact Go/Maven/other required preferred
sources, patches, recipes and notices. Encore source is generated automatically
for both targets; do not supply an `encore` path. Source roles must be complete;
upstream URLs alone are not source kits. Give every asset a distinct safe basename.

```sh
python3 release/pipeline.py prepare build/release-spec.json --output build/retained
# Android: also pass --aapt /path/to/36.0.0/aapt --apksigner /path/to/36.0.0/apksigner
python3 release/pipeline.py sign-receipt build/retained --key /private/platform-metadata.pem
sha256sum build/retained/receipt.json
python3 release/pipeline.py verify build/retained --public-key /private/platform-public.pem --receipt-sha256 "$RECEIPT_SHA256"
python3 release/pipeline.py publish build/retained --public-key /private/platform-public.pem --receipt-sha256 "$RECEIPT_SHA256"
```

Set `RECEIPT_SHA256` to the independently reviewed printed hash, not a value
downloaded with an untrusted candidate. Outputs must not already exist. The
last command is an offline dry-run printing the planned commands. Nothing above
uploads, publishes, contacts a device, or imports private signing material to CI.
Keep the offline directory/receipt backed up even after successful publication.

## Explicit immutable asset publication

Only after approval to upload these exact files, create a **draft** staging
Release and upload the flat prepared directory with `gh release create` (use
`--draft --target <full-source-sha>`) and `gh release upload`, without `--clobber`.
This is an external upload even though it is a draft; ask before doing it. Never
use the final `biscuit-rN`/`android-generic-rN` tag for staging.

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

## Explicit dev, stable, promotion and renewal

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
new draft feed-staging Release. Dispatch `activate-feed.yml` on main with target,
revision, channel, staging tag and receipt hash, initially `activate=false`.
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
required for ordinary OTA. Exact Linux base or Android signer/ABI checks still
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
python3 release/dev.py plan --output .amp/in/orb-backup/dev-preview
python3 release/check.py --base origin/main --head HEAD
go test ./platforms/linuxupdate ./platforms/android/update
actionlint .github/workflows/dev-release.yml .github/workflows/retain-release.yml .github/workflows/activate-feed.yml .github/workflows/android.yml
```

Tests use disposable metadata keys, isolated synthetic Git repositories and
device bundle fixtures; Android SDK output fixtures test parsing/rejection only.
They do not sign with the owner's key, upload firmware, publish a release/feed,
modify shared settings or operate a device.
