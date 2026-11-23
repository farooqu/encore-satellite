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

1. On trusted home Wi-Fi, tap **Discover on home Wi-Fi**, confirm, and keep the
   screen open. The phone advertises itself for up to 30 seconds without opening
   the microphone. Incubator discovers the `encore-personal-…` satellite and
   connects to it. If needed, point Incubator at the phone endpoint shown during
   setup. Confirm **Save server** if the detected address is your Incubator host.
   You can also enter the IPv4 address Incubator connects **from** manually.
   The app remembers that address; reserve it in DHCP if applicable.
2. Tap **Talk** and grant microphone and notification access. Wi-Fi is not
   required after setup. For a request or while wake-word listening is enabled,
   the phone listens on TCP 6055 at the source address Android routes toward the
   saved server and only accepts that server's IPv4 address. Incubator must be
   able to reach the phone, not just the reverse. A cold request may wait for
   Incubator's next connection attempt; you can cancel while it waits.
   Over WireGuard, route the phone's tunnel IP from the Incubator host and point
   Incubator at the phone's tunnel IP and TCP 6055 if discovery does not cross
   the tunnel. The incoming source must still match the saved address; check
   it if your VPN or container uses NAT. Native Incubator is unauthenticated
   and unencrypted; the IP filter is not identity verification. Use a trusted
   LAN or VPN and never port-forward the listener. On an untrusted network
   without a trusted route to Incubator, do not start voice requests or leave
   wake-word listening enabled: another device could use the same private IP.
3. **Listen for wake word** is optional and off by default. To use it, turn it
   on and select a wake word for the phone on Incubator's Wake words page.
   Bring your own compatible Micro Wake Word `.tflite` and
   `.json` manifest by uploading them **to Incubator**. It packs and pushes
   the model; the phone runs the MixedNet model with the Micro Wake Word
   frontend and only sends session audio (including bounded wake pre-roll)
   once invoked. Talk doesn't need a wake model. No model is bundled, and a
   Stop-only model can't start a request.
4. On Android 12 or newer, tap **Set as default assistant** and choose **Encore Satellite Assistant** as the
   default digital assistant. This replaces the existing assistant shortcut,
   not the Companion app. Use the system assistant gesture or hold the power
   button if your phone maps that action to the assistant. This starts a manual
   request even with wake-word listening off. The app doesn't request underlying app
   content, screenshots or the general draw-over-other-apps permission. Android
   8–11 retain in-app Talk and wake detection without the assistant overlay.
   The overlay shows what Incubator heard and the reply text it sends, including
   follow-ups in the same call. The transcript scrolls once the panel reaches
   half the screen. After a normal finish with text, it stays for six seconds;
   **×** or **Back** closes it sooner. The overlay does not keep transcript history
   after dismissal.
5. **Stop** cancels a request. Switching off **Listen for wake word** prevents
   new wakes without cancelling an existing request or follow-up. When wake-word
   listening is off, Encore opens the mic only after a manual request connects,
   then releases it when the request ends. **Stop voice** in the Android
   notification ends capture immediately. A network change or server outage
   does not switch off wake-word listening; the service retries on network
   changes and periodically. A disconnected request does not resume. Losing
   microphone access or disabling the notification stops capture. Background
   listening does not start automatically at boot or after process death.

On an unlocked, interactive screen, a tentative wake requests a small,
non-interactive cue above navigation immediately. The phone keeps capturing
through the decision. Only Incubator's `listening` event opens the full wake
panel and requests audio focus. A room win or a rejection removes the cue
without an alert. If Android delivers the window request after the verdict,
it opens directly as the full panel on acceptance or stays hidden on rejection.
The Listening panel stays compact and grows to fit the
transcript or reply, up to half the screen. Longer text scrolls inside the
panel. The close icon stops the current turn or cancels a pending request.
Automatic wake UI does not turn on the display or show over
the lock screen. Actual window behavior and power-button mapping vary by phone
and need device testing.

### Native display events

