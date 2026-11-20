# Generic Android: Incubator development APK

This target now contains an **Incubator-only Android app**, with a native Kotlin /
Jetpack Compose host and an in-process Go engine. It is not a released or
hardware-qualified package. Samsung Galaxy Tab A7 Lite is the first device to
try; Meta Portal installation remains unverified, not ordinary tablet support.
`profile.json` and the central release registry remain planning/revision-zero
metadata, not runtime settings or a release declaration.

## Optional illuminance

The service discovers an actual Android `Sensor.TYPE_LIGHT` at startup. Only
sensor-equipped hosts list the ESPHome **Illuminance** measurement entity (`lx`);
no model/SKU assumptions or required manifest hardware feature are added. A
registration/permission failure retains a supported entity with unknown state,
not a permanently invented sensor on hardware that lacks one. Values are Android
HAL-reported lux, not an Encore calibration claim.

An independent low-rate listener/worker owns readings, without microphone/audio
focus resources. Event timestamps determine age; polling never refreshes a cached
sample's timestamp. Values older than 30 seconds, invalid readings and errors
publish missing state. On-change sensors are re-registered after 20 seconds
without a new event to request another initial sample; an old HAL timestamp is
still rejected. Stop unregisters the listener and clears the value. Sensor
capability is rediscovered on service/satellite restart, not hot-added to an
already connected catalog. No new dangerous permission is required.

This is optional follow-on functionality, not a first-dev-release gate. Native
and protocol fixture checks are not a physical pass: the first Tab A7 Lite is
owner-confirmed LineageOS 21 GSI / Android 14 / API 34, but actual HAL samples,
background behavior and voice/music coexistence still require qualification.
Portal 2 remains a separate unqualified target. Biscuit's separate
[driver audit and integration gates](../biscuit/ILLUMINANCE.md) still apply.

## Build locally

Requirements: Go 1.27, JDK 21, Gradle 9.4.1, Android SDK platform/build-tools 36,
NDK 28.0.13004108, Meson (0.63 or newer), and Ninja. Set `ANDROID_HOME` and put
Gradle on `PATH` (or set `GRADLE` to its executable). From the repository root:

```sh
devices/android-generic/build-android
```

The script builds the Go engine and AEC3 library for `arm64-v8a` and
`armeabi-v7a`, downloads and SHA-256-checks a pinned SendspinDroid player library,
then builds `app/build/outputs/apk/debug/app-debug.apk` under this directory.
It does not install, upload, publish or update a device. Gradle uses its normal
local development signing key; do not use that identity for a production release.
Debug builds keep application ID `com.encore.satellite.incubator.dev`, default
versionCode 3 (`0.1.2-incubator-dev`), minimum API 26, target API 35. Signed release
builds use `com.encore.satellite.incubator` for both dev and stable update channels.
This is a separate app with separate data: existing debug installs remain untouched,
with no automatic uninstall or migration. Release signing inputs are never
provisioned by the source tree; protected signing still needs end-to-end validation.

The separate [signing/update foundation](../../release/android-updates.md) adds
APK CI checks, an explicitly gated signed-candidate workflow, authenticated
stable/dev feeds, a persistent ESPHome **Update channel** dropdown, available-update
reporting and an install request. Changing channel only changes subsequent checks;
it never installs or downgrades. Feeds/keys are not provisioned by default. Android
installation still requires local approval; use **Settings → Review downloaded
update** or the update notification after requesting it through ESPHome. Existing
debug installs cannot accept a different production signing key in-place.

Use a build machine with sufficient RAM: the initial 2 GB orb exhausted memory;
the complete APK/test/lint build passed in an `a1.large` orb. Default settings use
two Gradle workers and a 3 GB Gradle heap.

## Voice AEC3

Voice capture uses AEC3 by default in debug and release APKs. The build compiles
the checksum-pinned freedesktop WebRTC Audio Processing 1.3 source for both ARM
ABIs, with Abseil pinned by the upstream Meson wrap, and packages the JNI library
and dependency notices. No Kiosk code, OS changes, root, playback-capture grant,
or physical loopback input is used.

The assistant `AudioTrack` provides the accepted 16 kHz mono PCM for replies,
announcements and timer cues that reach that sink. Accepted writes only enter
the queue; the track timestamp/playback head determines which portion was
presented. Capture and render are mapped to the same monotonic clock, with
advancing-head and read-completion fallbacks when hardware timestamps are not
plausible. Flush retains the presented prefix for in-flight echo, never the
discarded queue. Route changes, underruns with outstanding PCM and clock jumps
invalidate the old mapping. Unusable timing substitutes silence, not raw mic
audio; native initialization or processing failure uses the existing capture
failure/mute path. This is still experimental timing: the PCM tap is before
Android's system volume, mixer and vendor DSP, not a measurement at the speaker.
Sendspin uses a separate `USAGE_MEDIA` sink; this build does **not** cancel its
audio, Android/system sounds or other apps. Keep Sendspin off for the voice A/B.

