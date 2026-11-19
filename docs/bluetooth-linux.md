# Linux Bluetooth base, Improv and the optional ESPHome proxy

Bluetooth is required base functionality for supported Encore devices. The
Bluetooth **proxy** is a separate feature: one persistent `bluetooth_proxy`
boolean, omitted/default **false**. Its encrypted ESPHome switch enables scans,
active GATT connections and supported pairing together. There is no second
active-connection/pairing toggle. BLE Improv first-boot Wi-Fi provisioning works
with the proxy OFF; forwarding advertisements does not provision Wi-Fi.

## Implementation and capabilities

- `core/ble` owns radio-independent raw-advertisement and GATT contracts.
- `platforms/linuxble/bluez` uses Linux Bluetooth and BlueZ D-Bus for discovery,
  GATT client operations, bonding and the Improv GATT server. A receive-only
  HCI socket preserves actual raw advertising bytes, address type and RSSI.
  BlueZ discovery is active scanning; it does not pretend to offer passive/active
  mode switching. No raw scan/reset commands compete with bluetoothd.
- `backends/bluetooth` implements the ESPHome native proxy protocol against
  `go-esphome-device v0.0.18`. Capability mask 47 is scan, active connections,
  remote service caching, pairing, and raw advertisements. It does not advertise
  cache clearing, connection-parameter setting, scanner-mode switching, or
  extended advertisements. A scan-only direct-H4 host advertises only scan/raw
  (and mode switching only if actually supported), never active GATT or pairing.
- Fixed-active proxies send scanner state on the advertisement subscription,
  with both the scan mode and configured mode set to active. `RUNNING` follows
  scanner readiness; pause/OFF and scan failure are distinct states. This is
  independent of advertisement delivery and slot allocation. Home Assistant
  Core 2026.11.1 with `bleak-esphome` 4.0.0 listens for these packets only when
  `FEATURE_STATE_AND_MODE` is set. Its frontend displays a missing current
  mode as **No scanning**, even while receiving advertisements. Do not set that
  bit for BlueZ discovery: HA can then send passive-mode requests, which this
  scanner cannot honor. The compatible client fix is to listen for optional
  state packets on raw subscriptions, while keeping mode commands, mode
  settings and the first-state retry watchdog gated by the existing bit.
- Just Works bonding uses a private `NoInputNoOutput` agent scoped to the
  requested device. PIN/passkey entry and numeric confirmation are rejected,
  not invented or silently approved. No global discoverability/default pairing
  agent is installed. Unpair refuses paired audio/phone peers. Bond keys remain
  BlueZ's responsibility, not the ESPHome Noise PSK's.
- Proxy resources are bounded: one scan worker, 64-report queue, batches of
  16/250 ms, one GATT worker, 32 ordinary queued requests, 36 coalesced
  disconnect addresses, 64 queued notifications, three connection slots and
  at most 32 BlueZ notification subscriptions.
  GATT operations have a 30-second context; D-Bus calls have a five-second
  maximum and HCI commands two seconds. A disconnect cancels earlier queued
  calls for its address and preempts ordinary in-flight work even at another
  address. Repeated disconnects coalesce only within the same lease generation
  and never cancel active disconnect cleanup; OFF or client loss still cancels
  the lease.
  Cancelled queued calls are removed before the disconnect response, so their
  late replies cannot be mistaken for a subsequent same-address retry.
  Older-generation replies finish before successor slot availability; active
  cleanup stops coalescing atomically with its terminal response. Preempted
  GATT calls receive an error even if controller success races cancellation.
  Cleanup is serviced ahead of ordinary work even when its queue is full;
  repeated disconnects for one address coalesce. Excess distinct disconnect
  addresses close the API connection and its lease rather than falsely report
  cleanup. Rejected disconnects never publish a disconnected acknowledgement.
  OFF disconnect is idempotent after confirmed central cleanup, including on
  first boot without opening a radio. Failed central cleanup closes the old API
  client without claiming disconnection and fences further active GATT leases
  at zero slots until the host recovers the controller and recreates the proxy.
  OFF/ON or API reconnect alone does not clear that fault. Encrypted switch
  controls remain available; BlueZ propagates individual disconnect failures.
  A control-only client never takes another client's GATT lease or slot
  subscription; persisted switch changes reach every state subscriber. Notification
  overflow cancels/fences that lease rather than silently losing link state.
  No audio/session locks are held for radio I/O.

