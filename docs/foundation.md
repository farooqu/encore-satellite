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
- `core/session`: one active backend, session/privacy/output ownership and generation
  fencing. Upstream owns recognition, intents and scheduling.
- `core/presentation`: snapshots containing only actual backend text and state.
- `backends/assist`: Noise-encrypted ESPHome native API, API audio only, with
  explicitly configured HA-dev wake-audio verification; default feature bits 7.
- `backends/incubator`: native protocol-2 transport/event mapping. Its unencrypted
  endpoint is loopback-only in the lab. No competing local wake election.
- `cmd/encore-lab` and `internal/lab`: explicitly virtual capture/control/playback
  instrumentation. Neither is a production host or hardware adapter.
  The encrypted selector and restart controls are private to the lab command,
  not a shared backend package.
- `lab`: pinned real HA service, deterministic speech fixtures and integration checks.
- `platforms/linux` and `devices/biscuit`: shared Linux host primitives and
  complete Dot Linux environment: project kernel/source artifacts, initramfs,
  Alpine radio services and runtime, recovery-only clean provisioning and local
  inactive-slot app updates. Not hardware-qualified; see the device README for missing
  AEC/calibration and required BLE kernel/physical qualification. This is not the lab fixture.
- `lab/incubator` and `lab/assist-wake`: separately provisioned real upstream
  service suites; their source/model pins, licenses and fixture boundaries are
  documented locally. Neither is a production host or packaging implementation.

`make check` runs dependency-boundary checks, vet and race-enabled tests.
`make lab-up`, `make lab-provision`, `make lab-test` are the real-stack entry points;
see `lab/README.md` for prerequisites and the scoped upstream suite entry points.
`go build ./cmd/encore-lab` builds the default virtual composition root;
`go build ./lab/assist-wake/satellite` builds the opt-in wake injection fixture.

The checker rejects core imports outside core, backend cross-imports or concrete
platform/device imports, platform imports of backends/devices, and cross-device
imports. It includes tests. Composition roots choose implementations explicitly;
there is no runtime plugin registry. Device code is not a claim of qualified
support. Assist quality-based cross-device arbitration is not yet
implemented; raw probabilities are not assumed comparable across devices.

## Single-backend packages and common controls

Production packages contain exactly one voice backend. Native Incubator is the
default package, not just the first release. Assist remains in the repository;
any future package for the Home Assistant Assist pipeline would be explicitly
labeled, opt-in and is deferred. No runtime selector, cross-backend fallback or
live switching is planned. Changing backends requires explicit application-package
replacement, while routine updates preserve the variant and channel. See the
[package contract](device-packages/README.md#incubator-first-single-backend-packages).

BLE and Sendspin enablement remain independent of that package choice.

The core retains live selection/stale-audio fencing, but the virtual host now
exposes saved selection plus an explicit Apply/restart operation, through both
encrypted HA controls and loopback instrumentation. It constructs only the chosen
voice route. Incubator mode keeps controls/BLE on the same ESPHome identity/key
without advertising Assist capability. Software mute and the independent BLE
choice survive runtime/process restarts; default BLE is off. Selecting alone is
non-disruptive. Applying deliberately restarts the shared endpoint and can
interrupt Bluetooth connections; ordinary mute/unmute keeps it alive.

That lab-only selection is not a production package requirement. The Dot binary links only Incubator
and keeps a separate encrypted ESPHome endpoint for Bluetooth/music controls,
without advertising Assist voice.

The pinned native integration creates its own persistent timer device with the
same stable MAC, not an `assist_satellite` or per-native-voice availability entity.
At the pinned HA baseline, withdrawing Assist capability removes voice-only
entities and restores their available IDs on return; common control registry
identities remain stable. Configured/running mode is separate from connection
readiness. No duplicate ESPHome assistant is fabricated. Production Dot/Android
hosts, setup UI, updater and hardware audio arbitration remain unimplemented;
Sendspin has separate opt-in virtual-lab instrumentation, not a device package.
Native rejected/losing HANGUP preserves healthy transport so
ordinary upstream decisions do not trigger reconnect backoff.

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
