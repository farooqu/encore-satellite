# CI and release policy — unreleased foundation

`targets.json` is the centrally reviewed inventory, not a download manifest.
The shared core is `0.1.0-dev.0`. Every target has `release_supported=false`
and package revision **0**, meaning never released. `lab-linux-amd64` is a
runnable virtual test fixture. Echo Dot 2 and generic Android (intended first
tablet: Samsung Galaxy Tab A7 Lite) are **planned**, not supported packages.

There are no installable device packages, update client, signing trust roots,
signed manifests, published channels or release credentials. This directory
does not establish any of them. No production signing, uploading or publishing
is performed by the foundation workflow. The new code's license remains
unselected; keep `ACKNOWLEDGEMENTS.md` and included notices intact. A distribution
license/dependency/model/asset audit is a prerequisite to eventual releases.

## Run the checks

From the repository root, with Python 3.12+ and Git:

```sh
python3 -m unittest discover -s release -p 'test_*.py' -v
python3 release/check.py
python3 release/check.py --base origin/main --head HEAD
```

The CLI validates the current inventory and prints deterministic JSON:

- `affected_targets`: all declared targets affected, including planned devices.
- `package_targets`: affected release-supported targets only; **always empty in
  this increment**. The validator rejects enabling release support or assigning
  nonzero revisions before the release contract/builders are implemented.
- `lab_check`: always true. No empty package matrix can replace actual checks.
- `unknown_paths`: unclassified inputs, or `<unavailable diff>` when history is
  unavailable. These select every declared target, not a success-by-skipping.

Without a base revision, with an all-zero initial-push base, or when Git cannot
resolve the diff, impact broadens to all targets. The diff is from merge base to
head, with NUL-delimited paths and rename detection disabled: moving a file
between devices affects both its old and new owners. Deleted paths count too.
The command checks committed changes, **not uncommitted edits**. Invalid inventory
fails the command rather than returning an empty successful plan.

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

## Release model to enforce before any device update exists

These are requirements for later work, **not a verifier or updater shipped now**.

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
  `stable`. Routine updates preserve the installed backend and channel, with no
  implicit fallback between either. Only Incubator is initially in scope; do not
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
- **Satellite packages, not base replacement:** Dot application bundles update
  over a separately maintained stable base. Android packages bundle matching host
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
  recovery, but do not let rollback defeat monotonic revision enforcement. A
  normal rollback should be a newly signed higher package revision carrying the
  intended older content. Any privileged recovery exception requires a separate
  reviewed trust policy, not silent channel downgrade acceptance.

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