The direct `linuxble.Open` scanner can exclusively own `/dev/stpbt` without a
Linux Bluetooth stack, or an HCI user channel with bluetoothd stopped. It is
**diagnostic/scan-only**, not a fallback fulfilling the supported Bluetooth base.
Never open `/dev/stpbt` directly alongside btbridge. Unlike the direct scanner's
18.75 ms/200 ms budget, BlueZ discovery's scan duty cycle is owned by BlueZ;
audio/Wi-Fi coexistence needs target measurement, not an assumed identical budget.

## Echo Dot 2 prerequisites (biscuit, Alpine ARMv7)

The stock/TWRP kernel must not be assumed to supply Bluetooth. A real
Bluetooth-enabled compatible kernel is an external prerequisite until one is
built and qualified for Encore. A Fire OS version string, a working raw H4 scan,
or successful Wi-Fi bring-up is not a substitute.

1. Vendor MediaTek drivers, unit-specific firmware and WMT initialization must
   expose and initialize `/dev/stpbt`. `wmtup` alone does not create BlueZ/HCI.
   Adopt only legally available unit firmware; no firmware blobs are added here.
2. Kernel `CONFIG_BT=y` and `CONFIG_BT_HCIVHCI=y`, working `/dev/vhci`, HCI/L2CAP/
   ATT/SMP/management support. Linux 3.18 includes LE in `CONFIG_BT` and has no
   separate `CONFIG_BT_LE`; newer configurations must not explicitly disable it.
   `python3 tools/check_ble_kernel.py <built.config[.gz]>` checks these built-in
   prerequisites. Module builds need a separately verified ABI/load contract.
   This checker neither builds nor approves a kernel or its security state.
3. Build included `cmd/btbridge` statically for ARMv7. After WMT initialization,
   use the stable factory controller address and the audited Dot workarounds:
   `btbridge -bdaddr <factory-bt-address> -max-feature-page 1 -accept-as-peripheral`.
   It owns `/dev/stpbt` exclusively and presents the controller through `/dev/vhci`.
4. Start a system D-Bus and BlueZ `bluetoothd -E` after the bridge. Install
   BlueZ, D-Bus, their complete signed package dependencies/configuration, D-Bus
   user/group/machine-id setup and appropriate service policy. Required APIs:
   `Adapter1` LE discovery/filtering and experimental `ConnectDevice`,
   `Device1` connect/services/pair/remove, `AgentManager1`, `GattManager1`,
   characteristic/descriptor operations. Raw capture/legacy advertising needs
   `CAP_NET_RAW` (and controller setup may need `CAP_NET_ADMIN`); D-Bus service
   policy must allow the daemon to export/register its own GATT/agent objects.
   BlueALSA/A2DP/codec packages are **not** prerequisites of this BLE feature.
5. Persist BlueZ's `/var/lib/bluetooth` on `/data/encore`, including across slot
   updates/rollback, with root-only access. Preserve controller identity and bonds;
   do not regenerate the address each boot. Supervise WMT, bridge, D-Bus and
   BlueZ in dependency order and fence/reinitialize runtime on base restarts.
6. Before marking a candidate slot healthy, run `bluez.Validate(ctx)` with no
   proxy/voice/provisioning worker competing. It probes actual capture permission,
   discovery start/stop, private pairing-agent registration, GATT application
   registration, and advertising/cleanup, even if saved proxy OFF. Failure means
   the required BLE base is not ready: preserve last-good/recovery access rather
   than committing a voice-only slot. Keep USB recovery available regardless.

`Validate` briefly exposes an unauthorized Improv service with no credential
writer. Success proves local interfaces, not over-the-air reception, successful
phone provisioning, remote GATT, three simultaneous physical links, or RF/audio
coexistence. Dot readiness must not be described as hardware qualification.

### Audited pins and kernel source assets

Imported MIT portions and their tests come from `farooqu/techo5` at
`2b091439765d5f881de4b89255b8cf0637d80cba`: `cmd/btbridge/**`,
`echod/internal/lib/bluez/{gatt,peripheral}.go`, Improv wire format, HCI parsing,
and small proxy conversions. Notices are in `ACKNOWLEDGEMENTS.md` and
`licenses/Techo5-MIT.txt`.