An x86-64 host JNI check with synthesized speech and two delayed echo taps
measured 45 dB echo reduction at 181 and 601 samples of lag, versus about 0 dB
without a reference. Output RMS during speech overlap was 2.5–3 dB below the
clean near-end input level; with overlapping broadband noise it was about 20 dB
below. These are fixture measurements, not evidence that the device recognizes
spoken Stop under overlap.

Local ordinary wake (idle), spoken Stop (armed for response playback,
announcements and alerts), and active/follow-up call input share the one capture
path through the existing Incubator adapter. The APK supplies AEC3's
output on the existing primary call route. `listen` means call listening and
follow-up, not an off-call mic monitor. `aec`, `monitor`, `monitor_aec`, and
`intercom` remain false, with no `monitor_result` claim. Do not enable call AEC
until measured; the shared Biscuit precedent sends the **same** cleaned mono
PCM on `0x08` before `0x03` with zero relative lead and matching pre-roll. No
second physical lane is required by the protocol.

The existing model-upload path accepts compatible mixednet models; collecting
training samples is a separate capability. Off-call Live Monitor, Enroll Voice,
Teach Wake Word, and two-way intercom remain unadvertised pending qualification.
The Android UI connects its foreground mic indicator to the shared monitor gate.
For an admitted request, the screen shows the visible mic edge band without
requiring a local tap. Android confirms the exact request only after
fresh mic samples arrive and the indicator draws in a shown, focused window.
The gate sends no pre-roll and does not resume a previous stream automatically.
Stop cancels the request; Mute cancels it and latches mic privacy. Permission or
capture loss, leaving the focused screen, and disconnect also end the stream.
The service notification and OS mic dot do not replace the app indicator. Keep
this unauthenticated protocol on an isolated trusted LAN.

