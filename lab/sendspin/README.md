# Sendspin playback and voice-ducking lab

Real Music Assistant queues audio through the official C++ Sendspin client into
Encore's clocked **virtual** mixer. Native Voice Incubator turns
drive local music ducking. This is Linux x86-64 integration tooling, not Echo or
Android music support, physical speaker playback, or acoustic synchronization.

The [Echo Dot 2 and Android integration checklist](../../docs/sendspin-targets.md)
records the target work still needed and the lab pieces that must not ship.

## Reproduce with isolated services

Run from the repository root. First prepare the existing HA/voice lab using
[the foundation instructions](../README.md) and [Incubator setup](../incubator/README.md),
including provisioning HA, restarting it after installing Incubator's integration,
and generating native speech fixtures. Do not use production configuration.

```sh
lab/sendspin/setup
lab/sendspin/server/setup
amp orb service start sendspin-ma --command "$PWD/lab/sendspin/server/run" --port 8097
python3 lab/sendspin/server/control.py bootstrap
amp orb service start incubator --command '.amp/in/orb-backup/voice-incubator/.venv/bin/python lab/incubator/run.py' --port 8100
amp orb service stop encore
amp orb service start sendspin-satellite --command 'ENCORE_SENDSPIN_LAB_CLIENT="$PWD/.amp/in/orb-backup/sendspin-receiver-build/encore-sendspin" go run -race ./cmd/encore-lab' --port 8099
lab/.venv/bin/python lab/sendspin/test.py
amp orb service stop sendspin-satellite  # Free ports 6053 and 6055 for the Go tests.
make check
```

For an already defined service use `amp orb service restart <name>`. Wait for HA
and Incubator startup before the test. Only one suite may own the voice backend
at a time. Do not run `amp orb services ensure` while `sendspin-satellite` owns
8099: the declared default `encore` service uses the same port. Do not run
`make lab-test` concurrently; both suites use the same native voice fixture.

No Sendspin endpoints exist without `ENCORE_SENDSPIN_LAB_CLIENT`. Even with this
host opt-in, the independent persisted switch defaults to **off**. Private
identity, switch and receiver log live under `ENCORE_LAB_STATE` (default
`~/.local/state/encore-lab`). Never publish its keys. MA has separate private
state and credentials; see [server setup/security](server/README.md).

All added HTTP/socket listeners bind to loopback. The C++ receiver dials the fixed
lab MA server; there is no client inbound listener. No portal is needed. These
are not OS network namespaces; MA core still uses normal mDNS interfaces. The
lab explicitly opts into **encrypted, unauthenticated guest access**, which this
MA pin autoapproves for these roles. Plaintext is disabled. This is not a product
pairing policy or suitable for an untrusted network.

## What runs, and what is simulated

- MA `07780231361dd45668f937fc3fe950b38527fde0`, aiosendspin 10.3.0 and FFmpeg
  8.1.3 run the real queue, HTTP fixture resolution, decoding and Sendspin server.
  MA runs in safe mode without optional LAN/ML providers, not a full distribution.
- Official `Sendspin/sendspin-cpp` at
  `a4a9b65fb356657e3a39a2ab70b4aae957bf53e0` owns Noise, protocol v1,
  player/controller/metadata roles, scheduling and decoding. The receiver
  negotiates only PCM16/48 kHz/stereo and uses its default bounded audio buffer.
- The Go virtual sink consumes decoded music alongside the existing 16 kHz mono
  voice route. It records mixed PCM but sends nothing to an audio device. Voice
  resampling is deliberately sample repetition ×3 and stereo duplication, with
  saturating addition; it is not a production-quality resampler/mixer.
- Incubator uses actual mDNS discovery,
  Kaldi wake verification, CTC/TDT recognition, HA intents and Piper with synthetic
  spoken capture. Wake detections are injected at the host boundary, not detected
  acoustically from music.

## Measurements and controls

`test.py` finds the real MA-assigned player by name, plays a locally generated
120-second 1 kHz triangle through `player_queues/play_media`, and measures
post-gain music RMS divided by input RMS in actual rendered blocks. It rejects
silent input and checks concurrent voice samples during response playback.

