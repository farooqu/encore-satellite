# Sendspin music on Encore

Encore's Android and Biscuit ARM64 packages include an opt-in Sendspin player for
Music Assistant. Voice still uses Incubator, and Bluetooth proxying is unchanged.
Use Music Assistant and its Home Assistant integration for queues, providers and
groups. Encore does not expose a duplicate ESPHome music player or implement
"play music here" room targeting.

| Target | Setup | Output |
| --- | --- | --- |
| [Android](../devices/android-generic/README.md#use-the-development-app) | Enable music in Satellite settings and pair with a locally revealed token. | Pinned SendspinDroid player and separate synchronized AudioTrack. |
| [Biscuit](../devices/biscuit/README.md#sendspin-music) | Enable the encrypted ESPHome Sendspin music switch and pair with the private USB-console token. | Static AArch64 Sendspin companion, versioned Unix socket, and shared ALSA mixer with separate voice and music queues. |

Both require a server that supports the encrypted Sendspin protocol and pairing.
Once enabled, each player automatically browses for a single compatible server
via local mDNS. If there are zero or multiple compatible advertisements, it
waits and retries rather than guessing. Android can override discovery in its
local Satellite settings by specifying a server address. Biscuit is discovery-only,
and ESPHome does not expose a Sendspin address setting.
Discovery identifies endpoints, not trusted servers. Neither player enables
plaintext or guest playback. Turning music off does not remove trust; use the
target's explicit revoke operation to remove trust and rotate the pairing token.

## Runtime behavior

- Android uses the clock page for Now Playing, with the time, large album art
  when supplied, and transport controls. Swiping from full Now Playing to a
  dashboard shows the floating player. Encore returns to the clock after 30
  seconds without touch only while music is playing with audio focus.
  Minimize keeps the floating player over every page and turns off idle return
  until expanded. Voice and timer panels take priority. Stop restores the
  plain clock and removes the floating player.
- On Biscuit, Action pauses authenticated Sendspin playback and can resume a
  pause it initiated in the same group. Active voice and ringing timers take
  priority; without active playback or a pause to resume, Action still starts
  voice when idle. These device interactions add no ESPHome entities.
- Music remains timed while voice attenuates it to 20% of the current music gain.
  Voice activity includes listening, processing, response drain, follow-up,
  announcements and ringing timers. Countdown alone does not duck. Local voice
  activity never pauses a Music Assistant group.
- Music Stop always silences the device locally and also requests a group Stop
  if the server supports it. Android's voice Stop affects only the current
  voice turn; Biscuit's Action also silences local music when canceling voice
  or a timer. Voice completion and privacy release do not resume locally
  stopped music.
  Voice output drain never waits for the music queue. Biscuit's ALSA driver
  cannot atomically stop and snapshot DMA; if a voice cancellation drops shared
  DMA, music discards uncertain frames and renegotiates instead of replaying them.
- The device players do not package the [virtual lab](../lab/sendspin/README.md)'s
  HTTP PCM sink or loopback controls. That lab tests real Music Assistant queue
  behavior and voice policy with virtual audio, not speaker output on a device.

## Hardware qualification still required

Cross-builds, protocol checks, and rendered Android tests do not qualify a
physical speaker route. Qualify each target independently on its exact build:

- Test real format negotiation, latency, CPU/RAM, underruns, speaker quality,
  output route loss, network recovery, and music/voice/BLE coexistence.
- Exercise local Stop, privacy release, changes to volume while ducked, disable
  during playback, and a timer Stop followed by another Incubator turn. Verify
  trust and settings survive restart and a signed update.
- On Android, test foreground/background restrictions, headphone and Bluetooth
  transitions, microphone and speaker concurrency, and AEC over music. Do not
  assume an available Android audio effect proves cancellation quality.
- On Biscuit, measure ALSA consumed-frame timing and shared-DMA recovery on the
  Dot. Negotiating stereo PCM does not make its single speaker stereo.
- Measure two real devices together for acoustic alignment. Group scheduling,
  physical synchronization and Music Assistant-managed announcements are separate
  checks. Wake-over-music is not qualified on either target.
