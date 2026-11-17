# Planned device package contract (schema 1)

These are **planning profiles, not runtime configuration or release manifests**.
There is no profile loader. Android now has a local development APK build and
host described in its target notes; neither target has a supported release.
Both profiles remain planned/revision-zero release metadata. Passing validation
below establishes metadata consistency only, not the APK or hardware behavior.
[Dot development package](../../devices/echo-dot-2/README.md) separately implements
a full Linux environment builder, hardware runtime and USB/recovery tooling,
without qualified support.

- [Echo Dot 2](../../devices/echo-dot-2/README.md): local hardware wiring,
  kernel/base services, calibration and Linux-environment bundle.
- [Generic Android](../../devices/android-generic/README.md): conservative APK
  profile; Samsung Galaxy Tab A7 Lite is the intended first validation tablet.

## Profile fields and validation

Each target owns exactly `devices/<target>/profile.json`. The format is deliberately
closed at this planning stage; every field is required and unknown fields fail.

| Field | Schema 1 value and purpose |
| --- | --- |
| `schema` | Integer `1`; identifies this planning-data contract, not an IPC version. |
| `target` | Exact directory/central target ID: `echo-dot-2` or `android-generic`; prevents cross-target profile substitution. |
| `status` | `planned`; no supported release. Development build evidence belongs in the target notes, not this metadata. |

Profiles contain only identity and planning status. They do not configure audio
or advertise runtime capabilities. Audio, control and display requirements remain
in the target notes until there is an implementation that needs those settings
and evidence for their values. Describing intended work does not declare support.

From the repository root, using only Python 3's standard library:

```sh
python3 docs/device-packages/validate.py
python3 -m unittest discover -s docs/device-packages -p 'test_*.py'
```

The validator rejects malformed/duplicate-key JSON, extra or missing fields,
runtime settings or capability fields, wrong target identity, and registry drift
from planned/revision-zero/non-release-supported. Tests exercise the command's
observable exit status with valid and deliberately invalid package metadata.
Neither command builds a device or validates hardware, an APK or an update.

## Ownership and intended assembly

The [device contribution guidance](../../devices/AGENTS.md) defines how to reuse
or introduce interfaces as implementations need them, without coupling the core
to concrete device packages or adding speculative configuration.

Device owners maintain their own profiles, wiring/calibration, target composition
roots, packaging inputs and hardware acceptance records. Shared Go contracts and
DSP belong in `core/`; reusable OS integration belongs in `platforms/echo/` or
`platforms/android/`. The Android host is now implemented for development; the
profile itself is not its runtime configuration. Echo supplies Linux
ALSA/evdev/privacy/playback primitives. Backend adapters remain in `backends/`.

Composition roots explicitly select the shared engine, one platform host,
their own configuration and exactly one voice adapter per installed package.
The initial Android package includes only Incubator, not Assist or switching UI. The engine never
imports device modules or branches on device model names. A Dot package must not
contain Android host code; an Android APK must not contain Dot hardware code.
Static profiles do not implement dependency isolation; the existing import-boundary
check and future package-content inspection have separate roles.

## Incubator-first, single-backend packages

The default device package uses native Voice Incubator, not just for the first
release. An unqualified Encore package or default build/install choice must mean
Incubator. Assist remains in the repository with its own protocol/lab coverage.
If offered later, its separately qualified, opt-in package must be explicitly
labeled for the **Home Assistant Assist pipeline**; it is deferred, not a
co-default or a prerequisite for an Incubator package.

Keep the Assist source available for periodic evaluation as Incubator features
reach the Home Assistant Assist pipeline. The owner's decision to move to it depends
on demonstrated wake-word detection/verification, speaker identification, spoken
"stop" interruption, and continued microphone listening throughout an active
conversation for interruptions and follow-ups without another wake word, always
subject to mute/privacy controls. These are reevaluation criteria, not claims of
current Assist limitations or implemented/validated Encore hardware capabilities.
They do not require an Assist package now or authorize automatic migration.