- Music gain is current user gain × **0.2 while voice is active** (about −14 dB),
  with a bounded 20 ms linear ramp. Privacy, user mute, Stop and feature-off
  override that gain immediately. Voice samples retain their separate gain.
- Listening, held response, concurrent response, virtual speaker
  drain and Incubator follow-up are exercised. A user-volume change from 100%
  to 35% produces gain 0.07 during voice and restores 0.35, not the old 1.0.
- MA mute survives volume changes. Stop/privacy latch local music silent;
  privacy release does not autoplay it. Explicit local play releases the latch.
- Disabling invalidates PCM generations before killing the receiver. Old PCM is
  rejected both while off and after re-enabling. MA may send a **fresh** stream
  when the receiver rejoins a still-playing queue; that is not cached replay.
- Native turns do not pause the MA queue. A real timer ducks music. Disabling
  Sendspin during listening still lets Incubator execute the HA intent and drain
  the complete Piper response.
- After timer Stop and upstream `timer_stopped`, the next real wake, HA intent
  and Piper response complete on the same native connection. Explicit music
  play releases Stop's silence latch; that next voice turn ducks/restores music.

The test writes `measurements.json` and
`incubator-ducking.wav` under `.amp/in/orb-backup/sendspin-proof` by default;
`--output` selects another directory. Captures are limited to 60 seconds per
recording. Final cleanup stops the MA queue and leaves Sendspin disabled. Voice,
HA and MA services remain running; no production service/account is configured.

Loopback controls: `GET /music/state`, `/music/blocks`, `/music.wav`;
`POST /music/record`, `/music/play`, `/music/stop`, and `/music/enable` with
`{"enabled":true|false}`. The stream/volume/PCM endpoints are private lab IPC,
not product APIs or an ESPHome media entity. The existing `/command` voice
controls remain separate. The persistent switch is a lab control, not yet an
encrypted common-control entity or Android setting.

## Known limitations and failed validation cases

- The synchronous local HTTP sink plus coarse virtual clock has partial-frame
  gaps and SDK lost-sync warnings under load. RMS checks establish attenuation
  of rendered music, **not uninterrupted playback quality**, real-time latency,
  drift performance, or synchronized multiroom. Replace/validate this sink with
  target output clocks and bounded asynchronous output before product use.
- The pinned server sends group IDs outside the C++ client's current
  43-character base64url expectation. The client currently normalizes and warns;
  its warning says a future release will close the connection. Pin upgrades
  require compatibility tests.
- An initial custom 192 kB SDK buffer filled on stream restart; restoring the
  upstream default 1 MB buffer fixed that observed startup failure. Tests now
  wait for non-silent consumption, not just socket registration or priming zeros.
- An initial re-enable check wrongly treated all new frames as stale. The real
  MA server can legitimately send fresh timed audio to a rejoining player. The
  test instead checks that old-generation PCM remains rejected.
- The initial run exposed a voice bug: after Stop interrupted paced native timer
  PCM, the next wake returned HTTP 409 because upstream sent `timer_stopped`
  without AUDIO_OUT_END. Core now releases a timer-only cancellation fence on
  that acknowledgement, without releasing an overlapping cancelled call or
  announcement. Regression checks keep late PCM fenced, preserve repeated Stop,
  and reject an old timer acknowledgement as a cancelled voice call's completion.
  Both this lab and the standalone native suite test a real next voice turn on
  the same connection.

No target builds, physical Echo/Android audio, wake reliability over music,
AEC/render-reference correctness, music-quality resampling, two-player timing,
metadata presentation, automatic server discovery, MA-managed announcements,
device-originated group controls or authenticated product pairing are established
by this lab. Existing native announcements remain covered only by the separate
voice regression suite, not by a music-ducking announcement test.

See [dependency provenance](NOTICES.md). Nothing in this lab selects Encore's
distribution license or authorizes bundling downloaded binaries/models.
