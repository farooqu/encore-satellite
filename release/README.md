# CI and release policy — experimental preparation

`targets.json` is the centrally reviewed inventory, not a download manifest.
The shared core for the first reserved dev cohort is `0.1.1-dev.1`, strictly newer
than the private `0.1.0-dev.0` baseline. Biscuit and generic Android are
**experimental**, not stable or hardware-qualified. Their reserved revisions are
18 and 5 respectively. `lab-linux-amd64` remains a revision-zero test fixture.
Every target keeps `release_supported=false`: automatic release package selection
remains disabled. Reserving versions does not build, sign, publish or qualify them.

Android now has a development APK and a gated signing/update foundation; see
[Android signing and updates](android-updates.md). No production trust roots,
published feeds or release credentials are provisioned by this repository.
There are no supported device releases. The Dot now has a
local development builder and explicitly invoked USB/recovery installer/updater;
its local revisions do not change this release inventory or qualify hardware.
Its bundle is the Dot Linux environment, including a project-built kernel,
recovery initramfs and radio/userspace services, not an app for preinstalled Linux.
The dedicated `biscuit-kernel.yml` workflow builds and verifies pinned kernel inputs.
Kernel/source-kit automatic uploads remain withheld pending concrete publication
approval. The exact audio FPGA binary has an owner-approved good-faith exception
for dev and production releases, not a resolved license; see the
[kernel source obligations](../devices/biscuit/packaging/KERNEL-SOURCE.md).
Durable corresponding-source retention and signing remain release gates.
This directory does not establish a release channel.
No production signing, uploading or publishing is performed by the foundation
workflow; the separate Android workflow only signs manually approved candidates.
Encore-owned code and documentation use the [MIT License](../LICENSE); keep
`ACKNOWLEDGEMENTS.md` and third-party licenses/notices intact. A distribution
license/dependency/model/asset and corresponding-source audit remains a
prerequisite to eventual releases.

## First dev reservation and revision ledger

[`ledger.json`](ledger.json) records append-only reservations. The public intent
is core `0.1.1-dev.1`, backend `incubator`, channel `dev`.

| Target | Reserved package revision | State |
| --- | --- | --- |
| `biscuit` | 18 | Reserved; no final public binary/source selected |
| `android-generic` | 5 (APK versionCode) | Reserved; production-signed APK pending |

The initial retired floors conservatively burn Biscuit revisions 1–15, including
pre-rename/private development, and Android 1–4, including debug/disposable-key
validation. This does not claim every number produced an artifact or any was
publicly released. The retained Android inspection records an actual debug APK
at revision 4; synthetic feed-test revisions are not production reservations.
The private bring-up owner confirmed no Biscuit revision above 15 was consumed
when 16 was proposed. A subsequent owner-authorized private build now reserves
17 (`speaker-office-private-17`, baseline core `0.1.0-dev.0`). The earlier public
16 remains burned/unbuilt; a new public reservation 18 supersedes it without
rewriting history. Android keeps its original reservation 5. Existing revision-15
bytes/provenance remain immutable. These are reservations, not build receipts.

Append a new reservation before building different candidate bytes; never reuse
a consumed/reserved number after a failed, abandoned or withdrawn candidate.
Revisions increase independently per target across all channels, not per SemVer.
Keep current inventory revisions/core aligned with the latest reservations.
Once this ledger is landed, serialize reservations through main and refresh main
before allocating; local drafts alone cannot prevent another checkout allocating
the same number. Stable promotion reuses eligible already-recorded artifact
bytes/revision, not a second reservation or rebuild. This prerelease has no stable
entry; both channel choices remain implemented without inventing an empty feed.

The validator rejects reused/nonincreasing revisions, stable prereleases, inventory
mismatches and non-Incubator entries. With available `--base` history it also
rejects deletion/reordering/rewriting of existing reservations or retired floors.
Without usable base history it validates current content only; CI's complete
checkout/base comparison and central review are required for historical integrity.
Neither the ledger nor these checks constitute a signed publication authorization.

