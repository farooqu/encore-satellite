# Real native Voice Incubator lab

Linux x86-64, real upstream app/models and the existing isolated Home Assistant
lab, with synthetic capture and a clocked virtual speaker. This is not device,
microphone, acoustic-quality or release proof. See
[executed validation](../../docs/incubator-validation.md) and
[downloaded material](NOTICES.md).

## Reproduce

Run from the repository root. Keep the foundation's native HANGUP/announcement
lifecycle fix alongside the Incubator adapter's `data.active` mapping; the
published foundation alone deliberately fails the negative-turn case.

```sh
.agents/setup
lab/incubator/setup
amp orb services ensure
amp orb service restart home-assistant
make lab-provision
.amp/in/orb-backup/voice-incubator/.venv/bin/python lab/incubator/fixtures.py
amp orb service start incubator --command '.amp/in/orb-backup/voice-incubator/.venv/bin/python lab/incubator/run.py' --port 8100
.amp/in/orb-backup/voice-incubator/.venv/bin/python lab/incubator/test_native.py
make check
```

Wait for HA to finish starting before provisioning; rerun provisioning if it
reports startup discovery unavailable. Run the Incubator suite only after both
`GET http://127.0.0.1:8099/state` and
`GET http://127.0.0.1:8100/api/settings` succeed. These URLs are loopback checks
inside the orb, not user-facing portals. Do not run other suites that select a
backend concurrently. On subsequent runs restart `incubator` to reset upstream
reconnect backoff. Restart `encore` after Go edits. The source is run through
the foundation's `go run ./cmd/encore-lab` supervised service, not a stale binary.
The suite now saves the requested voice mode and applies a runtime restart at
each mode transition through HA's actual encrypted selector/button controls;
it no longer uses the old live `backend` fixture command. The isolated native
environment runs the suite without importing Assist test dependencies.
It enables the virtual BLE proxy for the run, observes advertisements inside HA
and restores the previous BLE setting afterwards.

Setup is separate from shared `.agents/setup`, `.amp/services.yaml`, Makefile and
the foundation lockfile. It installs native system libraries/build tools, clones
the two exact revisions below, installs Python 3.13.5 with hash-locked packages,
downloads model assets, builds the Kaldi verifier and upstream native C extensions,
and symlinks the custom HA integration into the isolated lab state. It does not
patch upstream source. Upstream tracks two optional compiled `.so` files; the
setup's in-place extension build replaces those only. Other modified tracked
upstream files stop setup rather than being reset.

- App: `OHF-Voice/voice-incubator`
  `38b573b13a92f0654b24611a3a6355c8e8c4d5c3` (0.3.2).
- HA custom integration: `OHF-Voice/integration-voice-incubator`
  `e02de9447868b7cdc90dc6437618662b3ebef6f6`.
- HA: foundation's `2026.12.0.dev0`,
  `1395798e2338bd46ee522539f7281b336c44e3b8`; no HA source changes.

The checkout, venv and build outputs are under `.amp/in/orb-backup/`.
Models/generated PCM/app data are under `$HOME/.local/state/encore-incubator`;
HA authentication/configuration remain under the existing
`ENCORE_LAB_STATE` (default `$HOME/.local/state/encore-lab`). The launcher caches
a 30-day **isolated lab** HA token as `incubator-token`, mode 0600. Never use a
production HA token or publish that file. The Incubator state path is fixed for
this scoped fixture; `ENCORE_LAB_STATE` overrides HA state only.

On a small orb, stop `incubator` before regenerating fixtures or rebuilding its
environment to avoid overlapping model memory. Start/restart it afterwards.
Use `amp orb service logs incubator`, `status incubator`, `restart incubator`
and `stop incubator`; no detached processes are required. The app's unauthenticated
development HTTP port must not be exposed as a public portal or to an untrusted
network. Likewise the native TCP adapter and fixture/proxy ports are loopback.
The test advertises them using actual multicast DNS; an orb without working
multicast cannot substitute direct connection and call it discovery proof.

## What the suite actually runs

The app discovers `_voice-incubator._tcp.local.` through multicast and receives
Encore's actual native protocol-2 HELLO. A transparent TCP recorder on 6057
forwards unchanged frames to Encore's adapter on 6055. Capture enters the lab's
existing public `/command` interface, not private core methods.

