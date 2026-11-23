# Foundation checkpoint

One module: `github.com/farooqu/encore-satellite`. Core version
`0.1.0-dev.0`; these pre-1.0 contracts are still being established. No device is
release-supported and no production signature trust or update channel is shipped.
The target inventory is `release/targets.json`; revision zero means never released.
An eventual package must record target ID, backend variant, core SemVer,
monotonically increasing per-target package revision, explicit dev/stable channel
and exact source commit.

## Working paths and direction

- `core/audio`: mono PCM16/16 kHz capture, bounded frames and pre-roll; cancellable
  playback with an explicit consumption/drain contract.
- `core/wake`: host-supplied detection evidence, with a pure-Go MixedNet/MNB1
  frontend/executor for app-pushed microWakeWord models in `core/wake/mixednet`.
- `core/session`: session/privacy/output ownership and connection generation
  fencing. Upstream owns recognition, intents and scheduling.
- `core/presentation`: snapshots containing only actual backend text and state.
- `backends/incubator`: native protocol-2 transport/event mapping. Its unencrypted
  endpoint is loopback-only in the lab. No competing local wake election.
- `backends/esphome` and `backends/bluetooth`: encrypted Home Assistant controls,
  sensors and optional Bluetooth proxy, independent of voice.
- `cmd/encore-lab` and `internal/lab`: explicitly virtual capture/control/playback
  instrumentation. Neither is a production host or hardware adapter.
- `lab`: pinned real HA service and integration checks.
- `platforms/linux` and `devices/biscuit`: shared Linux host primitives and
  complete Dot Linux environment: project kernel/source artifacts, initramfs,
  Alpine radio services and runtime, recovery-only clean provisioning and local
  inactive-slot app updates. Not hardware-qualified; see the device README for missing
  AEC/calibration and required BLE kernel/physical qualification. This is not the lab fixture.
- `lab/incubator` and `lab/sendspin`: separately provisioned real upstream
  service suites; their source/model pins, licenses and fixture boundaries are
  documented locally. Neither is a production host or packaging implementation.

`make check` runs dependency-boundary checks, vet and race-enabled tests.
`make lab-up`, `make lab-provision`, `make lab-test` are the real-stack entry points;
see `lab/README.md` for prerequisites and the scoped upstream suite entry points.
`go build ./cmd/encore-lab` builds the virtual satellite.

The checker rejects core imports outside core, backend cross-imports or concrete
platform/device imports, platform imports of backends/devices, and cross-device
imports. It includes tests. Composition roots choose implementations explicitly;
there is no runtime plugin registry. Device code is not a claim of qualified
support.

## Single-backend packages and common controls

See the [Voice PE comparison](voice-experience.md) for session behavior and
device controls.

Device packages and the virtual lab run only native Voice Incubator. There is no
runtime backend selector or fallback. Routine updates preserve backend and channel. See the
[package contract](device-packages/README.md#incubator-first-single-backend-packages).

BLE and Sendspin remain independently enabled. The lab and device hosts keep
encrypted ESPHome management separate from the Incubator transport. Lab software
mute and Bluetooth enablement survive process restarts. Voice mute/reconnect does
not restart the Bluetooth proxy.

The pinned native integration creates its own persistent timer device with the
same stable MAC, not an `assist_satellite` or per-native-voice availability entity.
Native rejected/losing HANGUP preserves healthy transport so
ordinary upstream decisions do not trigger reconnect backoff.

[Voice PE at `8ba314e`](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/devices/voice-pe.yaml#L1215-L1233)
arms spoken Stop at `responding` and keeps it armed until the call ends. Encore
uses that same response window, even before the first reply samples or after
drain. Unlike Voice PE's same-socket cancellation, Encore sends `CALL_END`
and closes the voice connection so an untagged trailing frame cannot reopen
the microphone or affect a fresh turn. ESPHome management stays connected.
Verify with `go test ./core/session -run 'TestSpokenStop|TestNativeStop'` and
`go test ./backends/incubator -run TestNativeCancelFencesLateOutputAndReconnect`.

[Voice PE at `8ba314e`](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/components/voice_incubator/voice_incubator.cpp#L479-L497)
trusts the app's `HANGUP` with `listen: true`, with a local mute check and no
pre-roll. [Voice Incubator at `0eac1602`](https://github.com/farooqu/voice-incubator/blob/0eac16021a8dc85df64f8992f5f21f2d8df8886c/voice_incubator/server.py#L3266-L3287)
sends it after the reply and the listening cue. Encore keeps that handoff but
requires a current accepted response with no local output still open. This
prevents a stray or early repeat from opening or restarting capture, without
requiring local PCM for replies played on an external speaker. The wire has no
turn ID, so this cannot identify a replay that overlaps a later eligible
response. Verify with `TestFollowUpRequiresCurrentResponse` in `./core/session`
and `TestNativeWireEvidenceEndpointingDrainAndFollowUp` and
`TestNativeMonitoringRequiresIndicatorAndDoesNotReuseAudio` in `./backends/incubator`.

[Voice PE at `8ba314e`](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/components/voice_incubator/voice_incubator.cpp#L205-L215)
ends a call after 30 seconds without `HANGUP`, sends `CALL_END(timeout)`, and
lets buffered playback finish on the same connection. Encore uses the same
fixed call deadline, with a fresh deadline for each follow-up. On expiry it
stops streaming, drops unsent call PCM, and uses the Stop fence to interrupt
playback, send `CALL_END(timeout)` if the transport permits, and retire only
the voice connection. Keeping that connection would let untagged late frames
affect the next call. Off-call monitoring, intercom, alerts and announcements
do not arm the call deadline. The timed `STATUS` frames are diagnostic only
and are not needed for this watchdog. Verify with
`go test ./core/session ./backends/incubator -run 'Test.*CallWatchdog'`.

## Parallel work boundaries

The foundation/lab owner retains `go.mod`, `go.sum`, `core/**`, `backends/**`,
`cmd/encore-lab/**`, `internal/lab/**`, `lab/**`, `.agents/setup`,
`.amp/services.yaml` and the initial check entry points.

Independent CI/release work can own `.github/**`, `release/**`, `updates/**` and
release-policy documentation. No production signing keys, publishing workflows or
claimed updater are established by this checkpoint. Shared changes should select
all release-supported targets; today that set is empty, so the virtual lab still
needs checking rather than an empty successful matrix.

The initial device scaffolds have evolved: Dot work now includes a concrete Echo
host, device runtime, kernel/Linux-base builder and local recovery tooling. Android remains planning-only.
Keep hardware, required BLE integration, release authenticity and power-loss
qualification gaps explicit; no supported install, unattended APK update or new
base/kernel release is implied by the development builder.

The verified foundation is published to the shared GitHub baseline. New orbs
should fetch it and rebase their scoped work rather than integrating archives.
Reconcile the central target registry deliberately when package scaffolding is
integrated; do not edit it from two orbs at once.
