# Android Bluetooth proxy

The Android bridge is **scan-only**, using public BLE APIs on API 26+ when a
BLE adapter is present. It feeds Encore's shared encrypted ESPHome proxy, not a
dashboard JavaScript bridge. It does not implement active GATT connections,
remote service caching, notifications from connected devices, bonding or
unpairing. Those capability bits remain absent and connection slots remain zero.

The APK's **minimum API is 26**. `targetSdk = 35` selects Android compatibility
rules; it does not require Android 15 or API 35 to install. The build uses SDK 36
to compile available APIs, while runtime version checks guard newer calls.

## What this means for the intended devices

| Target | Implemented path | Still needs device qualification |
| --- | --- | --- |
| Tab A7 Lite, owner-reported LineageOS 21 GSI / Android 14 / API 34 | Raw legacy advertisement forwarding with explicitly unknown address type; Nearby Devices and precise location consent | Exact GSI/vendor Bluetooth driver, actual reception and beacon coverage, consent/revocation, radio recovery, battery/thermal and voice/Sendspin coexistence |
| Portal 2, older AOSP | Same public-API scan-only path if the actual OS is API 26+ and exposes BLE; fine location on API 26–30, Nearby Devices plus location on API 31+ | Actual OS/ABI and APK installability, permission UI/location services, BLE adapter/driver and reception, lifecycle/coexistence; API recommendations in Meta docs do not establish the unit's installed OS |
| API 35+ | Same scan-only path, with public address-type metadata when available | Same hardware/lifecycle checks; newer APIs alone do not enable GATT/cache/pairing |
| No BLE adapter | Saved control remains visible but no radio capability is advertised | No fake scanner or permission request |

Android before API 35 does not publicly supply the public/random address type.
The bridge sends explicit **unknown (255)** instead of inventing one. The pinned
`aioesphomeapi`/`bleak-esphome`/`habluetooth` path preserves this metadata and
parses advertisements as a **non-connectable** scanner. This is a tested client
compatibility convention, not a claim that 255 is a Bluetooth SIG address type.
The shared proxy rejects unknown metadata when it has an active GATT central.
Known address types and existing Linux behavior are unchanged.

## Consent and lifetime

- The `bluetooth_proxy` preference in private `encore.json` is shared by the
  local setup screen and the encrypted ESPHome switch. It defaults ON on fresh
  installs with detected BLE support. Existing installs keep their saved choice.
  The fresh default does not grant permission or let the controller start a scan.
- First-run setup offers Bluetooth after ESPHome pairing. **Not now** keeps the
  preference on while scanning stays blocked; **Turn off proxy** saves Off.
  Revisit at **Settings → Bluetooth proxy**. Nearby addresses,
  raw advertisement payloads and RSSI can disclose device presence. Encore does
  not save a beacon history. Neither voice mute nor music controls imply BLE
  consent; Bluetooth does not start or unmute the microphone.
- Bluetooth-only startup reports the microphone OFF and retains an explicit
  **Start microphone** action (or **Unmute mic** for a saved mute choice).
  Talk is unavailable until local capture starts; permission alone is not
  microphone readiness. Bluetooth never rewrites the saved mute choice.
- Local Bluetooth setting saves run off Android's UI thread. Switch-state
  subscribers use independent, bounded/coalescing writers, so a stalled
  controller cannot hold up the local setting or scan-only lifecycle pause.
  Save failure still leaves live/saved intent unchanged.
- API 26–30 uses the legacy Bluetooth declarations and requests fine location.
  API 31+ requests Nearby Devices and coarse/fine location. There is deliberately
  no `neverForLocation` assertion because it can filter some beacons. Exact
  driver coverage is not guaranteed by omitting that flag.
- Scanning requires the resumed main/setup activity, interactive screen,
  running satellite, saved ON, granted permissions, enabled location services
  and enabled radio, plus an authenticated subscribing ESPHome controller.
  A fresh install also requires explicit **Allow scanning** in local setup.
  It pauses in the background/screen-off. There is **no background-location
  request, background scan guarantee, boot restart or automatic process restart**.
- Recheck grants/radio/location on resume and every 500 ms while the service
  lives. Returning to a valid state resumes an existing subscription without
  changing saved intent. Explicit scan failures require Retry (or radio/grant
  recovery); do not restart in a throttling loop.