- Piper generates reproducible mono PCM16/16 kHz wake, command, unknown-command
  and confirmation fixtures. Their **speech** is synthetic; recognition is not
  mocked. Wake detection scores are injected at the device boundary because
  Encore's core consumes host detections, not a local model implementation.
- Real Kaldi verifies the wake audio; real shared CTC/TDT recognition selects the
  command; the real HA intent changes `input_boolean.encore_lamp`; real Piper
  produces the reply. The upstream recorded WAV is compared with the exact
  injected PCM prefix, and wire evidence preserves all three candidate scores.
- A held clocked virtual speaker must actually consume PCM before drain.
  Cancellation flushes without false completion. An intentional socket loss
  exercises reconnection, not automatic successful cancellation recovery alone.
- Real silent-wake rejection is followed immediately by a valid turn on the
  same transport. Two candidates with asymmetric scores exercise upstream
  election in both directions, without a local arbiter.
- The **second satellite** is explicitly a native wire/synthetic-sink fixture on
  6056. It supplies evidence; it does not supply verification, recognition,
  election results or HA actions.
- Announcement initiation uses upstream's typed `/api/try` command from that
  peer. This bypasses the **sender's** microphone path only. The receiver gets
  actual `announcement active=true`, chime, Piper, two independently drained
  streams and `active=false`, without creating a fake local call.
- The app asks for confirmation; the following `follow_up` has zero pre-roll;
  real recognition of the synthetic “Yes” triggers the action. A duration timer
  is created through HA's real service, rings the idle satellite, then device
  Stop reaches upstream as `stop_timer` and upstream reports `timer_stopped`.
- Switching away/back inspects app connection state and actual HA registry/entity
  state. Saving leaves the running mode and BLE scan unchanged; Apply preserves
  common entity identities and BLE enablement, then advertisements resume. The
  timer entity is persistent, **not** per-satellite availability.
- Voice-only mute sends CALL_END before retiring the native connection, without
  false playback drain. The replacement connection retains mute; after unmute a
  fresh recognized HA turn succeeds. Neither old ordinary AUDIO_OUT_END nor the
  identical cancellation ACK can release admission on the retiring connection.
  Deliberate native socket loss while muted reconnects without unmuting; starts
  remain rejected. HA receives changing BLE advertisements during mute and after
  reconnect with no radio scan restart.
- Idle mute/unmute is followed by a genuinely fresh announcement and HA timer.
  Muting an active announcement closes only native voice, preserving BLE. The
  proxy deliberately holds the next CALL_START while upstream's old task resumes
  Piper on its reconnected socket. No old samples play or claim drain; releasing
  CALL_START permits a fresh recognized HA action/reply. This case waits for a
  stable connection before forcing loss, so real upstream reconnect backoff is
  shorter than its 30-second chime drain timeout.

The suite intentionally edits only isolated lab command specs/helper/timer state
and temporary multicast entries. Its `finally` block restores playback hold and
stops the turn. Do not point it at household services. Run only one instance.
Malformed frames, unsolicited idle PCM and stale generations have deterministic
Go contract tests; this script does not pretend malformed messages came from
the real upstream app. Announcement cancellation/reconnect now also runs against
the actual upstream app as described above.

## Why the missing-model prerequisite was not hardware

Upstream `downloads.fetch` installs `IMAGE` assets, including wake/Kaldi tools,
not English shared CTC/TDT speech assets. `downloads.missing` iterates linked
model directories; without `downloads.link` those absent directories are skipped.
`models.py` uses the same link-then-fetch/download layout as upstream's image and
asserts `languages.load("en").missing(APP)` is empty before startup. OpenBLAS
is required by the compiled verifier. SoundFont/FluidSynth support is needed for
the real announcement chime path, even with ordinary call sounds disabled.

No wake training/model-transfer, AEC/dual capture, monitoring/intercom, scheduled
alarm, streamed-agent empty-text reply, physical device or speech-quality claim
is established here. Speaker identification and embedding matching are disabled.
The standard upstream download API still fetches some unused assets; see notices.
