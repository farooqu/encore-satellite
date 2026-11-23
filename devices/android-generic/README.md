# Generic Android: Incubator development APK

Use this app to add Incubator voice to an Android display. You can show a Home
Assistant dashboard with a conversation overlay. For personal voice on a phone,
use the [Assistant app](../android-assistant/README.md).

We installed the signed 0.1.63 APK on a Galaxy Tab A7 Lite (SM-T220, Android
14, LineageOS 21 GSI) and reached **Pair with Home Assistant**. We stopped
before pairing, voice, or OTA tests. We haven't verified a Meta Portal install.

## Install the signed APK

Start with an Android 8 (API 26) or newer device that can run ARM64 or ARMv7 apps.
Download `encore-android.apk` from an `android-<version>`
[development release](https://github.com/farooqu/encore-satellite/releases?q=tag%3Aandroid-).
Compare its SHA-256 with the asset digest on
GitHub before installing (`shasum -a 256 encore-android.apk` on macOS, or
`sha256sum encore-android.apk` on Linux). Open the APK on the tablet and accept
Android's install prompt, or use USB with [Android platform-tools](https://developer.android.com/tools/releases/platform-tools)
and USB debugging enabled and approved on the tablet:

```sh
adb devices -l
# Set TABLET_SERIAL to the intended device listed as "device", not "unauthorized".
adb -s "$TABLET_SERIAL" install encore-android.apk
adb -s "$TABLET_SERIAL" shell pm path com.encore.satellite.incubator
adb -s "$TABLET_SERIAL" shell dumpsys package com.encore.satellite.incubator | grep -E 'versionName=|versionCode='
```

Look for `Success`, a `package:` path, and the version you chose. Use
`install -r` to keep settings when you upgrade the same signed app.
Uninstalling deletes pairing and settings. The debug APK has a separate app ID
and can stay installed.

Open Encore and follow [first-run setup](#set-up-the-app). Pair with Home
Assistant before connecting Incubator. Test **Talk** before setting up a
dashboard, changing the Android Home app, or enabling remote installs.

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
it never installs or downgrades. Feeds/keys are not provisioned by default. On
Android 12+, opt in under **Settings → Device setup → Remote install settings**
and allow APKs from Encore in Android Settings. After setup, only an explicit
Home Assistant **Install** request can start a native self-update. If Android
requires confirmation or native updates are off or unavailable, use the Settings
review action or update notification. See [Android updates](../../release/android-updates.md)
for checks and restart behavior. Debug and production identities and signing keys
are not interchangeable.

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
follow-up. Android separately advertises `monitor` and `monitor_result` for
Live Monitor on the primary route. `aec`, `monitor_aec`, and `intercom` remain
false. Do not enable call AEC
until measured; the shared Biscuit precedent sends the **same** cleaned mono
PCM on `0x08` before `0x03` with zero relative lead and matching pre-roll. No
second physical lane is required by the protocol.

Live Monitor sends current primary PCM and report-only wake scores for any
active mixednet models. Voice enrollment and Teach Wake Word require the separate
AEC monitor route and remain unavailable, as does two-way intercom.
[Voice PE at `8ba314e`](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/PROTOCOL.md#monitoring-and-the-intercom)
also streams primary PCM and reports detections without answering, but can
advertise AEC capture and intercom. Android leaves those routes off until
qualified. Instead of Voice PE's ring, Android uses a foreground screen
indicator: it cannot provide a persistent local recording cue while hidden.
For an admitted request, the screen shows the visible mic edge band without
requiring a local tap. Android confirms the exact request only after
fresh mic samples arrive and the indicator draws in a shown, focused window.
The gate sends no pre-roll and does not resume a previous stream automatically.
Stop cancels the request; Mute cancels it and latches mic privacy. Permission or
capture loss, leaving the focused screen, and disconnect also end the stream.
The service notification and OS mic dot do not replace the app indicator. Keep
this unauthenticated protocol on an isolated trusted LAN.
`go test ./devices/android-generic/mobile -run TestAndroidOffersPrimaryMonitor`
checks the advertised routes, indicator admission and revocation on the wire.

For future full intercom support, the reference companion is
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

## Set up the app

1. Open the [installed app](#install-the-signed-apk) and choose **Allow microphone
   access** if prompted. Notifications are optional on Android 13+.
   Encore starts its microphone/media
   foreground service automatically once microphone access is granted. No boot
   receiver, root, device owner or unattended kiosk privileges are required.
   Microphone capture, Incubator and Sendspin stay off until ESPHome pairing succeeds.
2. On a fresh install, tap **Pair with Home Assistant** on the Android setup screen.
   On a trusted LAN, open Home Assistant **Settings → Devices & services** and
   add the discovered satellite as **Encore Android · 0AB2** (or add ESPHome by
   tablet IP, TCP 6053) within two minutes. The friendly name ends in the last
   four uppercase hex digits of the app's saved, generated MAC. The ESPHome
   node name is `encore-android-` plus the last six hex digits of that MAC, for
   example `encore-android-350ab2`. Use a Home Assistant version that supports
   zero-PSK provisioning.
   HA generates the permanent encryption key; no key entry is required. If the
   window expires, tap **Pair with Home Assistant** again. If HA adds the device
   but controls do not appear, reload its ESPHome integration. Existing installs
   keep their key and settings and skip pairing. For manual recovery after
   pairing, go to **Settings → Connection details → Reveal ESPHome encryption key**.
   The read-only **Microphone muted** (`microphone_muted`) binary sensor reports
   the app's microphone privacy latch and updates when it changes. It does not
   report Android permission or recorder readiness and cannot unmute the app.
3. After ESPHome pairing, connect Voice Incubator using the advertised
   `_voice-incubator._tcp` service, or the tablet IP and TCP port 6055. This
   protocol is **unencrypted/unauthenticated**; use it only on a trusted LAN.
   Incubator sends mixednet models, which execute locally through the compatible
   microWakeWord frontend. No wake model is bundled or downloaded independently.
   Readiness combines usable ordinary wake models with current native capture
   health; a stop-only model does not mean hands-free wake is ready. Tap **Talk**
   and confirm you hear a reply. Then assign a wake word in Incubator and try it
   separately. If Talk works but wake doesn't, check model assignment and mic
   readiness rather than repeating ESPHome pairing.
   First-run setup then offers Sendspin music and, on supported hardware, the
   Bluetooth proxy. The proxy defaults On but needs local access
   and scan approval. Dashboard, Android Home and remote installs are optional.
   Choose **Done** to use Encore without a dashboard; **Settings → Device setup**
   reopens the checklist.
4. Tap the Encore display, then open **Settings → Dashboards**. Enter the Home
   Assistant server address (for example `https://home.example`) and tap **Choose dashboards**.
   Sign in on the tablet, then select up to five Home Assistant dashboard views
   in the order you want them and tap **Save**. Encore opens the first selection.
   Swipe left or right across the display to switch pages. The clock is the page
   before the first dashboard; **Show only clock** removes the dashboard pages.
   The pages slide into view unless Android animations are disabled. Dashboards
   stay loaded when you switch pages, including when you return to the clock.
   There is no screensaver; only active Now Playing can trigger an idle return.

   Encore hides the Home Assistant sidebar and Lovelace header in the display
   WebView, without changing the dashboard or installing a Home Assistant plugin.
   This is display styling, not an account restriction or Android lockdown.

   The old ESPHome **Dashboard URL** setting is no longer used. Select views on
   the tablet to replace it. The ESPHome **Reload dashboard** button still reloads
   the visible dashboard.
   Prefer HTTPS: `http://` also sends tablet sign-in traffic unencrypted.

   You can use **Sign in with phone** from the picker or Encore Settings.
   Phone sign-in requires HA 2026.11 or later, an up-to-date
   Android System WebView, and an HTTPS HA address trusted and reachable by both
   devices. Connect the phone and tablet to the same trusted local IPv4 network
   (not isolated guest Wi-Fi).
   - Scan the tablet's QR code within five minutes. Check the HA address on the
     phone before signing in. If your phone normally uses your main HA account,
     open the link in a private browser tab and sign in as your non-admin
     satellite account. You do not need to switch accounts in the HA companion app.
   - Return to the tablet and verify the account shown before tapping **Use this
     account**. Repeat on each satellite; the same account can have independently
     revocable sessions and each satellite keeps its own dashboard selection.
     The session has the account's normal HA permissions, not dashboard-only access.
   - If a session is lost or revoked, tap **Sign in with phone** again, or choose
     **Sign in on tablet** from the recovery screen. To revoke one tablet from HA,
     compare the session ID in **Settings → Connection details** with the HA profile's
     refresh-token list for the satellite account under **Security**. You can also
     sign out from that tablet's dashboard. No need to re-pair ESPHome.
   Manual sign-in still works, including when the QR flow is unavailable. The
   tablet WebView permits only same-origin navigation (no external-IdP allowlist).
5. Sendspin is on by default after pairing with Home Assistant. On a fresh
   tablet, choose **Continue setup** at **Music (optional)** to keep it on, or
   **Turn off music** if you don't want it. You can change this later in
   **Settings → Connection details** on the tablet or with **Sendspin music**
   under the tablet's ESPHome device configuration in Home Assistant.
   With [automatic pairing](../../docs/sendspin-targets.md#automatic-pairing-through-home-assistant)
   configured, Music Assistant pairs without token entry, and you can finish
   tablet setup without waiting for it to connect.
   Encore automatically browses for `_sendspin-server._tcp.`
   on the local network and connects when it finds exactly one compatible server.
   If none or more than one are found, it waits and tries again. Choose
   **Specify server** at any time to connect to a `host:port` with an optional
   path (default `/sendspin`). The app remembers the address until you choose
   **Connect by auto-discovery**. This setting is local to Android, not exposed
   through ESPHome. Neither connection method grants trust. For an encrypted
   server without auto-pairing, **Reveal Sendspin pairing token** in connection
   details and enter it in the server's pairing UI. The app stores
   identity and trust with Android Keystore encryption. Unpaired playback and
   plaintext-only servers are not supported.
   **Turn off music** stops discovery and playback, and keeps trust;
   **Revoke all Sendspin trust** also removes trusted servers and rotates the
   token without replacing the device identity. Keep the token private. Leave
   music off if you don't want a connected Music Assistant to auto-pair again.

   Use Music Assistant for queues and grouping. The clock page becomes
   **Now Playing** with the time, large album art when supplied, play/pause,
   local Stop and supported skip controls. Swipe to a dashboard to use the
   floating player. After leaving full Now Playing, Encore returns to the clock
   after 30 seconds without touch only while music is playing with audio focus.
   **Minimize** keeps the floating player on the clock and dashboards without
   an automatic return. Tap its title to expand. The player yields to voice and
   timer panels and disappears when the group or tablet stops.
   Playback controls are also on the main Satellite settings page. A voice turn reduces music
   track gain to 20% of its current player volume without pausing the group.
   **Stop music**, explicitly enabling microphone privacy, and Android's
   headphone/route-loss signal latch local music output off. Releasing privacy
   does not resume playback; choose **Play** explicitly. Voice Stop still
   cancels voice independently of music.

The foreground service owns audio and network sockets independently of dashboard
reloads and Activity rotation. The notification's **Stop satellite** action stops
it until Encore returns to the foreground; reopening never silently reverses
persisted microphone privacy. The service does not start itself in the background
after reboot or process death. An enabled player resumes discovery or reconnects
to the manually selected address when Encore next starts, without clearing a
saved local Stop. The screen stays awake while the Activity is visible.

## Use Encore as the Android Home screen

Encore still opens as a regular app from the existing launcher. To make it the
tablet's Home screen, open Encore's gear → **Choose Home app**, then select
**Encore Incubator** in Android. Encore does not change the default itself.
Once selected, the Home button or gesture returns to Encore instead of the old
launcher. The dashboard is fullscreen and keeps the screen awake while visible;
swipe in from a system-bar edge to reveal Android's controls. Encore doesn't
replace the lock screen or provide an app drawer.

To switch back, open Encore's gear → **Choose Home app** and select the previous
launcher. If there is an active voice turn, tap **Stop** first. **Open Android
settings** also gives you a way out; look for **Apps → Default apps → Home app**
(wording varies by Android build). If the dedicated Home settings page is missing,
Encore opens general Android settings instead. If you can't use Encore's gear,
try revealing the system bars and entering Settings from Quick Settings.

- **Reboot and unlock:** Android should open the selected Home app after unlock.
  Encore doesn't launch itself at boot or before unlock. Normal microphone
  permission, saved mute, and Android microphone privacy still apply.
- **Updates:** Choosing Home changes neither Encore's update settings nor Android's
  installation approval. A same-package, same-signer update keeps the Home
  component eligible; Android owns the Home preference and decides when to show
  it again. After install, unlock if needed and press Home if Encore isn't already
  visible. Installing the separate debug package isn't an in-place update.
- **Screen pinning** is an optional Android feature, where available, that keeps
  one app on screen until someone uses the system unpin gesture. It neither
  selects Home nor provides persistent kiosk enforcement. Encore does not turn
  it on. Unpin before changing Home apps or installing an update.
- **Managed kiosk** (enforced lock task, a policy-set Home, restrictions on
  Settings or system controls) requires separate Android Enterprise provisioning
  with an appropriate device-owner policy controller. Selecting Encore as Home
  does not enroll the device, require root, or enforce those restrictions.

### Physical Home acceptance checks

On the Galaxy Tab A7 Lite / LineageOS 21 GSI (Android 14), keep the previous
launcher installed. Confirm normal launch without opting in. Select Encore in
Android and press Home from Android Settings and another app, using the current
navigation mode. Verify fullscreen, transient bars, and screen-awake behavior.
After reboot, verify the lock screen is still required and Encore does not use
the microphone before unlock. Record what opens immediately after first unlock,
then confirm that pressing Home opens Encore. With Encore still selected and
the mic muted, approve a newer same-signed APK. Check that Home still opens the
dashboard, its configuration and mute persist, and the old launcher can be
restored via Encore's settings. Repeat the recovery check with the dashboard offline.

Meta Portal is separate: first confirm sideloading, Android version/ABI,
WebView, and whether the exact firmware exposes a replaceable Home app and usable
system Settings. A regular app launch or successful sideload would not establish
Home support on Portal.

## Display and privacy behavior

- The dashboard stays usable outside the voice panel. Tap-to-talk and accepted
  local wakes open the **Second Pulse** transcript panel, themed by Android. A
  slow wake decision gets only a small, silent cue; a rejected or lost wake
  never opens the panel. Listening gives way to what Encore heard, the reply,
  and any server-requested follow-ups from that call.
- With the [native display extensions](https://github.com/farooqu/voice-incubator/blob/9a1bcf59060c315e2d3c2cccc3893daf493bb483/voice_incubator/protocol.py),
  the panel can show Heard before final recognition and update the reply as
  text arrives. Reply text can end at a prefix; it does not signal playback.
  The timer sheet uses supplied countdown and ringing names without changing
  Stop. Older servers still show their single reply and legacy timer labels.
- The panel grows to fit, up to half the screen, then scrolls under a fixed
  header. **×** or **Back** stops an active turn, including playback drain;
  after normal completion, the transcript stays for six seconds or until
  dismissed. Transcripts are not persisted. Close the panel and tap the display
  to reveal the microphone control.
- Incoming audio and remote mic sessions keep their existing edge indicator and
  Stop/privacy controls. Fresh mic RMS and measured playback animate that edge;
  queued audio and missing samples do not create motion. Reduced motion removes
  the traveling highlight. No enrollment or wake-training capability is added.
- Tap the display to reveal the idle controls, on the right in landscape and
  along the bottom in portrait. They hide five seconds after the last tap.
  The gear opens full-screen Settings, where music controls live.
  On a running Bluetooth-first install the primary action can explicitly start
  the microphone. Muting clears session pre-roll, cancels active voice, and
  releases the microphone. Android screenshots remain disabled for the running
  Activity, including locally revealed pairing keys.
- Only server-requested Incubator follow-ups exist. No Assist, runtime route
  switching, Assist-only follow-up toggle, enrollment stream, advertised wire
  AEC route is included.
- The paired ESPHome endpoint exposes **Announcements** for Home Assistant TTS
  and URL playback. Like Biscuit, it accepts PCM16, 16 kHz mono WAV and supports
  Stop through Home Assistant. It uses the shared voice output and respects its
  mute and Stop behavior. Android controls output volume; Sendspin music is separate.
  If the entity is missing on an already paired device after updating, reload
  the device's ESPHome integration in Home Assistant.
- The [Android Bluetooth proxy](../../docs/bluetooth-android.md) defaults On only
  on fresh installs with a detected BLE adapter. It is scan-only on API 26+.
  First-run setup explains consent and version-specific grants. Scans pause outside a
  visible Encore activity; GATT, remote caching and pairing are not implemented.
  Tab A7 Lite / LineageOS 21 GSI and Portal 2 still need physical qualification.
- Dashboard JavaScript has no native command bridge, file/content access,
  camera/mic grants or external-intent launch path. With phone sign-in, only the
  configured HTTPS origin's main frame can request the current HA access token
  or revoke it via HA's external-auth bridge. TLS errors are not bypassed. The
  QR code contains no credentials; the temporary local callback receives a
  single-use PKCE-bound code, and HA credentials are exchanged only over HTTPS.
  The tablet stores its refresh token encrypted with Android Keystore. Navigation
  stays within the selected Home Assistant origin; changing origin recreates
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
to verify the Incubator and Android composition.

Application updates are signed APK replacements, independent of Android OS/security
updates. Preserve signature/version constraints through a centrally reviewed
packaging policy; core SemVer and package revision alone are not an Android
versionCode algorithm. The central ledger reserves experimental Android revision 5
with core `0.1.1-dev.1`; this is not a built or published release. Automatic release
support remains false. Final package/update metadata must bind the backend variant and routine updates
must preserve it. The update foundation is not an activated
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
