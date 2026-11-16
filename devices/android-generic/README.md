# Generic Android: planned APK target

**Planned, never released, not installable.** This directory contains a
planning profile and package notes, not an Android app, build, installer or
on-device validation. **Samsung Galaxy Tab A7 Lite is the intended first validation
tablet**, not a tested compatibility claim. Other devices in the root README are
future intentions, not a supported/tested list. See the
[package contract](../../docs/device-packages/README.md).

## Local ownership

This target owns its explicit composition, target profile, APK packaging
inputs and tested-device records. Shared Android service/Activity, audio,
permissions, IPC and display integration belong in `platforms/android/`, not in
this device directory. Add measured model-specific profiles only when justified;
do not create a separate APK simply because a tablet has another brand.

`profile.json` records target identity and planned status only; it contains no
audio settings or capability declarations. Future Android integration must not
inherit Dot wiring, EQ, gain or calibration. Ordinary microphone
input does not imply a raw/reference pair, aligned capture, reliable platform AEC,
or Incubator's second processed capture/enrollment capability. OS effect
availability is not proof of cancellation quality; never stack OS and software
AEC blindly. Screen/touch/brightness and dashboard intent are not implemented
facilities in this scaffold.

## Source evidence and intended assembly

[Techo5 Panel's PanelService](https://github.com/farooqu/techo5-panel/blob/14c16fea2fbc64ce9b706009a3fed16766523e6d/app/src/app/techo5/panel/PanelService.java)
uses native `AudioRecord`/`AudioTrack` and starts an APK-bundled Go executable as a
child process from the native-library directory. Its
[LocalPeer check](https://github.com/farooqu/techo5-panel/blob/14c16fea2fbc64ce9b706009a3fed16766523e6d/app/src/app/techo5/panel/LocalPeer.java)
restricts local peers to the app UID. This is implementation evidence for the
intended design, **not fresh hardware validation, an Encore port or a guarantee
that current Android versions permit every execution/provisioning mode**.

A future target composition root would select the shared Go engine, native
Android host, this profile and only the native Incubator backend. Assist remains
in the repository for a possible later, separately qualified APK for the
**Home Assistant Assist pipeline**. Incubator is the default APK; the Assist APK
would be explicitly opt-in. Do not
bundle a second voice backend or runtime backend selection/fallback. One signed
APK must bundle its matching host and engine for each explicitly supported ABI. The host
owns engine lifetime independently of dashboard reloads; it adapts
actual audio routes to the engine's bounded mono PCM16/16 kHz capture contract.
IPC must check same-UID peers on both sides, reject incompatible protocol versions,
and preserve bounded audio plus prompt control/cancellation. No socket, protocol,
executable or handshake is implemented here, and no Dot-specific code belongs in
the APK. Do not fetch a separately updated engine executable.

Native playback needs real position/timestamp-based drain validation; a completed
`AudioTrack.write` or socket delivery is not proof the speaker finished. Permission
loss/mute must stop or gate capture immediately and clear queued/pre-roll audio.
The native dashboard/overlay must not expose a general device-command bridge to
dashboard JavaScript. These are acceptance requirements, not existing behavior.

## Prerequisites still to establish on the first tablet

| Area | Missing fact/decision |
| --- | --- |
| Device / OS | Exact Tab A7 Lite model, firmware/Android version, patch level and maintained OS/base path; no minimum/supported API range selected. |
| Binary compatibility | Supported CPU ABIs, Go/NDK/JDK/Android SDK toolchain, native dependencies and APK executable extraction/launch behavior; do not inherit Panel's ABI/API matrix as Encore support. |
| Permissions / lifecycle | Microphone permission, foreground-service rules/types, notifications, audio focus/routes, background/reboot recovery and battery restrictions under the actual OS. |
| Provisioning / kiosk | Ordinary user-granted installation and operation first; root, device-owner privileges, lock-task/kiosk mode and unattended installation are not assumed or supplied. |
| Installation / signing | App ID, signing identity and secure key custody, versionCode mapping, user-consent/update flow and failed-install recovery are unresolved; no debug key or privileged installer is provided. |
| Audio / display | Actual sample routes, platform processing quality, wake during playback, drain/stop timing and display-control privileges; no measurements recorded. |
| Ownership | Named hardware maintainer and reproducible acceptance record still required before a support commitment. |

Application updates are signed APK replacements, independent of maintained Android
OS/security updates. Preserve Android signature/version constraints through a
centrally reviewed packaging policy; core SemVer and package revision alone are
not an Android versionCode algorithm. Current central revision is zero and release
support is false. There is no APK, updater or installation procedure to run.
Future package/update metadata must bind the backend variant and routine updates
must preserve it. A later change to Assist requires explicit application-package
replacement with validated state migration/recovery and Android signing/version
compatibility, not a runtime toggle or an OS reflash.

Before support is claimed, record actual tablet/OS/ABI/toolchain and pinned backend
versions plus native Incubator voice/timer behavior, capture/playback/drain, wake
during playback, immediate stop/mute/permission revocation, network/audio-route recovery,
dashboard reload without voice loss, background/reboot behavior under real
provisioning and a signed same-backend app update without an OS update. Inspect
the APK dependency closure and contents to exclude the Assist adapter. Assist
qualification and live-switching tests are not gates for the Incubator APK.
Measure resource use, latency and underruns. A profile check or a future successful
APK build does not establish any of those hardware results.

## Acknowledgement

Techo5 Panel and Techo5's upstream work informed this boundary. See
[ACKNOWLEDGEMENTS.md](../../ACKNOWLEDGEMENTS.md) and the
[package attribution/licensing caveat](../../docs/device-packages/README.md#evidence-attribution-and-licensing).
No Panel Java/Go code, UI assets or models are copied here. New Encore material's
license remains unselected; audit all included material before distribution.
