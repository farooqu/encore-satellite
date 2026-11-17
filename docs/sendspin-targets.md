# Sendspin music integration TODOs

Encore's real Music Assistant/Sendspin lab validates the music/voice policy with
virtual audio. Echo Dot 2 now has an experimental package integration; Android
still needs its target integration. Both need physical qualification. This
checklist is a handoff, not a device-support claim.

## What is already available

- [Reproducible lab](../lab/sendspin/README.md): pinned real MA queue/streaming,
  official C++ receiver, Assist and native Incubator, with synthetic capture and
  a virtual mixer. No Music Assistant server belongs on the satellite itself.
- `core/session.Engine.SetActivityHandler(func(bool))`: reports aggregate local
  voice activity from accepted wake/manual invocation through listening,
  processing, response drain, follow-up, announcements and ringing timers. The
  callback runs under the engine lock; it must be fast and must not reenter the
  engine. A host should apply it to music gain, not microphone or voice gain.
- Timer-only Stop recovery accepts upstream `timer_stopped` when cancellation
  omits AUDIO_OUT_END, without releasing unrelated cancelled voice/announcement
  output. The next native voice turn works without reconnecting.
- Executed on Linux x86-64, November 16, 2026: 31 rendered music-gain checks over
  385,854 music frames; full Assist/native suites; boundary, vet and race checks.
  The regression covers repeated Stop, late PCM and overlapping cancellations.

The lab uses `internal/lab/{music,sendspin,virtual}.go` and
`lab/sendspin/receiver/`. These are reference instrumentation, **not production
platform adapters**. Its synchronous per-block HTTP output exhibits gaps and SDK
lost-sync warnings; its sample-repetition voice resampler is not music-quality.
Do not package that IPC/sink, the loopback control API, the test MA/Python/FFmpeg
server or the lab's encrypted unauthenticated guest policy as the device player.
The pinned server/client also have a documented group-ID compatibility warning.
See the lab README and [dependency notices](../lab/sendspin/NOTICES.md).

## Shared behavior to preserve on both targets

- [ ] Provide a persistent, independently switchable Sendspin feature, initially
  off, through the target's normal configuration/common controls. The installed
  voice backend and Bluetooth proxying must not control its lifetime. Off means no
  active music discovery, connection, decoder or queued output; preserve private
  identity/trust for deliberate re-enabling and reject stale callbacks/PCM.
- [ ] Implement discovery/manual server configuration, stable per-device identity,
  authenticated pairing/revocation and private trust storage. No silent plaintext
  or guest fallback. Keep source/provider accounts and queues in MA; use HA's MA
  integration for media controls, not a duplicate ESPHome music entity.
- [ ] Compose current user music gain with temporary voice attenuation. The lab
  uses factor 0.2 (about -14 dB) and a 20 ms ramp; validate these on the target.
  Keep music ducked throughout listening, processing and foreground playback;
  follow-up and overlapping owners cannot restore it early. Countdown alone does
  not duck music; ringing/speaking timers do. Timer display is separate UI work.
- [ ] Keep music/voice queues and cancellation separate. Voice Flush/Drain must
  not flush music or claim it drained. Continue the timed music stream while
  ducked; do not pause an MA group as a side effect of a local voice interaction.
  Respect current user mute/volume, privacy, Stop and route-loss gates. Stop must
  silence immediately and voice completion must not clear the music Stop latch;
  privacy release must not autoplay music. Require explicit Play to release it.
- [ ] Validate the actual music output format separately from mono PCM16/16 kHz
  voice. The lab's PCM16/48 kHz/stereo negotiation is not a physical stereo or
  format-support claim for either device. Report real consumed-frame/presentation
  timing, bound buffers, fence replaced streams and handle output/network failure.
- [ ] Verify voice and timer responses with the package's sole backend, native
  Incubator by default; repeat qualification for any later package for the Home
  Assistant Assist pipeline, not runtime switching. Cover feature-off during
  voice/playback, user-volume changes
  while ducked, late audio, reconnect and restart. MA absence must not disable voice.
  Test MA-managed announcements separately; Sendspin does not supply Encore's
  voice/timer lifecycle or implement "play music here" intents/room targeting.

## Echo Dot 2 package integration