`farooqu/techo5-dot` at `b2a50c74bbe8b34785748574aa39fbd045fc4e46` supplies the
audited kernel/base approach, not an Encore-qualified kernel:

- `tools/linux/build-kernel.sh`, `tools/linux/kernel-bt.fragment`,
  `tools/linux/kernel-patches/**` and
  `docs/dumps/biscuit-fireos6-echolocal-boot-3.18.19.config`.
- Dot Amazon source `Echo_Dot_src-6.5.7.1-20251024.tar.bz2`, SHA256
  `2f6b7eed8c09cecf7633f01909c6a4085bef691c29ed0d106c75e7b48c7b4721`.
  Omitted Bluetooth sources restored from upstream `linux-3.18.19.tar.xz`, SHA256
  `3d80d3b8d98c3141d9e26f6c25d73575d688f1c1651b8076f0f2bfd76325b7c9`.
  Reuse the pinned build script's security backports and its license/source
  obligations; don't transplant only the config and call an old kernel secure.
- `tools/linux/rootfs/usr/local/sbin/techo5-bt` for bridge quirks and lifecycle;
  `tools/linux/packages-bt.txt` pins BlueZ 5.86-r2 and D-Bus 1.16.2-r2 there.
  These are source-audit pins, **not** verified minimum versions or a demand to
  replace Alpine 3.22 packages. The integrating Dot prototype uses BlueZ 5.82-r0
  and D-Bus 1.16.2-r1. That exact combination remains to be tested on the Dot.

On Linux 3.18, BlueZ may have GATT but no `LEAdvertisingManager1` because the
kernel lacks Add Advertising. `Provision` uses narrowly scoped legacy HCI
advertising in that case while BlueZ serves GATT. No arbitrary OS-version check
selects it. With BlueZ 5.82, Improv state changes update `ServiceData` on the
existing advertisement via `PropertiesChanged`, without removing and re-adding
it while a client is connected. The signal is not a controller acknowledgement;
the GATT Current State characteristic remains authoritative for the client.
Legacy HCI still updates the advertising data and restarts advertising on disconnect.

## Host API and sequencing

`bluez.OpenScanner(ctx) (*bluez.Scanner, error)` opens the shared BlueZ scanner.
The host owns `Close()`. `bluez.OpenCentral(value func(string, uint16, []byte),
lost func(string)) (*bluez.Central, error)` opens a private GATT client/agent.
Wrap its concrete return in `Options.OpenCentral` returning `ble.Central`.

`bluetooth.New(scanner ble.Scanner, options bluetooth.Options) *bluetooth.Proxy`
takes `Enabled`, `SaveEnabled func(bool) error`, `OpenCentral`, `Pairing` and
`ModeSwitch`. On the BlueZ path use `Pairing: true, ModeSwitch: false` only after
base validation. `SaveEnabled` must atomically save the schema1
`bluetooth_proxy` bool in `/data/encore/config.json`; a failure leaves both live
and saved state unchanged. It is called before applying a switch change.

`(*Proxy).Serve(ctx context.Context, listener net.Listener, info esphome.Info,
key esphome.PSK) error` runs the voice-independent control endpoint and rejects
a zero Noise key. For Assist, assign the same proxy to `assist.Server.Controls`
instead; that server composes one worker lifecycle/listener. Do not call `Run`
twice. Keep encrypted ESPHome controls available in **both** voice modes, not
only while Assist is selected. Capability bits remain stable while OFF, but
slots are zero and radio operations refused. No unauthenticated/plaintext proxy
or automatic PSK provisioning is enabled here.

`SetPaused(bool)` cancels scans/GATT leases and updates slots without changing
the persistent switch. The host can apply voice/RF/privacy policy; this does not
mute or unmute microphones. Stop/cancel Serve and wait before lending the
controller to provisioning. Do not equate microphone hardware mute with radio
power without an explicit device privacy policy.

`bluez.Provision(ctx context.Context, options improv.Options) error` runs
independently of the optional proxy, before voice/proxy on first boot:

- `Options{Name, Version, Hardware string; Authorize <-chan struct{};
  Join func(context.Context, improv.WiFi) (string, error)}`.
