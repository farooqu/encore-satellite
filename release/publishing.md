# Development builds, immutable releases and explicit feed selection

Biscuit now builds only the Linux 6.1/AArch64 starter. The Linux 3.18/ARMv7
build and mixed Biscuit/Android cohort workflows are retired. Old reservations
and provenance remain historical records, not authorization to ship an old ZIP.

Building, signing, publishing immutable assets and activating an update feed
are separate steps. Only eligible release builds from `main` can automatically
advance Dev; PR/check artifacts cannot. Signing stays restricted to protected
`main` with verified commits. Stable always selects an exact qualified version
manually.

The frozen first-parent anchor in `release/biscuit_dev.py` still fixes the
increasing build revision (also Android's `versionCode`). Historical sources
retain their original shared version. New product trains use a separate
public version, resolved from the exact source by `release/product_versions.py`.
Android main-derived receipts use `identity: main`, without a reservation.
Keep the old ledger for historical verification; do not allocate new entries.
After successful **CI · Repository checks**, one Dev
plan compares the push tip against each device's own last immutable published
release on first-parent `main`. Shared package inputs select the targets that use them;
device-only inputs select that device. Failed checks, an unpublished build, a
draft, or a bare tag do not consume pending changes, so the next eligible
green push can release them. A skipped target leaves its published release and
feed untouched. Its next build can skip Dev suffixes and OTA revisions.

The **CI · Repository checks** summary shows the combined changelog, including an explicit
**No build** for each skipped target. The shared `dev-release-plan` artifact
also carries separate release notes per selected target. Each GitHub release
and, for Tablet and Biscuit, each automatic Dev OTA entry uses only its own
target's highlights: breaking changes, features, fixes (including reverts),
and performance. Maintenance commits stay in the full source comparison, which
can include other targets' changes. The Assistant remains a sideload APK with no
OTA feed. Both old and new immutable Dev releases remain valid baselines.
Reviewed manual candidate builds remain available and do not automatically
publish or promote Stable.

## Independent product trains

| Product | Existing Dev tag | First tag after cutover | First possible Stable tag |
| --- | --- | --- | --- |
| Android Tablet (`android-generic`) | `android-0.x.y` | `android-tablet-v0.10.0-dev.1` | `android-tablet-v0.10.0` |
| Biscuit (`biscuit`) | `biscuit-0.x.y` | `biscuit-v0.10.0-dev.1` | `biscuit-v0.10.0` |
| Android Assistant (`android-assistant`) | `android-assistant-r…` | `android-assistant-v0.10.0-dev.1` | `android-assistant-v0.10.0` |

Dev advances only `-dev.N`. A train derives its suffix from the package's
first-parent revision minus `first_revision`, plus one. A rerun of the same
source gets the same version even if Stable has advanced since. After a product
ships Stable `0.10.0`, open that product's `0.10.1-dev.N` train; Dev does not
promise that `0.10.1` will ship. Other products keep their trains.

**Cutover is not active.** `release/dev-trains.json` is empty until the owner
approves the migration. Before adding an entry, hold and drain affected writers
and audit public tags, deployed signed feeds, private/attempted revisions, and
Biscuit base compatibility. Keep every legacy tag, immutable asset, pinned
input release, feed URL and checkpoint. For each approved product, add
`{"base":"0.10.0","first_revision":<cutover revision>,"ordering_major":<audited major>}`
in one reviewed `chore(release): …` commit. Use the revision of that commit
(its first parent's revision plus one) and an ordering major strictly above
the legacy installed ordering floors. Pin the source before qualifying the
`.1` candidate. Verify legacy-to-Dev OTA and two successive new Dev builds on
compatible devices before opening publication. A source that missed `.1` can
be retried at the same SHA; do not move the counter origin.

For Assistant, dispatch **Android Assistant · Checks and Dev release** on
protected `main` with `sign_only=true` and `publish=false`. The workflow uses
the exact source-derived APK identity and protected Android signer, and returns
an Actions artifact without creating a tag or GitHub release.

Tablet and Biscuit still require **both** a higher revision and a higher signed
`core_version`. New builds use `<ordering_major>.0.<revision>` only for
ordering and artifact identity. The signed `display_version` is what the new
clients offer in Home Assistant; the installed label comes from the verified
APK or rootfs. Legacy clients may show the internal ordering value for the
first offer, before installing a build that understands the display field.
Do not lower signed versions or bypass the device's OTA compatibility rules.

### Review and promote Stable

Run `python3 release/stable_plan.py PRODUCT CANDIDATE_SHA BASELINE_SHA --initial 0.10.0`
for the first Stable release. For subsequent releases use the last immutable
Stable record's source as `BASELINE_SHA` and replace `--initial` with
`--previous VERSION`. Use the exact candidate, not today's `main` or latest Dev.
Take the strongest applicable bump once: breaking → major, feature → minor,
fix/performance/shipped packaging → patch. Test-only, verification-only and
docs-only changes do not count unless shipped. No qualifying change means no
new Stable release. For mixed changes use `Version-Bumps: android-generic=minor, biscuit=patch`.
Omitted products have no contribution. `Release-Targets` still only controls
notes. Resolve flagged ambiguous historical commits with `--decisions FILE`,
a reviewed JSON map of SHA to `none`, `patch`, `minor` or `major` for this product.

1. Download the exact immutable Dev release and independently verify the
   source/tag, signing identity and qualification. Save the reviewed preview
   JSON. For Tablet and Biscuit, run `release/pipeline.py stable-record`
   with the retained directory, `--plan`, `--public-key` and `--receipt-sha256`.
   For Assistant use `assistant-stable-record` with the directory, `--plan`,
   `--certificate-sha256` and Android build tools. Both print `promotion.json`
   without publishing anything.
2. With release approval, publish an immutable non-prerelease at the record's
   Stable tag and exact source, with `promotion.json`, reviewed notes and links
   to the existing immutable Dev downloads and matching source kits. Use the
   same product title prefixes (`Android`, `Biscuit ARM64`, `Encore Satellite
   Assistant`). Do not rebuild or rename an APK or ZIP. Its installed label
   remains the embedded Dev label; Stable is a release/offer label.
3. For Tablet or Biscuit, sign with `pipeline.py feed --channel stable
   --promotion promotion.json` (Biscuit's protected signer also accepts
   `channel=stable` and `stable_version`). Stage with **Release · Update OTA feed**:
   use the retained Dev label as `version` and the Stable label as
   `stable_version`. Start with validation only; activation still requires the
   protected environment and explicit approval. Assistant has no feed.
4. Only when that product's Stable publication (and feed activation, if any)
   succeeds, record the preview's `next_dev_base` and the housekeeping commit's
   new `first_revision` in `dev-trains.json`. Keep `ordering_major` unchanged.
   No other product should be edited. An equal or older revision on a newly
   selected channel does not reinstall or relabel the device.

## Conventional Commits

Use `type(scope): summary`; the scope is optional, e.g.
`fix(android): restore reconnect` or `docs: clarify install steps`. Use `feat`
for a minor bump and `!` or a `BREAKING CHANGE: …` footer for a major bump.
Other accepted types are `build`, `chore`, `ci`, `docs`, `fix`, `perf`,
`refactor`, `revert`, `style`, and `test`. The versioning anchor and published
versions do not change.

**CI · Repository checks** lints all commits introduced by a PR and all new `main`
history since the enforcement cutoff. Use a Conventional Commit subject for
the final merge or squash commit too.

For user-facing changes, make the scope name the feature or area and the subject
say what users can now do or what stopped going wrong. Use the body for why and
how. If a mixed-product commit has the same release note for several products,
write it once and list the intended audiences in a Git trailer:

```text
feat(sendspin): add Sendspin playback

Release-Targets: android-generic, biscuit
```

Tablet and Biscuit both show **Add Sendspin playback** under **Features**.
Use comma-separated target IDs: `android-generic`, `android-assistant`, or
`biscuit`. The trailer filters the changelog only: it cannot add a note for an
unaffected product, and it does not change build selection or versioning.
Omit it when the changed files make the audience clear. Without it, ambiguous
mixed-product highlights use a short commit link instead of the subject.
Use `Release-Note-<target>: <user-facing summary>` when a product needs different
wording. If both trailers are present, that wording applies only to products
included in `Release-Targets`.

Enable the local pre-push hook once per clone with
`git config --local core.hooksPath .githooks`. If you already use a custom hooks
directory, chain `.githooks/pre-push` from your existing hook instead of replacing
your configuration. The hook uses Python 3 to check commits sent to each branch
before Git uploads them. For a new branch it compares against the named remote's
`main`, so fetch `main` from that remote first. Tags and branch deletions are
unaffected. To check manually, run
`python3 release/biscuit_dev.py --check-commits origin/main HEAD`.

CI runs after GitHub accepts a direct push: an invalid commit on `main` blocks
automatic Dev releases, including on later pushes. Fix invalid subjects before
pushing; if one lands on append-only `main`, resolve it with the maintainer
rather than rewriting history or moving the cutoff.

## Biscuit native A/B GitHub signing

The keyless Biscuit build uploads `biscuit-native-unsigned` after an exact main
build with the external inputs below. Its receipt binds the seed ZIP, the OTA
ZIP, the build evidence and matching sources. Both ZIPs must have the same
two boot images and the same runtime; repeat builds must match both ZIPs.
The seed is for a fresh install. The OTA is for existing native installs only.

Manually run **Biscuit · Sign and deliver Dev** on
protected `main`, with the build run ID, exact source SHA and the separately
reviewed receipt SHA256. Provide `BISCUIT_METADATA_PRIVATE_KEY_PEM` as an
environment secret and `BISCUIT_METADATA_PUBLIC_KEY_PEM` as a variable in
`biscuit-signing`. The key must match the Ed25519 root compiled into Biscuit.
Restrict the environment to exact `main` and disable administrator deployment
bypass. `operation=check-gate` exercises the environment without checkout,
download or secret access. Use `operation=sign` for a reviewed manual pilot.

The pilot uses an owner-approved solo trust model: append-only `main`, owner
review in Amp before direct pushes, and verified commits. The owner approved
unattended key use under these constraints. This is not independent review or
protection from compromise of that account; anyone allowed to push to main
can change signing code. Before adding other writers, require reviewed changes
and independent approval or move signing to a separately controlled repository
or service.

The signing job never runs candidate code. It uploads `biscuit-native-signed`
with the seed, OTA and signed receipt. Select `none` to sign without a feed or
`stable` to prepare a separate feed artifact for manual promotion. With
publishing armed, a successful main-push release build containing the unsigned
native artifact queues the protected signer automatically, for Dev only.
Check-only runs and manual rebuilds do not queue signing. After successful
automatic signing, Dev retention and activation are chained using that run's
artifacts through the unattended, exact-main environments
`release-dev-publication` and `release-dev-feeds`. The manual environments
`release-publication` and `release-feeds` retain their reviewer gates.
The manual `sign` operation never publishes, regardless of the publication switch.
Native feeds are stored under `biscuit-native-ab/incubator/` so they cannot
affect recovery installs.

Publish ordinary signed Dev releases before the device pilot; do not reserve a
separate pilot version. Install the first retained Dev release's exact signed
seed with the [native installer](../devices/biscuit/packaging/README.md#signed-native-ab-pilot).
After fresh enrollment, apply the next retained Dev release through Home Assistant.
USB verification checks that the new version is running and confirmed, that
the former slot still holds its previous version, and that enrollment survives
a further reboot. A later ordinary update writes the opposite slot. Do not
mirror the new version onto both slots; that discards the rollback version.
After qualification, explicitly select the verified immutable release for the
initial Stable feed; do not rebuild it. The Dev feed channel and the `-dev.N`
package label are separate from the signed ordering version.

## Biscuit ARM64 public dev starter

The legacy ARM64 starter is a signed fresh-install download, not an OTA feed
entry. Historical releases retain their original naming, for example tag
`biscuit-0.1.7`, title `Biscuit ARM64 0.1.7`, and asset
`encore-biscuit-arm64-0.1.7.zip`. New trains use the native A/B packaging path.
Do not force-push main, move the revision anchor, rename older releases, or
use the private R22 reservation.
Neither a new registry entry nor an Android build is needed.

The historical starter versions do not have a Dev suffix. A runtime OTA feed
for these releases selects the retained rootfs, never the starter ZIP.
Release promotion is not performed by the automatic Dev workflow.

1. `devices/biscuit/packaging/ci-inputs.json` locks only external
   bytes. CI fetches the pinned upstream Linux source and Go archives from
   their upstream URLs. Once approved for this use, retain the exact other
   four files (`alpine-apks.tar.gz`, `alpine-source.tar.gz`, `apk-tools-static.apk`,
   `audited-header.bin`) at an immutable tag and set `external_bundle_tag` in a
   reviewed commit. Null leaves GitHub artifact assembly disabled. Do not
   republish the upstream kernel input archive or include stock radio firmware.
   For a private, local build, supply all seven locked files directly to
   `locked_build.py --inputs`; no release tag or upload is required.
2. After **CI · Repository checks**, **Biscuit · Build Dev packages** checks out
   the exact main commit, builds Linux and the DTB from that tree's recipe, and constructs
   Alpine from the locked APKs, not from an earlier Encore image. It emits one
   version's complete starter ZIP and matching unsigned runtime payload, with
   byte-identical repeat assemblies, the two corresponding source kits and
   evidence. The `alpine` source role (`alpine-go-source.tar.gz`) also carries
   `sendspin-source.tar.gz`, including the pinned SDK, adapter, and bundled
   userspace runtime sources. Receipt verification checks that kit against the
   companion provenance, executable, and notices in the exact packaged rootfs.
   Dispatch with a full first-parent main SHA to replay that commit and its
   locks. This checks repeat packaging; it does not claim identical rebuilds
   across arbitrary build hosts. Retention and approval of matching source and
   notices still require review before any distribution.
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
[ARM64 install guide](../devices/biscuit/packaging/README.md).
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

Dispatch **Release · Publish GitHub assets** (`retain-release.yml`) on main,
supplying the staging tag, target and reviewed receipt SHA256, initially with
`publish=false`. After its validation
and environment review, separately authorize a dispatch with `publish=true`.
The workflow verifies signatures, exact asset set/sizes/hashes, immutable source
and reservation, source ancestry on main, and the Android certificate. It checks
repository immutability, refuses any existing final tag or draft, creates a new
draft at the exact source SHA, uploads **all** binary/source/evidence assets,
downloads and hash-compares them, then publishes with `latest=false`. It confirms
the published release's `immutable=true`. No feed is written. Retained releases
remain labelled experimental/prerelease even when a later qualified stable feed
selects their eligible non-prerelease core; GitHub's badge is not qualification.

**Repository requirement: keep immutable releases enabled.** Automatic Dev
retention has no admin-read token and does not query the setting before
publication. It still verifies the downloaded draft bytes before publishing
and requires GitHub to report `immutable: true` afterward. Feed activation
independently requires the same confirmation. If a release was published but
immutability cannot be confirmed, inspect the reported public tag; do not
activate it, delete it automatically, or retry blindly. Manual publication
still requires `IMMUTABILITY_READ_TOKEN` for its pre-publication settings check.

If GitHub refuses to create a tag for an earlier `main` commit with the built-in
token, check that no release or draft exists. An authorized maintainer may
create the exact release tag as a lightweight tag pointing at the verified
receipt source, never ahead of or behind it. Automatic Dev retention accepts
that exact existing tag without creating or retargeting it. For Biscuit, use
`operation=deliver-dev` on protected `main` with the original build run ID,
source SHA and reviewed receipt SHA256 to deliver the same candidate to Dev.
This operation forces Dev and keeps its existing policy and environment gates.

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

The binary URL is the immutable versioned GitHub Release asset. No `latest`,
staging or expiring URL is signed. Automatic Dev feeds use the candidate's
verified release plan for inline Home Assistant release notes. For a manual
feed, pass `--changelog path/to/reviewed-notes.md` with the Markdown from the
exact retained release. Notes are optional, signed with the rest of the entry,
and excerpted if they exceed 3 KiB. Renewing an entry can add notes without
changing which bytes it selects. For Stable, use `--channel stable` and the Stable
output filename; for a new-train candidate add `--promotion promotion.json`.
Stable rejects prerelease cores, including **0.1.1-dev.1**. Stable promotion signs a new
entry selecting the same already-reviewed bytes/revision, without rebuilding,
rewriting the receipt, changing APK identity or relabelling bundle provenance.
Stable can be verified with `activate=false`. Seed Stable only with a
signature-verified, install-qualified baseline. For the initial selection and
all later Stable promotions, manually dispatch with `channel=stable`,
`activate=true` and `promote_stable=true` for the exact retained release and
receipt hash, then approve the protected deployment. The automatic path only
accepts Dev. Retaining release assets does not by itself select a channel.

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
new draft feed-staging Release. Dispatch **Release · Update OTA feed**
(`activate-feed.yml`) on main with the
core version (without the `android-` or `biscuit-` prefix), channel, staging tag
and receipt hash, initially `activate=false`.
It requires a published immutable matching artifact, re-downloads/verifies all
its sources/assets, validates the signed entry against the receipt and root,
and checks freshness and existing per-channel checkpoints. Manual activation
requires `publishing_enabled=true`; automatic Dev activation instead requires
the target in `automatic_dev_targets`. Both require `RELEASE_SETUP_APPROVED=true`
in the applicable feed environment before committing history and deploying
Pages. Manual activation additionally requires environment review.

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
