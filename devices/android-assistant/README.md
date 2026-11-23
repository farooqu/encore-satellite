# Encore Satellite Assistant

Use your Android phone as a personal input for Voice Incubator. To use the
overlay, choose Encore Satellite Assistant as Android's default digital
assistant. You can keep the Home Assistant Companion app installed. See the
[device comparison](../../docs/voice-experience.md) for the tablet and Echo Dot.

## Install the signed APK

From the [Assistant prereleases](https://github.com/farooqu/encore-satellite/releases?q=tag%3Aandroid-assistant-),
download `encore-satellite-assistant.apk` and `SHA256SUMS` from the same release.
The APK supports ARM64 and ARMv7 on Android 8 (API 26) or newer; the optional
system assistant overlay needs Android 12 or newer. Verify the APK before
opening it on your phone and accepting Android's install prompt:

```sh
grep -E '[[:space:]]\*?encore-satellite-assistant\.apk$' SHA256SUMS | shasum -a 256 -c
```

With [Android platform-tools](https://developer.android.com/tools/releases/platform-tools)
and approved USB debugging, you can also install and check it from a computer:

```sh
adb devices -l
# Set PHONE_SERIAL to the intended phone listed as "device".
adb -s "$PHONE_SERIAL" install encore-satellite-assistant.apk
adb -s "$PHONE_SERIAL" shell pm path com.encore.personal
```

Use `install -r` to keep app data when you install a newer APK with the same
signer. The signed, tablet, and debug apps have different app IDs. Open the
phone app and [connect it to Incubator](#connect-to-voice-incubator).

## Build locally instead

Build with Go 1.27, JDK 21, Gradle 9.4.1, Android SDK platform/build-tools 36,
NDK 28.0.13004108, Meson and Ninja. Set `ANDROID_HOME` and put Gradle on `PATH`
or set `GRADLE` to its executable.

```sh
devices/android-assistant/build-android assembleDebug testDebugUnitTest lintDebug
```

Install `devices/android-assistant/build/outputs/apk/debug/EncoreSatelliteAssistant-debug.apk`
using Android's ordinary sideloading confirmation. The debug app ID is
`com.encore.personal.dev`, with ARM64 and ARMv7 native libraries, API 26 minimum.

## Connect to Voice Incubator

1. On trusted home Wi-Fi, enter the private IPv4 address the Incubator app
   connects **from** (normally the HA host; reserve its address in DHCP).
   Tap **Arm mic** and grant microphone and notification access. The phone
   only listens on its current private Wi-Fi address, TCP 6055, while armed.
2. Connect Incubator to the discovered `encore-personal-…` satellite or use
   the phone endpoint shown in the app. The prototype only accepts connections
   from the configured server IP. If using a container or NAT, verify the
   actual source address. Native Incubator is unauthenticated and unencrypted;
   the address filter is not authentication. Do not port-forward it or arm
   on guest, public or untrusted Wi-Fi.
3. Try **Talk**, then select an ordinary wake word for the phone on Incubator's
   Wake words page. Bring your own compatible Micro Wake Word `.tflite` and
   `.json` manifest by uploading them **to Incubator**. It packs and pushes
   the model; the phone runs the MixedNet model with the Micro Wake Word
   frontend and only sends session audio (including bounded wake pre-roll)
   once invoked. No model is bundled and a stop-only model will not arm wake.
4. On Android 12 or newer, tap **Set up assistant overlay** and choose **Encore Satellite Assistant** as the
   default digital assistant. This replaces the existing assistant shortcut,
   not the Companion app. Use the system assistant gesture or hold the power
   button if your phone maps that action to the assistant. Neither the gesture
   nor a wake arms a paused microphone. The app doesn't request underlying app
   content, screenshots or the general draw-over-other-apps permission. Android
   8–11 retain in-app Talk and wake detection without the assistant overlay.
   The overlay shows what Incubator heard and the reply text it sends, including
   follow-ups in the same call. The transcript scrolls once the panel reaches
   half the screen. After a normal finish with text, it stays for six seconds;
   **×** or **Back** closes it sooner. The overlay does not keep transcript history
   after dismissal.
5. Test with the display on, then locked and screen off. The explicit arm
   keeps a microphone foreground service and partial wake lock; measure battery,
   heat, mic contention and Bluetooth audio routing on the actual phone. **Stop**
   cancels a turn. **Pause mic** in the app or notification stops listening and
   closes the satellite. Changing the Wi-Fi network/address or losing sustained
   microphone access pauses it. It does not re-arm at boot or after process death.

On an unlocked, interactive screen, a tentative wake requests a small,
non-interactive cue above navigation immediately. The phone keeps capturing
through the decision. Only Incubator's `listening` event opens the full wake
panel and requests audio focus. A room win or a rejection removes the cue
without an alert. If Android delivers the window request after the verdict,
it opens directly as the full panel on acceptance or stays hidden on rejection.
The Listening panel stays compact and grows to fit the
transcript or reply, up to half the screen. Longer text scrolls inside the
panel. The close icon stops the current turn; Pause mic stays in setup and the
notification. Automatic wake UI does not turn on the display or show over
the lock screen. Actual window behavior and power-button mapping vary by phone
and need device testing.

## Which satellite options belong on a phone?

| Option | Encore Satellite Assistant prototype |
| --- | --- |
| Assigned/custom wake words, Talk, spoken Stop, replies, follow-ups | Reuses the native Incubator session/model path and Android audio/AEC3. On-device reliability and echo behavior are unqualified. |
| Listening status and mic privacy | Keep explicit arming and Pause; distinguish waiting for server/model from wake ready. Transcript is not persisted. |
| Fixed Incubator Person | Important before treating it as a personal assistant. The install ID is stable, but stock Incubator has no fixed satellite-to-Person mapping. A per-person wake word is only an attribution fallback, not a binding for Talk or authorization. |
| Timers, alarms and announcements | Worth testing separately. The native transport can receive them if Incubator routes them here while armed. There is no phone alarm/scheduling or ESPHome announcement feature. Prefer personal timers over shared-room broadcasts; avoid assigning the phone to a shared room by default. |
| Android assistant role | Optional system overlay and manual gesture. Does not take over the lock screen or replace the system speech recognizer. |
| Headset routing, shortcuts/tiles | Evaluate after screen-off voice and battery are tested. |
| Off-store updates | Required before regular use. The debug APK has no updater and is not the initial signed install. |
| Sendspin music player, BLE proxy, sensors, dashboard, Home role, ESPHome controls | Not packaged. |
| Intercom, Live Monitor, voice enrollment, wake training capture | Not advertised. The custom-model upload path does not require these. |

## GitHub Dev releases

The [Android Assistant · Checks and Dev release workflow](../../.github/workflows/android-assistant.yml)
builds and checks the APK on pull requests without accessing signing secrets.
After **CI · Repository checks** passes on protected `main`, the workflow reads
the shared Dev plan. If the plan selects Assistant, it builds that exact
verified commit and can sign and publish it as an immutable GitHub prerelease.
Assistant-specific changes and shared inputs used by Assistant can select it.
The tag is `android-assistant-r<version code>`; the Android version code comes
from `main` history, so a rerun keeps the same identity. The prerelease includes
`encore-satellite-assistant.apk`, SHA-256 checksums, and matching source. It
does not become the repo's latest release.

Automatic Dev reuses the [regular Android satellite's signing key and pinned
certificate](../../release/android-updates.md#build-and-signing-pipeline) in
`android-dev-signing` and the shared `release-dev-publication` gate, which
requires `RELEASE_SETUP_APPROVED=true` and immutable releases enabled. No new
key or environment is needed. A manual run with **publish** off still retains
an unsigned candidate. Manual publication continues to require explicit
repository policy approval, `android-signing`, and `release-publication`.
The release app ID is `com.encore.personal`; it installs separately from the
tablet APK and from the Assistant debug build.

## Off-store updates

Use the same [signed-feed and Android installer foundation](../../release/android-updates.md),
with the **Assistant** application ID, long-lived APK signer and separately
identified HTTPS feed. Reuse the verifier and installer; do not reuse the tablet's
feed, ESPHome update controls, or explicit Home Assistant install request. The
prototype debug build uses a different app ID and signer, so install the signed
app separately; it cannot update the debug app in place or inherit its satellite
identity. GitHub prereleases are for manual sideloading; no update feed or
in-app updater is activated by this workflow.

For the signed app, make automatic updates a local opt-in. Schedule checks and
stage verified, newer APKs automatically; verify the signed metadata, digest,
package identity and signer before install. Apply while the mic is paused so
an update doesn't interrupt a voice session or silently re-arm the mic. On
Android 12+ request unattended self-update where Android permits it. If source
permission, device policy, or the installer requires user action, show a local
notification for approval. Test a clean install followed by an actual signed
in-place update on the phone before calling the route supported.
