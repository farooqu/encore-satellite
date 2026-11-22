# CI and release policy — experimental preparation

`targets.json` is the centrally reviewed target inventory, not a download
manifest. `ledger.json` retains historical reservations and remains part of the
Android release workflow. The Biscuit ARM64 starter derives its public SemVer
from main history independently; the retired Linux 3.18 path no longer creates
releases or entries in the ledger. No target is declared release-supported.

The [Biscuit build and installation guide](../devices/biscuit/packaging/README.md)
covers the current Linux 6.1/AArch64 starter: recovery, kernel, Alpine userspace
and the native runtime. The dedicated kernel workflow checks the pinned source.
The keyless ARM64 build produces a native A/B seed, full OTA and source kits
from one exact main commit once its external-only lock can be resolved. The
separate, manual Biscuit workflow signs the reviewed receipt and optional feed
on GitHub. Neither workflow publishes or activates a feed. See the
[publishing runbook](publishing.md) for the exact-artifact handoff.

[Android signing and updates](android-updates.md) remains independent. Manual
publication and Android feed workflows default to dry-run and require separate
approval for writes. CI builds and signature verification are not device
qualification or release approval. Preserve notices and matching source for
all distributions; see [ACKNOWLEDGEMENTS](../ACKNOWLEDGEMENTS.md).

## Reservations and provenance

[`ledger.json`](ledger.json) is append-only. It preserves private and abandoned
Biscuit reservations, including their anti-rollback floors, but new Biscuit
public dev builds do not allocate reservations. Android keeps its existing
reservation process: refresh main before allocating, never reuse a revision
for different bytes, and record the exact source and APK signer in the separate
release receipt. Selecting already retained bytes needs no new reservation.

With usable `--base` history, validation also rejects deletion, reordering or
rewriting of prior reservations and decreases to retired floors. This protects
recorded identities; it is not permission to distribute the corresponding bytes.

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
gates remain. A separately recovered FPGA tail is outside the exception.

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

Shared changes can affect both Biscuit and Android, but they build and release
independently. Each release must pass its own checks. A target-only packaging
change does not require releasing the other target. Planned-target impact is
informational, not hardware coverage.

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
   AArch64 runtime, WMT helper and Bluetooth bridge. This does not boot a Dot
   or publish an artifact.

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
- **Versions and anti-rollback:** Biscuit exposes plain SemVer derived from
  main history; its internal integer remains for anti-rollback. Android retains
  its existing core SemVer and APK versionCode. A target's positive revision
  increases across releases, variants and channels; never reuse it for
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
  validated immutable package bytes/revision, not a rebuild disguised as the
  same package. A target advances independently.
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