Before publication, retain a separate immutable receipt binding each reserved
target/revision to the exact clean source commit, toolchain, artifact size/hash,
compatibility/base or APK certificate/ABI, matching sources/notices and completed
review evidence. Those values are intentionally not fabricated in a reservation.
Feed signatures and final asset/source verification remain separate gates.

Public 18 is above private 17; a unit running 17 cannot ordinarily update to the
abandoned public 16. Any subsequent private allocation above 18 must likewise be
accounted for before public selection; never weaken anti-rollback or relabel an
old reservation. Ordinary OTA also requires a strictly newer core SemVer, not
merely a higher package revision, and an exact compatible Linux base. The private
revision-15 boot stops before Improv; the separately owned correction/private17
build does not qualify the eventual public image or authorize device actions.

## Redistribution preparation

Experimental dev releases do not require stable hardware qualification, but they
still require distribution rights and matching notices/source. The original
Amazon archive audit did not establish a grant for the embedded audio FPGA
program. On 2026-11-17 Umer accepted a narrowly scoped documented-good-faith
exception for that exact binary in both dev and production distributions,
following the verified community precedent. Its license stays `NOASSERTION`;
this is acceptance of unresolved risk, not legal clearance or publication
approval. Preserve the [decision record](../devices/biscuit/packaging/FPGA-PROVENANCE.txt)
with the binary/source distribution; all other compliance, signing and safety
gates remain. Linux 6.1 is a separate project, not a release gate.

The base builder excludes the unused external BusyBox `ssl_client` helper while
its GPL-only/OpenSSL linkage grant remains unresolved. Encore's downloads use
Go TLS; no Encore service invokes that helper. A real build removed only
`usr/bin/ssl_client`, leaving the other 41 package versions/recipe commits
unchanged. A fresh base has a new identity; it is not an app-slot OTA migration
and must not silently replace an already selected private bring-up input.

The [dependency notice pack](../licenses/README.md) now includes Go and Android
notices, the matching MPL Public Suffix List preferred source, and Alpine notices.
Exact Alpine recipes/configurations/patches and all 271 checksum-listed inputs
for the retained inventory's 28 source origins were collected and verified.
This is source-input retention, not an independent clean rebuild or blanket
license clearance. The release owner must durably deliver the matching source
bundle with distributed binaries and recheck the final binary inventory,
notices and source correspondence. Local retention and upstream links alone
are not the public source-delivery step.

## Run the checks

From the repository root, with Python 3.12+ and Git:

```sh
python3 -m unittest discover -s release -p 'test_*.py' -v
python3 release/check.py
python3 release/check.py --base origin/main --head HEAD
```

The CLI validates the current inventory and prints deterministic JSON:

- `affected_targets`: all declared targets affected, including experimental devices.
- `package_targets`: affected release-supported targets only; **still empty**.
  Positive experimental reservations do not enable automatic package builders.
- `lab_check`: always true. No empty package matrix can replace actual checks.
- `unknown_paths`: unclassified inputs, or `<unavailable diff>` when history is
  unavailable. These select every declared target, not a success-by-skipping.

Without a base revision, with an all-zero initial-push base, or when Git cannot
resolve the diff, impact broadens to all targets. The diff is from merge base to
head, with NUL-delimited paths and rename detection disabled: moving a file
between devices affects both its old and new owners. Deleted paths count too.
Impact selection checks committed changes, **not uncommitted edits**. Inventory
and ledger validation read the current worktree. Invalid records fail the command
rather than returning an empty successful plan.

### Dependency contract

Every target declares `depends_on` directory prefixes, including its platform;
planned devices must include their own `devices/<id>/` directory. Several
targets may depend on one platform. Shared core/backend code, Go module/workspace
dependencies, Makefile, shared tools/scripts/build infrastructure, CI, release
metadata, update policy, setup/services and legal notices affect **all** targets.
Shared dependencies are declared once in `check.py`; target-specific dependencies
are in `targets.json`. Changes to either are themselves shared impact.

Platform changes select all registered dependents. Device-local profiles and
packaging select that device only. Lab-only implementation/dependency changes
select the fixture only. Markdown-only documentation changes need no package
builds (except legal acknowledgement inputs). Executable files or schemas under
`docs/` are not documentation-only. Unknown paths, unknown devices/platforms and
unsafe paths select everything. Add new shared inputs and explicit dependency
prefixes as real build contracts emerge; do not infer ownership from labels.