Personal Mic keeps native protocol 2 and advertises `early_transcript`,
`reply_chunks`, and `timer_names` in HELLO. The [fork’s protocol at `9a1bcf5`](https://github.com/farooqu/voice-incubator/blob/9a1bcf59060c315e2d3c2cccc3893daf493bb483/voice_incubator/protocol.py)
allows an early Heard line and incremental, best-effort reply text. The final
recognition result replaces the early line. Reply text appends to the one
`responding` event and may remain a prefix at hangup; neither display event
changes how audio is played or acknowledged. The main screen shows countdowns
and ringing names while the satellite is connected. Older servers still show
their single reply, unnamed countdowns and legacy ringing name/count.

Voice PE at `8ba314e` uses `responding` to change its LED effect and arm spoken
Stop, and uses timer events for countdown LEDs and the ringing effect. Its ring
doesn't display text. Personal Mic uses the phone screen for these optional
captions and names without changing Stop, playback, or the manual-only
connection lifetime below. The native TCP tests cover captions around PCM and
drain, turn fences, and legacy timers; the Personal Mic UI tests cover the
rendered labels and captions.

### Voice PE comparison and route check

[Voice PE firmware `8ba314e`](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/PROTOCOL.md)
advertises a TCP listener. It does not start a connection to the app or toggle
the hardware mute switch when the connection drops. The
[Incubator app at `38b573b`](https://github.com/OHF-Voice/voice-incubator/blob/38b573b13a92f0654b24611a3a6355c8e8c4d5c3/voice_incubator/server.py#L1752-L1830)
discovers and dials that listener, then retries with backoff. Encore keeps that
direction and chooses a new listen address when the OS route changes rather
than opening a wildcard listener. Voice PE's Action button sends a `manual`
call independently of ordinary wake recognition. Encore does the same for Talk
and the Android shortcut, but makes background capture opt-in and closes the
manual-only connection at the end of a request to avoid keeping an idle phone's
microphone and foreground service active. This can make cold Talk slower than
the always-connected Voice PE. Spoken Stop remains available during eligible
replies if Incubator has supplied its model. The phone does not advertise
Live Monitor, monitor AEC, or intercom, and ignores intercom requests: using the
phone as a personal assistant does not grant other rooms access to its mic.
Discovery learns the app's source
address from its inbound connection because stock Incubator does not advertise
itself as a server. These are source findings, not a phone test.

To verify on a phone, save the server once. With wake-word listening off, try
Talk and the Android shortcut on Wi-Fi and on cellular with WireGuard routed
both ways. Check that the microphone indicator appears for the request and
clears at the end, including after follow-ups. Cancel while connecting and
confirm no request starts later. Turn on wake-word listening, disconnect the VPN
while off Wi-Fi, and confirm the switch stays on. Return to Wi-Fi and confirm
wake and Talk recover without touching the switch. Repeat with the screen off
and check microphone contention, audio routing, battery, and heat.

## Which satellite options belong on a phone?

| Option | Encore Satellite Assistant prototype |
| --- | --- |
| Assigned/custom wake words, Talk, spoken Stop, replies, follow-ups | Reuses the native Incubator session/model path and Android audio/AEC3. On-device reliability and echo behavior are unqualified. |
| Listening status and mic privacy | On-demand capture for manual requests; separate opt-in for background wake-word listening. Transcript is not persisted. |
| Fixed Incubator Person | Important before treating it as a personal assistant. The install ID is stable, but stock Incubator has no fixed satellite-to-Person mapping. A per-person wake word is only an attribution fallback, not a binding for Talk or authorization. |
| Timers, alarms and announcements | Worth testing separately. The native transport can receive them if Incubator routes them here while the service is connected. There is no phone alarm/scheduling or ESPHome announcement feature. Prefer personal timers over shared-room broadcasts; avoid assigning the phone to a shared room by default. |
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
package identity and signer before install. Apply while the mic is off so
an update doesn't interrupt a voice session or silently turn on the mic. On
Android 12+ request unattended self-update where Android permits it. If source
permission, device policy, or the installer requires user action, show a local
notification for approval. Test a clean install followed by an actual signed
in-place update on the phone before calling the route supported.