- A physical Action event sends an item on Authorize. Closing the channel does
  **not** authorize. Authorization expires after one minute; a failed association
  starts a fresh minute. The complete offer is limited to five minutes.
- Fragmented ATT RPC writes are assembled with a 258-byte frame limit. Only
  device-info capability is advertised beyond Wi-Fi settings; no pretend Wi-Fi
  scan, identify, hostname or name-setting support.
- `Join` must honor its 80-second context, validate/apply candidate Wi-Fi, wait
  for association and an address, then atomically persist successful credentials.
  It returns an optional onboarding URL. Do not log credentials. The result is
  readable for ten seconds before advertising/application cleanup.
- Improv is cleartext at the BLE link layer, unlike encrypted ESPHome. Offer only
  on explicit first-boot setup or locally authorized recovery, not every network
  outage. Keep the USB/ADB/SSH/direct-USB alternatives independent of proxy OFF
  and of any unavailable replacement kernel.

## Reproducible verification and remaining Dot qualification

`make check` runs dependency boundaries, vet and the race-enabled Go suite. Tests
exercise H4 framing with a socketpair controller emulator, asymmetric multiple
raw reports, cancellation/scan disable, malformed batches, private D-Bus GATT
read/write/descriptors/notification, cached-handle reconnect and scoped pairing
agent calls. Improv covers authorization, expiry, fragmented Wi-Fi,
retry/error/result and offer timeout, including first-boot provisioning over
D-Bus without starting a proxy or voice backend. Peripheral reads and managed
objects retain immutable values while notifications update them. The bus daemon
is real, but the exported BlueZ adapter/device objects are test fixtures, not
bluetoothd or a kernel controller.
The private-bus tests skip only when `dbus-daemon` is not installed.

Use the lab's pinned Python environment (`.agents/setup` installs it):
aioesphomeapi 46.30.0, bleak-esphome 4.0.0, and the HA development revision
documented in `lab/README.md`.

```sh
mkdir -p .amp/in/orb-backup
go build -race -o .amp/in/orb-backup/ble-fixture ./lab/bluetooth/satellite
lab/.venv/bin/python lab/test_bluetooth.py .amp/in/orb-backup/ble-fixture
lab/.venv/bin/python lab/test_bluetooth_ha.py .amp/in/orb-backup/ble-fixture
```

This runs real Noise/protobuf against a **virtual** radio: wrong-key rejection,
default OFF/no radio, one switch, exact raw bytes/address/RSSI, asymmetric
UUID/ATT handles, GATT read/write/descriptors/notifications/error, pair/unpair
response ordering, three-slot limit, active/queued connect cancellation,
same-address retry after cancellation, disconnect under queue saturation with
unrelated-operation preemption and another connected peer preserved, 64 retries
during delayed cleanup, delayed OFF/ON and successor-client generation handoffs,
stale slot-refresh ordering, preempted-read terminal errors, failed central
cleanup with no false acknowledgement or successor lease, disconnect overflow,
pause/resume, OFF traffic stop, rapid toggles,
process-restart persistence and failed-save atomicity.

The second suite boots disposable HA and exercises its actual ESPHome config
flow, single default-OFF switch, raw-advertisement parsing, multi-source Bleak
routing, CCCD notifications, cached-service reconnect, competing client
rejection, ATT handle/value limits, OFF disconnect, pause/resume, failed-save
isolation and config-entry unload/reload. Controls work with no voice backend.
Both suites contact only ephemeral loopback listeners and do not modify
production HA. Their GATT peer and pairing outcomes are virtual; neither suite
qualifies BlueZ/RF bonding or changed-service cache invalidation on a real peer.

Before supported-device release, test the exact kernel/base on a physical Dot:
phone/browser first-boot Improv with proxy OFF (physical authorization, expired
auth, rejected credentials, association/address/persistence, reconnect and
cleanup); HA discovery/encrypted control in both voice modes; known and unseen
random/public-address GATT devices; reads/writes/notifications/descriptors;
Just Works bond persistence and unpair; PIN/numeric-confirmation rejection;
simultaneous links; OFF/pause/disconnect/daemon restart cleanup; no stale results;
voice latency, Wi-Fi throughput, CPU/memory and privacy behavior under heavy BLE
traffic. No such hardware qualification was performed in the source orb.