For full intercom and explicit monitor admission, the required companion is
the unpublished `encore-loopback-intercom` revision
`ef556aea47337be17340d1499acd99e9b2f3b9ce`, based on upstream
[`ffd4f23`](https://github.com/OHF-Voice/voice-incubator/commit/ffd4f23520d8e4c845741a0c9da797b50995d5f6).
See the [paired verification and patch handoff](https://ampcode.com/threads/T-01a1f425-c4ad-7081-b906-3b3728b201d8).
Stock `v0.3.3` emits unmarked connect tones. The compatible companion marks
cues as announcements, waits for both connect drains before relay, correlates
request IDs with `monitor_result`, and retires both relay streams on Stop without
acknowledging discarded playback as drained. The companion is necessary, not
sufficient: Android still needs its own route qualification and recording gate.

On the exact LineageOS Tab A7 Lite and Meta Portal units, record model/ABI,
OS and vendor builds, selected routes and input effect state. Compare identical
levels and speech with a pre-AEC3 build, the current build, and (as a separate
comparator) Android's session AEC on `VOICE_COMMUNICATION`. Measure echo reduction, self-wake/false
Stop, near-end and overlapping speech, Stop latency, route/focus recovery,
sustained delay/drift, CPU/heat and underruns. Use real Incubator model push,
wake/reply and follow-up; then qualify monitor indicator and teardown, intercom,
Live Monitor and actual enrollment/training with the matched companion. Neither
the synthetic Android tests nor prior Biscuit virtual intercom runs qualify
these devices, acoustic timings or model-backed enrollment/training.

## Use the development app

1. Install the debug APK using Android's ordinary user-approved sideloading flow.
   Open Encore and choose **Allow microphone access** if prompted. Notifications
   are optional. Encore starts its microphone/media foreground service automatically
   once microphone access is granted. No boot receiver, root, device owner or
   unattended kiosk privileges are required.
2. On a trusted LAN, pair the device with Voice Incubator using the advertised
   `_voice-incubator._tcp` service, or its IP and TCP port 6055. This protocol is
   **unencrypted/unauthenticated**; do not expose it to untrusted networks.
   Incubator sends mixednet models, which execute locally through the compatible
   microWakeWord frontend. No wake model is bundled or downloaded independently.
   Readiness combines usable ordinary wake models with current native capture
   health; a stop-only model does not mean hands-free wake is ready. **Talk** is a
   manual fallback, not a replacement for local wake detection.
3. On a fresh install, tap **Pair with Home Assistant** on the Android setup screen.
   On a trusted LAN, open Home Assistant **Settings → Devices & services** and
   add the discovered satellite (or add ESPHome by tablet IP, TCP 6053) within two
   minutes. Use a Home Assistant version that supports zero-PSK provisioning.
   HA generates the permanent encryption key; no key entry is required. If the
   window expires, tap **Pair with Home Assistant** again. If HA adds the device
   but controls do not appear, reload its ESPHome integration. Existing installs
   keep their key and settings and skip pairing; **Reveal ESPHome encryption key**
   in local Settings remains available for manual recovery after pairing.
   Set the single **Dashboard URL** text entity
   to a full URL, for example `https://home.example/lovelace/home`. This supplies
   both the HA origin and dashboard path; no separate tablet origin setup exists.
   Prefer HTTPS; explicitly choosing `http://` permits trusted-LAN HTTP, including
   unencrypted HA sign-in traffic. The paired ESPHome controller can change the
   full URL, including its origin. Invalid URLs retain the previous configuration.
   **Reload dashboard** is a separate button entity. No dashboard list or backend
   selector exists. Previously configured full dashboard URLs remain valid.
   The read-only **Microphone muted** (`microphone_muted`) binary sensor reports
   the app's microphone privacy latch and updates when it changes. It does not
   report Android permission or recorder readiness and cannot unmute the app.
4. Sign into HA in the dashboard itself, or use **Settings → Sign in with
   phone** on the tablet. Phone sign-in requires HA 2026.11 or later, an up-to-date
   Android System WebView, and an HTTPS HA address trusted and reachable by both
   devices. Connect the phone and tablet to the same trusted local IPv4 network
   (not isolated guest Wi-Fi).
   - Scan the tablet's QR code within five minutes. Check the HA address on the
     phone before signing in. If your phone normally uses your main HA account,
     open the link in a private browser tab and sign in as your non-admin
     satellite account. You do not need to switch accounts in the HA companion app.
   - Return to the tablet and verify the account shown before tapping **Use this
     account**. Repeat on each satellite; the same account can have independently
     revocable sessions and each satellite keeps its own ESPHome Dashboard URL.
     The session has the account's normal HA permissions, not dashboard-only access.
   - If a session is lost or revoked, tap **Sign in with phone** again, or choose
     **Sign in on tablet** from the recovery screen. To revoke one tablet from HA,
     compare the Session ID shown in Encore Settings with the HA profile's
     refresh-token list for the satellite account under **Security**. You can also
     sign out from that tablet's dashboard. No need to re-pair ESPHome.
   Manual sign-in still works, including when the QR flow is unavailable. The
   tablet WebView permits only same-origin navigation (no external-IdP allowlist).
5. Optionally connect a **current Noise-capable Sendspin server** by host:port in
   Settings. Reveal the independent Sendspin pairing token locally and provision
   it on the server. The player uses encrypted, Keystore-protected identity/trust;
   it does not silently fall back to plaintext or admit unpaired playback.
   Older plaintext-only servers are not compatible. Music can be disconnected
   without changing voice, and microphone mute does not disable music. A voice
   turn reduces the music track gain to 15%, retaining player volume/mute and sync.

The foreground service owns audio and network sockets independently of dashboard
reloads and Activity rotation. The notification's **Stop satellite** action stops
it until Encore returns to the foreground; reopening never silently reverses
persisted microphone privacy. The service does not start itself in the background
after reboot or process death.
The music server address/connection is entered explicitly each time; identity and
pairing records persist. The screen stays awake while the Activity is visible.

## Display and privacy behavior

- The dashboard stays visible and usable during voice. The voice controls sit on
  the right in landscape and along the bottom in portrait. Trigger assistant
  becomes Stop in the same spot until the turn and any playback drain finish.
  The primary action is disabled while stopped; on a running Bluetooth-first
  install it can still explicitly start the microphone. Mute remains a separate
  privacy latch. The gear opens full-screen Settings, where music controls live.
- A continuous mint band shows when Encore is taking mic input, including during
  silence. A brighter highlight flows around the band with fresh mic RMS, and a
  lilac highlight travels the opposite direction with
  measured response playback. Quiet input gets a visual lift without changing
  recording gain. Queued audio and missing samples do not create motion. This
  visualizes levels; it does not distinguish speech from other sound or echo.
- Processing and alerts have distinct edge colors. Healthy idle has no band;
  mute, disconnect and unknown mic state remove any live mic indication without
  a fade. Android's disabled-animations setting keeps the level changes but
  removes the traveling highlight. Night mode dims the display-edge treatment.
  Voice status is available on the Stop control to screen readers without visible
  text over the dashboard. Transcript text is not persisted. Muting clears session
  pre-roll, cancels active voice, and releases the microphone. Android screenshots
  are disabled for the running Activity, including locally revealed pairing keys.
- Only server-requested Incubator follow-ups exist. No Assist, runtime route
  switching, Assist-only follow-up toggle, enrollment stream, advertised wire
  AEC route, or ESPHome `media_player` is included.
- The optional [Android Bluetooth proxy](../../docs/bluetooth-android.md) is
  independently default-OFF and scan-only on API 26+ with a detected BLE adapter.
  Settings explains consent and version-specific grants. Scans pause outside a
  visible Encore activity; GATT, remote caching and pairing are not implemented.
  Tab A7 Lite / LineageOS 21 GSI and Portal 2 still need physical qualification.
- Dashboard JavaScript has no native command bridge, file/content access,
  camera/mic grants or external-intent launch path. With phone sign-in, only the
  configured HTTPS origin's main frame can request the current HA access token
  or revoke it via HA's external-auth bridge. TLS errors are not bypassed. The
  QR code contains no credentials; the temporary local callback receives a
  single-use PKCE-bound code, and HA credentials are exchanged only over HTTPS.
  The tablet stores its refresh token encrypted with Android Keystore. Navigation
  stays within the origin derived from the ESPHome URL; changing origin recreates
  the WebView without sharing the session or interrupting voice or music.

## Checks

```sh
make check
devices/android-generic/build-android :app:assembleDebug :app:testDebugUnitTest :app:lintDebug
python3 docs/device-packages/validate.py
```

Go tests exercise model transfer/digest/capacity/privacy plus existing voice and
drain contracts. JVM Robolectric/Roborazzi tests render the actual Compose controls and
exercise touch shielding through drain, stop/privacy access at enlarged text and
short/narrow sizes, transcripts, queued/output distinction, simultaneous independent
input/output, silence, reduced motion, between-snapshot ribbon movement,
missing-sample clearing, speaker fallback, capability-gated music and same-origin navigation.
An AudioRecord PCM fixture checks stale history, input epochs, capture health and
permission loss without a real microphone. An AudioTrack fixture checks accepted
PCM envelope bins against a controlled playback head, including silence,
exhaustion, flush and stale epochs. Native-rendered test
captures are written to `app/build/reports/screenshots/`; their audio, transcript
and music states are explicitly test fixtures, not on-device evidence.

The animation development build passed matching-AAR APK assembly, all thirteen native
JVM tests and lint (zero errors, 23 warnings, including deliberately unsupported
x86/ChromeOS, newer-version and existing KTX/storage suggestions). The APK's v2
development signature and ARM-only native library contents were independently checked. These checks do not establish
installation, microphone quality, real Sendspin playback or physical audio timing.
The full-URL configuration, origin changes, invalid-input rejection, persistence
and voice-independent controls were also exercised through a real encrypted
`aioesphomeapi` client in the earlier one-URL build. Animation work does not change
the package ID, signing configuration or versionCode; on-device updating remains untested.

The remaining practical check is installation and a real Incubator turn on the
Tab A7 Lite: model push/local wake, capture/reply audio, playback-head drain,
stop/privacy/permission loss, music duck/restore, HA login/dashboard reload,
background survival and route/network recovery. No physical-device result is
claimed. Portal sideload access/OS/ABI/WebView and hardware mic/speaker access must
be established on the exact unit before claiming it works there.

Before support is claimed, record the actual tablet/OS/ABI/toolchain and pinned
backend versions, native Incubator timer behavior, wake during playback, measured
resource use, latency and underruns. Inspect the APK dependency closure and contents
to exclude the Assist adapter. Assist qualification and switching tests are not
gates for the Incubator APK.

Application updates are signed APK replacements, independent of Android OS/security
updates. Preserve signature/version constraints through a centrally reviewed
packaging policy; core SemVer and package revision alone are not an Android
versionCode algorithm. The central ledger reserves experimental Android revision 5
with core `0.1.1-dev.1`; this is not a built or published release. Automatic release
support remains false. Final package/update metadata must bind the backend variant and routine updates
must preserve it. A later, explicitly opt-in Assist package requires its own
qualification, validated state migration/recovery and signing/version compatibility,
not a runtime toggle or an OS reflash. The update foundation is not an activated
production update service or a silent installer; see its provisioning and device
qualification gates before using it for distribution.

## Ownership and attribution

This directory owns composition and APK inputs. Reusable Android Activity/service,
audio and display code lives in `platforms/android/`; shared frontend/executor code
lives in `core/wake/`. No Dot hardware settings are inherited. The Go library is
bundled in the APK, not fetched as a separate executable.

See [ACKNOWLEDGEMENTS.md](../../ACKNOWLEDGEMENTS.md) for pinned Techo5/frontend,
mixednet and SendspinDroid provenance. Included notices are also packaged as APK
assets, alongside the [MIT License](../../LICENSE) for Encore-owned material.
Third-party material retains its own terms; release distribution still needs a
complete dependency/license and corresponding-source review.