- Each production package must link and start exactly one voice backend:
  `incubator` by default, or `assist` only in a future package for the Home
  Assistant Assist pipeline.
  Do not bundle both adapters and disable one through configuration. Audit the
  built dependency closure and package contents, not just the selected setting.
- There is no runtime backend selector, fallback to the other backend, or live
  switching. Shared audio, wake, session, presentation and platform/device code
  remain reusable. Reconnect, privacy, cancellation and stale-audio fencing for
  the installed backend remain required.
- Changing backends means an explicit application-package replacement, not an
  ordinary update, settings toggle or implicit base/OS reflash. Establish and
  validate configuration/state migration and recovery before offering it.
- Future package provenance and signed update metadata must bind both device
  target and backend variant. Routine updates must preserve the installed backend
  and channel and reject a mismatched variant. Backend is not a dev/stable channel.
- Qualify the installed backend on each supported device/base/ABI. Incubator
  acceptance must not depend on Assist hardware qualification or switching tests;
  a later Assist variant needs its own acceptance evidence.

The dual-backend virtual lab may retain switching as test instrumentation; it is
not a production composition root or a package to ship. Common device controls
must not require a second voice backend. The Dot uses a separate encrypted
ESPHome common-control endpoint without advertising an Assist voice satellite.

These requirements do not add runtime fields to `profile.json` or enable release
support. Schema 1 remains identity and planned status only until an implemented
consumer needs a richer contract.

## Audio and release boundaries

The current engine capture contract is bounded mono PCM16 at 16 kHz, with sequence,
capture time and discontinuities; see `core/audio/audio.go`. Native hosts must
adapt real capture formats, honor cancellation/privacy and acknowledge physical
playback drain, not socket receipt. Neither raw Dot channels nor an Android
AudioTrack write automatically satisfy those contracts. No new shared interface
or runtime adapter is introduced here.

`release/targets.json` is centrally owned and remains authoritative for target
inventory, core SemVer, per-target package revision and release inclusion. These
profiles intentionally duplicate none of the version/channel/build fields.
Package revision zero means never released, not a downloadable revision. Future
artifacts must record their target, backend variant, core SemVer, package revision
and exact source commit; dev/stable channel manifests are centrally managed.
Device contributors cannot select release inclusion, signing trust or publication
from a profile. Shared contract/registry changes require their respective owners'
review.

The Dot bundle supplies its own kernel, initramfs, Linux userspace and runtime.
Incremental app-slot updates require the same Linux-base identity; they cannot
silently replace the shared kernel or recovery. Android's future signed APK
contains its matching native host and Go engine, not a separately downloaded core.
The Dot's local development installer/updater is explicitly invoked over USB
recovery with a trusted local build digest and an unsigned-development opt-in.
There is no network updater, signature implementation or production trust root.

## Evidence, attribution and licensing

The target notes cite pinned **Techo5** and **Techo5 Panel** implementation evidence.
Their work and upstream EchoLocal, echo-dot-assist, TensorFlow Lite Micro, KISS FFT,
microwakeword, ESPHome, Home Assistant and Voice Incubator deserve acknowledgement;
see [ACKNOWLEDGEMENTS.md](../../ACKNOWLEDGEMENTS.md) for exact provenance and
included third-party notices. No runtime code, model, calibration, firmware or
asset is declared by the planning profiles. Android's included frontend/executor
and downloaded Sendspin libraries are identified in the acknowledgements. Echo
low-level code and Dot's wmtup retain their MIT notices. Source inspection is not fresh
hardware validation and upstream compatibility claims are not Encore guarantees.

The license for new Encore material is still **not selected**. Existing third-party
licenses/notices remain applicable; audit all bundled code, models, assets and
base requirements before any distribution. Neither a proposed permissive license
nor a passing profile check grants rights to redistribute upstream material.
