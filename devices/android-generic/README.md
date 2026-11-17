# Generic Android: Incubator development APK

This target now contains an **Incubator-only Android app**, with a native Kotlin /
Jetpack Compose host and an in-process Go engine. It is not a released or
hardware-qualified package. Samsung Galaxy Tab A7 Lite is the first device to
try; Meta Portal installation remains unverified, not ordinary tablet support.
`profile.json` and the central release registry remain planning/revision-zero
metadata, not runtime settings or a release declaration.

## Build locally

Requirements: Go 1.27, JDK 21, Gradle 9.4.1, Android SDK platform/build-tools 36,
and NDK 28.0.13004108. Set `ANDROID_HOME` and put Gradle on `PATH` (or set `GRADLE`
to its executable). From the repository root:

```sh
devices/android-generic/build-android
```

The script binds the matching Go engine into an AAR for `arm64-v8a` and
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
   The app shows connected state and installed model count. **Talk** is a manual
   fallback, not a replacement for local wake detection.
3. Add the device's separate ESPHome integration on TCP 6053 using the encryption
   key revealed in local **Settings**. Set its single **Dashboard URL** text entity
   to a full URL, for example `https://home.example/lovelace/home`. This supplies
   both the HA origin and dashboard path; no separate tablet origin setup exists.
   Prefer HTTPS; explicitly choosing `http://` permits trusted-LAN HTTP, including
   unencrypted HA sign-in traffic. The paired ESPHome controller can change the
   full URL, including its origin. Invalid URLs retain the previous configuration.
   **Reload dashboard** is a separate button entity. No dashboard list or backend
   selector exists. Previously configured full dashboard URLs remain valid.
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

- One dashboard fills the main display. During the entire voice turn it is dimmed,
  touch-shielded and hidden from accessibility interaction; stop and privacy remain
  usable. The horizontal Horizon braid draws real microphone PCM, not a timed
  speaking animation. It is static when microphone visualization is inactive.
- Actual upstream recognition/reply text is shown by default, with no synthetic
  transcript. Muting clears the shared session/pre-roll, cancels active voice and
  releases the microphone; idle connections can stay attached while muted.
  Transcript text is not persisted. Android screenshots
  are disabled for the running Activity, including locally revealed pairing keys.
- Only server-requested Incubator follow-ups exist. No Assist, runtime route
  switching, Assist-only follow-up toggle, enrollment/reference stream, claimed
  AEC or ESPHome `media_player` is included.
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
exercise touch shielding, stop/privacy access, transcripts, waveform state,
capability-gated music controls and same-origin navigation. Native-rendered test
captures are written to `app/build/reports/screenshots/`; their audio, transcript
and music states are explicitly test fixtures, not on-device evidence.

The one-URL development build passed APK assembly, all three native UI tests and
lint (zero errors, 15 warnings, including deliberately unsupported x86/ChromeOS
and newer-version suggestions). The APK's v2 development signature and ARM-only
native library contents were independently checked. These checks do not establish
installation, microphone quality, real Sendspin playback or physical audio timing.
The full-URL configuration, origin changes, invalid-input rejection, persistence
and voice-independent controls were also exercised through a real encrypted
`aioesphomeapi` client. The rebuilt APK retains the previous development signing
certificate and uses a higher versionCode; on-device updating remains untested.

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