The [Echo packaging thread](https://ampcode.com/threads/T-01a1e5ab-b7b7-72ae-b3ec-ff58e6f691a2)
owns `devices/echo-dot-2/`, `platforms/echo/` and its package/base. At handoff it
reports a locally built, unflashed Alpine 3.22 ARMv7 package with ALSA, controls
and Action-activated voice. Its experimental integration is documented in the
[Dot package](../devices/echo-dot-2/README.md#sendspin-music). The production adapter
is `platforms/linuxsendspin`, not the lab receiver. Physical qualification remains
outstanding; these checked items describe implementation, not Dot measurements.

- [x] Cross-build official `Sendspin/sendspin-cpp` at the lab pin (or a deliberately
  revalidated upgrade) for the package's ARMv7 musl userspace. Establish a C++20
  userspace toolchain and compatible native dependencies. The kernel's AOSP
  arm-eabi 4.8 compiler is not that toolchain, and the current CGO-disabled Go
  runtime alone cannot supply the C++ player. Choose a small native/companion
  boundary against the actual package; the lab child process is not a fixed API.
- [x] Add bounded asynchronous output to the real ALSA speaker route, with proper
  resampling/channel conversion and separate music/voice gain. Wire SDK
  `on_audio_write` and `notify_audio_played` to real consumption and monotonic
  presentation timing, not HTTP return time or samples merely accepted by ALSA.
  The Dot driver cannot atomically stop/snapshot shared DMA: cancellation that
  drops it invalidates affected audio and renegotiates music's epoch rather than
  replaying uncertain frames. Voice Drain stays source-scoped. Gain changes keep
  the clock and apply within the bounded 20 ms DMA window; underruns recover
  without killing the host. Seamless Flush preservation above remains a target,
  not a capability claimed for this driver.
- [x] Wire `SetActivityHandler` and physical privacy/Stop to the output owner;
  connect persistent Sendspin settings to common controls without restarting
  voice or BLE. Keep Dot wiring, speaker tuning and button mappings device-local.
- [x] Package only the chosen client/runtime dependencies and audited notices.
  Record ABI/libc/compiler, source pins, runtime footprint and any added base
  libraries. Account for the package updater's Linux-base compatibility rules
  if music introduces base changes; do not assume an application-slot update
  safely replaces the shared kernel/base. No public release is authorized here.
  The companion is statically linked ARMv7 musl/libc++; no base library is added.
  Pairing/revocation uses a private physical-console token and manual server URL;
  automatic discovery and a friendlier pairing UI are not implemented. Retain the
  build's complete source/notices/provenance kit before any binary distribution.
- [ ] Measure real playback quality, CPU/RAM, underruns, physical drain/Stop/mute,
  timer Stop followed by another voice turn, music/voice/BLE coexistence and
  reboot/update recovery on the exact Dot build. The existing Action invocation
  can validate interaction policy first; wake execution, wake-over-music, AEC,
  beamforming and calibrated EQ need separate evidence. Any AEC reference must
  represent actual post-gain mixed speaker output with validated alignment.

## Android APK integration

The [Android target notes](../devices/android-generic/README.md) own device/API/ABI
qualification; the intended first tablet is the Galaxy Tab A7 Lite. There is no
Encore Android host/APK in this checkout. Reusable host code belongs in
`platforms/android/`, not Dot-specific packages.

- [ ] Build the UI-free SendspinDroid `:player` and `:shared` source modules at
  evaluated revision `bd1d90a8f79b8b78c1ee924a0e6c14016ed1f1e2`, or revalidate an
  upgrade. Their API 26+, compile SDK 36 and JDK 21 requirements are upstream
  prerequisites, not Encore's declared Android support range. Confirm actual
  tablet OS/API/ABI, codecs, dependency closure and licensing before adopting.
- [ ] Supply the native app/foreground-service lifecycle, permissions, audio focus,
  route changes, notifications, private identity/trust/settings and pairing UI.
  Bundle the matching Go voice engine and same-UID, versioned, bounded IPC in the
  signed APK. Dashboard reload must not own or terminate voice/music service life.
- [ ] Connect AudioTrack music output and voice playback through a validated
  concurrent route/mixer. Use temporary music track gain for ducking, not the
  player's voice-mute hook; combine it with current user gain and safety gates.
  Validate playback-head/timestamp drain and cancellation on route/permission
  loss. Advertise only codecs/formats demonstrated on the actual tablet.
- [ ] Validate speaker/microphone coexistence, wake-over-music and actual platform
  AEC before claiming hands-free support; do not assume Android effect
  availability proves quality or stack OS/software AEC without measurements.
- [ ] Package/sign the chosen source modules with notices and test background,
  battery restriction, screen/reboot, route recovery and signed-update behavior.
  Keep the independent Sendspin setting in native configuration, not a general
  privileged command bridge exposed to dashboard JavaScript.

## Completion gates

A successful cross-build or APK build establishes buildability, not audio support.
One target can qualify independently of the other. Record exact device/base,
source/toolchain pins, output formats and measured cases before declaring support.
Two-client protocol scheduling and physical two-device acoustic alignment are
separate gates; neither is proven by the single-player virtual lab. MA-managed
announcements, metadata presentation and acoustic sync remain unvalidated here.