Future builders must build **every affected release-supported target** for a
shared change and all dependents for a platform change. A failed affected target
must block the whole affected release cohort; never publish a silently partial
shared-core release. Target-only packaging changes must not force an unrelated
target to update. Planned-target impact is informational, not hardware coverage.

## What GitHub Actions is configured to check

`.github/workflows/check.yml` runs on pull requests, branch pushes and manual
dispatch, without path filters. It has read-only contents permission, no release
environment or secrets, no persisted checkout credentials, and action references
pinned to full commit IDs. PR code is untrusted: do not change this into a
`pull_request_target` workflow executing contributor code with privileged access.

1. Validate inventory, run selection regression tests and report impact.
2. Refuse package targets until a real package-build contract exists.
3. Always run `make check` (dependency boundaries, `go vet`, race-enabled Go tests)
   and `go build ./cmd/encore-lab`, using Go 1.27.4 on Ubuntu x86-64.
4. Check the Dot development package contracts/shell syntax and cross-build the
   ARMv7 runtime, WMT helper and Bluetooth bridge. This does not boot a Dot or publish an artifact.

The package-validation step uses the stdlib entry points inspected at package
scaffold commit [`686cd2b`](https://github.com/farooqu/encore-satellite/commit/686cd2bf57fd8b8540ab3e90804319c7fcfa5ea6).
If either planned device directory or `docs/device-packages/` exists, both
`python3 docs/device-packages/validate.py` and its unittest suite are mandatory;
a missing validator then fails, rather than silently skipping. On the independent
CI branch before package integration, the step explicitly reports the absent
scaffold and claims no profile validation. CI never fetches or executes a moving
peer branch. Integration must bring the reviewed scaffold into the checked-out
tree; the selector/registry change does not merge those files itself.

The selected planned targets do **not** run invented device-build commands.
Building the virtual lab is not building an APK or Echo package. The initial
workflow does not run `make lab-test`; that is a separate real-stack check, not a
synonym for race/unit tests. The current lab requires Python 3.14.7, system
libturbojpeg/ffmpeg, locked dependencies, checksum-verified HA source and generated
translations, onboarding/pairing, mDNS/Noise services and **Amp-supervised service
restarts** invoked by `lab/test_assist.py`. Standard GitHub-hosted runners do not
provide that service lifecycle. Do not claim integration coverage by launching
only a Go test or an unprepared HA process there.

Run the real integration in an isolated Amp orb per `lab/README.md`:
`.agents/setup`, `make lab-up`, `make lab-provision`, `make lab-test`. Source of
truth is the lab's own pins: HA **2026.12.0.dev0**, commit
`1395798e2338bd46ee522539f7281b336c44e3b8`, not stable HA 2026.11.1.
Keep generated credentials private; never substitute production HA credentials.
Future automated real-stack CI needs an explicit compatible runner/lifecycle
contract from the lab owner. Workflow configuration is not evidence that GitHub
has run anything; record executed commands and actual CI run URLs separately.

## Release model to enforce before device publication

These are publication requirements, not a claim of supported device releases.
Android implements the gated verifier/install-request path described above;
experimental Linux signed-OTA source is separately implemented and platform-owned.
Neither implementation is published or hardware-qualified. Both use the
[shared channel/control contract](../docs/device-packages/update-contract.md).
The manually supplied Dot local-development USB bundle is explicitly unsigned,
requires an out-of-band build hash, never downloads code, and must not be
relabeled as a production update mechanism.

- **Incubator default, one backend per package:** the default device package and
  any unqualified build/install choice must use only the native `incubator`
  adapter. Keep `assist` in the repository, but defer its possible separately
  qualified, opt-in package, explicitly labeled for the **Home Assistant Assist
  pipeline**. Do not present it as a co-default. The backend identifier remains
  `assist`. No runtime
  backend selector, automatic cross-backend fallback or live switching is part
  of a device package.
  Inspect built dependencies and package contents to enforce this; a disabled
  second adapter is not isolation. The dual-backend lab remains a test fixture.
- **Two version axes:** shared core SemVer describes protocol/core behavior.
  A positive, monotonically increasing integer package revision belongs to each
  target across all core versions, backend variants and channels; adding a variant
  must not reset that sequence. Packaging/base-compatibility-only
  changes can increment one target's revision without changing core SemVer or
  other targets. Revision zero is never distributable. Never reuse a revision for
  different bytes, including after a failed or withdrawn release.
- **Immutable provenance:** each package records exact target identity, backend
  variant (`incubator` or later `assist`), core SemVer, package revision, full source
  commit, build/toolchain provenance, artifact byte size and cryptographic digest,
  plus compatibility requirements.
  Immutable artifact identities and retained records must be established before
  publication; do not overwrite a `latest` binary or silently replace old bytes.
- **Explicit variants and channels:** publish a separate signed entry for each
  supported target/backend/channel combination; channels remain exactly `dev` or
  `stable`. Routine updates preserve the installed backend and use the explicitly
  selected persistent desired channel (which may differ from installed package
  provenance), with no implicit fallback or downgrade. Selection alone never
  installs a package. Only Incubator is initially in scope; do not
  create empty Assist release entries. Stable entries must not point to a
  prerelease core. Promotion reuses the already
  validated immutable package bytes/revision, not a rebuild disguised as the same
  package. A target advances independently, except for the affected-cohort gate.
- **Authenticity before installation:** signed metadata must bind target, backend
  variant, channel, versions, source, digest/size and compatibility. Establish a
  vetted format, trusted offline-provisioned roots, verification/rotation/revocation and
  freshness/anti-rollback behavior before implementing downloads or activation.
  Reject missing/invalid signatures, wrong targets/backend variants/channels,
  stale revisions, tampered artifacts and incompatible packages. Never accept
  trust keys or shell commands from an unauthenticated device/backend/channel response.
- **Compatibility before activation:** enforce declared base identity/version,
  OS/kernel/userspace/ABI, platform-host/engine IPC contract, configuration/state
  migrations and tested backend baselines. Exact constraints must come from
  tested target contracts, not a generic ARM or Android label. Test rejection as
  well as successful application, interruption recovery and health checks.
- **Full environment, safe incremental updates:** the Dot installation bundle
  supplies its kernel, recovery initramfs, Linux userspace and Satellite runtime.
  Its incremental app-slot updater requires the exact kernel/userspace/initramfs
  base identity; it cannot replace the kernel shared by both fallback slots.
  A safe base-migration updater is not yet implemented. Android packages bundle matching host
  and engine, subject to real signing/install permissions. Base maintenance and
  recovery are separate; no implicit OS flashing, arbitrary remote execution,
  independently fetched Android engine or promised unattended APK update.
- **Backend replacement is explicit:** moving between Incubator and a later Assist
  variant is a deliberate application-package replacement, not a routine update,
  runtime setting or required base/OS reflash. Before offering it, validate
  configuration/state migration, platform signing/version constraints and recovery.
  Qualify each variant independently; Assist hardware support and switching tests
  must not block an otherwise qualified Incubator package.
- **Rollback is deliberate:** retain known-good immutable packages and test
  recovery, but do not let rollback defeat monotonic revision enforcement.
  Ordinary OTA additionally requires a strictly newer shared-core SemVer in the
  selected channel. A higher package revision carrying older/equal core is not
  eligible; a channel behind the installed version waits. Any recovery exception
  requires a separate reviewed mechanism, not channel downgrade acceptance.

## Review routing and enforcement still required

`../.github/CODEOWNERS` routes shared core/platform/security/release changes
centrally, and names device paths separately. `@farooqu` is the repository owner;
no community maintainers are invented. Add actual device reviewers only after
they accept that role. Device-specific privileges and installers also require
central security review; a device path does not make executable recipes trusted.

CODEOWNERS is neither isolation nor a guarantee of all approvals. The repository
owner must configure rulesets/branch protection, required status checks, protected
release environments and approval policy before relying on enforcement. GitHub's
owner-match semantics do not require every listed owner to approve. A mixed
device/core change needs central review, not just a device label or approval.
No repository settings were changed by adding these files.