- A generation token fences old native reports. Stop clears the native callback
  before IPC; if Android denies cleanup after revocation, the retired registration
  is retained for retry and no new scan can start over it. No radio toggle or
  permission enforcement bypass is used.

The scanner forwards `ScanRecord` bytes, MAC in printed order and RSSI without
reconstructing AD structures. Extended/oversized reports and unsupported address
types are dropped rather than truncated or relabeled. Android may combine scan
responses, filter packets or throttle scanning. Low-power scanning is not a
promise of passive on-air operation, and mode switching is not advertised.

## Deferred active GATT and pairing

**Owner decision (2026-11-17): keep Android scan-only until an upstream-compatible
GATT path exists.** Do not develop a Home Assistant-side compatibility fork for
this implementation. Active connections, connected-device notifications,
pairing and remote caching remain unimplemented and unadvertised. The independent
proxy setup does not change Linux GATT or make Android Bluetooth a first Dot
bring-up gate.

This is not the full originally requested Android GATT bridge. The existing
`core/ble` service contract explicitly requires real, stable ATT handles,
including descriptors. Android's public `BluetoothGattDescriptor` API does not
expose its handle; public characteristic instance IDs do not solve descriptor
identity. Upgrading to API 35 does not remove this boundary. Android itself
supports GATT operations, but a correct ESPHome adapter still needs a deliberate
object/handle mapping and cache-lifetime contract, plus serialized operations,
notification subscriptions, cancellation, reconnect/slot fencing and pairing
qualification. Do not fabricate ATT handles or use hidden APIs to fill the gap.

**The proposed uncached mapping also requires Home Assistant client work.** The
pinned `aioesphomeapi==46.30.0` explicitly rejects a new connection without the
`REMOTE_CACHING` feature, before sending any request. This was executed with
active-connection masks 35 and 43 (without/with pairing); both raise
`ValueError: ESPHome device does not support REMOTE_CACHING feature`. Controls
39 and 47 pass that check and reach the disconnected transport. Merely splitting
Encore's active/cache feature flags cannot provide a working HA GATT proxy.
Advertising remote caching for session-local synthetic handles is not a safe
workaround: cached service/descriptor identities can be reused across sessions
and proxies. The pinned `bleak-esphome==4.0.0` also permits callers to explicitly
reuse cached services with `dangerous_use_bleak_cache`, independently of the
proxy's remote-caching bit.

An uncached Android adapter therefore needs a supported HA/client path with
source/session-scoped attribute identities, fresh service discovery and stale
cache rejection, while Linux retains its existing real ATT handle contract.
This client-side scope has not been approved or implemented; no active GATT or
pairing capability is advertised as a result of this assessment.

Android does publicly provide `BluetoothDevice.createBond()` (API 19+) and
system-managed pairing confirmation. Do not intercept/auto-confirm those
prompts or supply invented PINs. However, the public `BluetoothDevice` API
through SDK 36 exposes neither `removeBond()` nor `cancelBondProcess()`;
remote forget/cancel cannot be promised using those hidden methods. A future
adapter must report actual bond completion, preserve user denial and direct
unsupported forget operations to user-controlled Android Settings instead.

## Reproducible checks and limits

```sh
go test -race ./platforms/android ./devices/android-generic/mobile
make check
go build -race -o .amp/in/orb-backup/android-ble-fixture ./lab/androidbluetooth
lab/.venv/bin/python lab/test_android_bluetooth.py .amp/in/orb-backup/android-ble-fixture
devices/android-generic/build-android :app:assembleDebug :app:testDebugUnitTest :app:lintDebug --continue
```

The mobile fixture uses the regular 6053/6055 listeners on the test host; use an
isolated host with those ports free. It simulates the native callbacks and uses
real Noise encryption and the pinned HA Bluetooth client. JVM tests use Android
framework shadows for permission policy plus native Compose rendering and
switch interaction. The APK build compiles the ARM32/ARM64 JNI bridge, but these
host tests do not execute the Kotlin scanner/service/JNI lifecycle on Android.
Actual registration, RF reception, OS revocation/radio recovery and coexistence
remain physical qualification gates. No supported-device claim follows from a
green build; there is no device interaction recording from this orb.
