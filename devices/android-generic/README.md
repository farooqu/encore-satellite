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

## Use the development app

1. Install the debug APK using Android's ordinary user-approved sideloading flow.
   Open it and choose **Start satellite**, granting microphone permission. It runs
   a microphone/media foreground service with a persistent notification. No boot
   receiver, root, device owner or unattended kiosk privileges are required.
2. On a trusted LAN, pair the device with Voice Incubator using the advertised
   `_voice-incubator._tcp` service, or its IP and TCP port 6055. This protocol is
   **unencrypted/unauthenticated**; do not expose it to untrusted networks.
   Incubator sends mixednet models, which execute locally through the compatible
   microWakeWord frontend. No wake model is bundled or downloaded independently.
   Readiness combines usable ordinary wake models with current native capture
   health; a stop-only model does not mean hands-free wake is ready. **Talk** is a
   manual fallback, not a replacement for local wake detection.
3. Add the device's separate ESPHome integration on TCP 6053 using the encryption
   key revealed in local **Settings**. Set its single **Dashboard URL** text entity
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
4. Sign into HA in the dashboard itself. Only the HA dashboard is a WebView;
   settings, privacy, voice, waveform and music controls are Compose. Initial
   login supports same-origin flows only (no external-IdP allowlist yet).
5. Optionally connect a **current Noise-capable Sendspin server** by host:port in
   Settings. Reveal the independent Sendspin pairing token locally and provision
   it on the server. The player uses encrypted, Keystore-protected identity/trust;
   it does not silently fall back to plaintext or admit unpaired playback.
   Older plaintext-only servers are not compatible. Music can be disconnected
   without changing voice, and microphone mute does not disable music. A voice
   turn reduces the music track gain to 15%, retaining player volume/mute and sync.

The foreground service owns audio and network sockets independently of dashboard
reloads and Activity rotation. Closing the notification's **Stop satellite** action
stops it; restarting never silently reverses persisted microphone privacy.
This initial package does not automatically resume after reboot or process death.
The music server address/connection is entered explicitly each time; identity and
pairing records persist. The screen stays awake while the Activity is visible.

## Display and privacy behavior

- One dashboard fills the display edge-to-edge, with no header, permanent toolbar,
  margins or dashboard card. Android system bars are hidden but can be revealed by
  a system swipe. Swipe inward from the leftmost 20 dp to open satellite controls:
  Talk/start, privacy, settings and independent music controls. Other dashboard
  swipes are not drawer gestures. No visible menu button is drawn. Screen readers
  can use either voice button's “Open satellite controls” accessibility action.
  Two circular 56 dp buttons float at the bottom center: microphone/Talk (or
  explicit satellite start before startup) and mic mute. After BLE-only startup,
  the primary button explicitly starts the microphone without changing saved mute
  intent. Talk remains unavailable until fresh capture and shared permission are
  ready; mute is disabled without a recorder unless it is already saved-muted,
  when explicit permission-aware unmute remains available. During voice, Talk becomes
  Stop in the same circular button. Mute has a crossed-mic symbol, a checked toggle
  state and a red background when muted; unmuting remains an explicit action.
  During the entire voice turn the dashboard is frosted,
  touch-shielded and hidden from accessibility interaction; stop and privacy remain
  usable, including host playback drain after HANGUP. Healthy idle has no trace.
  Voice takes over the dashboard edge-to-edge without a visible card. On Android
  12+ the background is softly blurred with a light neutral frost; older Android keeps the tint
  and input shield without blur. This is visual treatment, not security redaction.
  The waveform uses the compact 48 dp row on every display, anchored independently
  of transcript length. Text fills the space below only as needed, then scrolls
  under a true alpha fade, above the reserved bottom controls. No large
  empty panel is drawn for short exchanges. Heard text uses a subdued version of
  the input braid color (including reported speaker RGB); replies use response
  lilac, darkened for contrast on the light frost, with no added prefixes. Privacy
  and Stop remain directly reachable during voice; both are also available in the
  drawer. Notices stay visible without reserving dashboard space. The dashboard keeps
  its existing viewport size while hidden; entering voice does not reflow it.
- Native Canvas draws two independent Horizon braids: mint input from fresh
  microphone PCM and lilac response audio from accepted PCM at the AudioTrack
  playback head. They can move simultaneously; silence in either settles only
  that layer flat. Queued/missing audio never invents output energy. A valid
  current-turn Incubator speaker RGB replaces the input base color after it is
  reported, including during response playback; it never recolors output. The
  microphone can hear device echo: these layers do not establish user/device
  acoustic separation, authentication or sound direction. Audio focus, capture,
  routing and ducking keep their existing policies.
- Each braid is a level-reactive visualization, not a raw PCM oscilloscope.
  RMS drives three smooth ribbons, with display-frame attack/release easing
  (22/95 ms initial tuning), bounded quiet-level lift, broad taper and
  tangent-continuous curves. Silent input/output settles flat; missing samples
  clear immediately. Reduced motion and night mode freeze ribbon drift while
  retaining smaller bounded live-level response. Smoothing resets at
  turn/connection/lifecycle boundaries. Kiosk Satellite's
  frame-ticked level response informed this approach; no code, assets, strand
  tables, glow layers or idle animation were copied from its CC BY-NC-ND source.
- Processing/waiting uses three small neutral dots above the trace, only for
  actual processing or queued state, never merely because audio is silent.
  Amber alert rails remain distinct. No visible listening/processing/speaking
  captions or speaker metadata are displayed; accessibility retains state and
  server-attributed input-speaker descriptions. The drawer shows availability and
  privacy status; notices, actual transcripts and Stop/privacy controls remain
  visible when applicable. Announcements retain the shared active/owner facts,
  including during drain, even when capture is unavailable. A focus interruption
  still requires explicit Resume audio for an unchanged playing music stream.
- Stop/privacy/disconnect/unknown state removes the braids without a tail.
  Capture history cannot cross input turns/connections, and readings older than
  250 ms fail unavailable. These are initial visual timings, not on-device latency
  promises. Android's disabled-animations setting freezes decoration; backend
  night on/unknown additionally dims the trace and limits displacement.
  Decorative clocks stop below the visible lifecycle and resume from current facts.
- Actual upstream recognition/reply text is shown by default, with no synthetic
  transcript. Muting clears the shared session/pre-roll, cancels active voice and
  releases the microphone; idle connections can stay attached while muted.
  Transcript text is not persisted. Android screenshots
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
  camera/mic grants or external-intent launch path. TLS errors are not bypassed.
  Navigation stays within the origin derived from the current ESPHome URL;
  changing origin recreates the WebView, independently of voice and music.

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
