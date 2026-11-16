# Foundation checkpoint

One module: `github.com/farooqu/encore-satellite`. Core version
`0.1.0-dev.0`; these pre-1.0 contracts are still being established. No device is
release-supported and no installer, signature trust or update channel is shipped.
The target inventory is `release/targets.json`; revision zero means never released.
An eventual package must record target ID, core SemVer, monotonically increasing
per-target package revision, explicit dev/stable channel and exact source commit.

## Working paths and direction

- `core/audio`: mono PCM16/16 kHz capture, bounded frames and pre-roll; cancellable
  playback with an explicit consumption/drain contract.
- `core/wake`: host-supplied detection evidence, not a wake model implementation.
- `core/session`: one active backend, session/privacy/output ownership and generation
  fencing. Upstream owns recognition, intents and scheduling.
- `core/presentation`: snapshots containing only actual backend text and state.
- `backends/assist`: Noise-encrypted ESPHome native API, API audio only, with
  explicitly configured HA-dev wake-audio verification; default feature bits 7.
- `backends/incubator`: native protocol-2 transport/event mapping. Its unencrypted
  endpoint is loopback-only in the lab. No competing local wake election.
- `cmd/encore-lab` and `internal/lab`: explicitly virtual capture/control/playback
  instrumentation. Neither is a production host or hardware adapter.
- `lab`: pinned real HA service, deterministic speech fixtures and integration checks.
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
there is no runtime plugin registry. Platforms/devices are reserved paths, not
implemented support. Assist quality-based cross-device arbitration is not yet
implemented; raw probabilities are not assumed comparable across devices.

## Backend selection and common controls

The shared core and loopback lab can switch backends live and revoke stale audio.
A product setup/HA backend selector and common-control connection independent of
voice ownership are not implemented yet. The current lab can still disconnect
Assist transport when switching; do not claim common controls already survive it.

The pinned native integration creates its own persistent timer device with the
same stable MAC, not an `assist_satellite` or per-native-voice availability entity.
Existing HA Assist availability follows the whole ESPHome connection. The intended
surface is one shared Encore control identity plus selected-backend/readiness
state; mapping it to separate voice availability requires HA-side support, not
duplicated controls or a fake second ESPHome assistant. Selected is not the same
as connected. Native rejected/losing HANGUP now preserves healthy transport so
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

Independent package-section work can own `devices/echo-dot-2/**`,
`devices/android-generic/**` and package contract documentation only. Record missing
base/ABI/install prerequisites explicitly. No device runtime, working installer,
physical audio, unattended APK update or OS-image replacement is implied.

The verified foundation is published to the shared GitHub baseline. New orbs
should fetch it and rebase their scoped work rather than integrating archives.
Reconcile the central target registry deliberately when package scaffolding is
integrated; do not edit it from two orbs at once.
