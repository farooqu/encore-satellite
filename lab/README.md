# Reproducible protocol lab

This is a real Home Assistant service plus a virtual satellite, **not device
support or a new assistant**. Linux x86-64 is the tested lab host. The setup pins
Go 1.27.4, Python 3.14.7 and every Python/Go dependency in lockfiles.

Home Assistant is `2026.12.0.dev0`, source commit
`1395798e2338bd46ee522539f7281b336c44e3b8`, with aioesphomeapi 46.30.0. This is
an experimental **development** baseline, not a claim about stable 2026.11 or all
future 2026.12 builds. The source archive omits English translations;
`prepare_ha.py` checksum-verifies that exact archive and runs HA's own
`script.translations develop --all`, copying only generated language resources.
No HA protocol/session code is patched. HA runs with `--skip-pip`; dependencies
must come from `uv.lock`, not surprise runtime installs.

## Run in an Amp orb

From the repository root:

```sh
.agents/setup
make check
make lab-up
make lab-provision
make lab-test
```

Setup installs the system libturbojpeg and ffmpeg dependencies using sudo and
downloads the pinned toolchains. It does not onboard HA or create user secrets in
a reusable snapshot. `make lab-up` starts the supervised services in
`.amp/services.yaml`, waits for readiness, and prints the HA portal URL. They
survive pause/resume; use `amp orb service logs home-assistant` or
`amp orb service logs encore` to inspect them. Restart HA after changing its
fixture integration/configuration; restart Encore after changing Go code.

Provisioning onboards **only this isolated loopback instance**, selects the
deterministic pipeline, exposes `input_boolean.encore_lamp`, and pairs the virtual
satellite through HA's actual mDNS/configuration/Noise flow. After authenticated
login, it confirms a healthy active HTTP configuration trial so this HA dev
build does not roll it back and restart after five minutes. Failed/inactive
trials are never promoted. It removes cloud/demo
integrations automatically created by onboarding. It never reads a production HA
token. State is private under `$HOME/.local/state/encore-lab` (override with
`ENCORE_LAB_STATE` consistently for both services and clients). Credentials and
the unique generated Noise PSK are mode 0600 and are not stored in the repository
or printed. The credentials JSON there contains the `encore` user's random
password for signing into the HA portal. Do not publish that file or logs with
authentication headers. Ordinary HA login still protects the portal.

`make lab-test` deliberately re-pairs **the Encore virtual satellite entry** and
restarts the Encore service to reset its virtual playback instrumentation. It
changes only lab helpers/scenarios, leaves HA authentication intact, and restores
Assist mode and playback hold/mute on exit. Run `make lab-test-modes` separately
for the lab-only selector suite through HA's actual select/button/switch services,
including a process restart and temporary save/bind failures. That suite restores
the initial BLE choice. It is not a prerequisite for single-backend packages.
Do not repurpose these scripts for a real household instance.

## Optional real upstream service suites

Run these separately and serially; they share HA and port 8100. Stop Incubator
before starting the Assist wake host, and stop that host before Incubator. Use
the scoped setup/reproduction instructions rather than assuming shared setup
downloads models or installs the custom integration:

- [Native Incubator](incubator/README.md): actual upstream Kaldi, CTC/TDT,
  HA integration/intents and Piper, with synthetic capture/detection and a
  clocked virtual speaker. Covers upstream elections, rejection, confirmation,
  announcements and duration timers without implementing assistant logic here.
- [Experimental Assist wake verification](assist-wake/README.md): actual pinned
  HA and app-voice Kaldi, with fixture STT/TTS/capture/playback. Explicit opt-in;
  default Assist capabilities stay 7. Verifier fault injection is labeled and
  demonstrates HA's fail-open fallback, not confirmed wake acceptance.
- [Sendspin music and voice ducking](sendspin/README.md): actual Music Assistant
  queue and C++ client, plus Assist/Incubator turns, feeding a virtual mixed
  audio sink. Independently switchable and disabled by default; no physical
  audio or synchronization claim. Follow its separate service instructions to
  avoid two satellite hosts competing for port 8099.

The scoped scripts run only in this isolated lab. Their downloaded services,
models and tools keep separate licenses; [Incubator notices](incubator/NOTICES.md)
include the GPL-licensed Piper dependency and model-specific terms. No binary
distribution permission or device support follows from running these tests.

## Real services versus fixtures

- **Real:** HA onboarding/auth/configuration, multicast discovery, ESPHome native
  API/Noise, capability negotiation, Assist pipeline, HA conversation/intents,
  helper state, TTS transport and HA cancellation/reconnect behavior.
- **Fixtures:** loopback control API, deterministic PCM input, custom `encore_fixture`
  STT (consumes/hashes audio, returns a fixed command), mathematical TTS tone and
  bounded clocked virtual speaker. No speech recognition accuracy, physical audio,
  wake model, AEC, EQ, hardware mute or real speaker drain is tested.

Controls are loopback-only on port 8099: `GET /state`, `GET /trace`,
`POST /command`. Commands are `start`, `wake`, `capture`, `end`, `stop`, `mute`,
`mode`, `restart` and `hold_playback`. `mode` takes `backend:assist|incubator` and
only saves the choice; `restart` replaces the runtime when that choice differs.
Capture accepts 1–1024 signed PCM16 samples. The
native encrypted API is port 6053 and advertises the local virtual mDNS identity;
the unencrypted native Incubator adapter listens on loopback port 6055 only in
Incubator mode. Native mDNS discovery is supplied by the separate Incubator
suite's recording proxy, not by this loopback host.
Do not expose these lab control/Incubator ports to an untrusted network.

## Lab-only restart-bound voice mode and independent BLE

These controls belong only to `cmd/encore-lab`; the Go command is not importable
by device hosts. Production packages link one voice backend, Incubator by default.
Changing that backend requires replacing the application package, not these
controls. The lab deliberately links both adapters to exercise their contracts;
its Assist default is a test prerequisite, not the product default.

The encrypted ESPHome endpoint exposes **Voice mode (restart required)**,
**Apply and restart**, **Running voice mode** and **Bluetooth proxy**
in both modes. Choose **Home Assistant Assist** or **Voice Incubator**, then press
Apply. Selection alone never interrupts a turn or restarts BLE. The diagnostic
reports the running route, not whether the remote assistant is connected.

The host saves `voice_mode`, `bluetooth_proxy` and software `muted` state in private
`settings.json` alongside the existing Noise key. Missing configuration defaults
to Assist with BLE off; invalid saved modes fail startup without fallback.
Writes sync a temporary file and atomically rename it before reporting success.
A failed save does not change the saved/running mode. Mute takes effect even if
its save fails: the effective privacy latch survives runtime Apply and is included
in subsequent mode/BLE saves. Unmute requires a successful save. A failed mute
write cannot promise persistence across process death or power loss. This is not a
qualified power-loss/storage-recovery mechanism. Mode is runtime configuration, not a device
planning profile, package variant or download.

Apply cancels old voice output, closes the old listeners/connections, waits for
their workers to stop and starts a fresh engine with only the selected voice
adapter. In both modes, accepted ESPHome sockets are tied to runtime retirement
before reaching the protocol server, so a silent peer awaiting its Noise
handshake cannot strand Apply. HTTP admission and mutations are fenced during
teardown; a request still reading its body cannot mutate the retired runtime or
shared settings.
After a graceful-shutdown timeout, connections are closed and handlers joined
before replacement. The same ESPHome identity/key and BLE/mute choices survive.
BLE reconnects during this deliberate runtime restart; ongoing GATT sessions are
not promised uninterrupted. The virtual radio emits changing advertisements but
does not implement GATT. Ordinary mute/unmute never pauses or disables the proxy.

Incubator mode keeps the encrypted settings/BLE endpoint with voice capability
zero. An offline Incubator peer, or failure to bind its loopback listener, leaves
management and BLE available with no fallback to Assist. `/state` separates
configured/running/restart-required mode, session connection state and local
backend startup error. Selecting Assist and applying remains possible.

On the pinned HA baseline, removing Assist capability removes its satellite and
voice-only selectors. Switching back restores the original IDs if they remain
free. The four common controls retain registry/device identities. The separate
native Incubator integration may create its own persistent timer device; the
selector does not fabricate or merge HA devices.

## Compatibility boundary and gaps

Default Assist bits are exactly voice assistant, speaker and API audio (7).
There are no invented partial captions, local timer scheduling, media-player
entities, Assist announcements, downloads, AEC/multichannel audio or working
hardware profiles. Native Incubator can supply explicit announcement and timer
audio; Encore only presents and drains it, and forwards Stop upstream.
The composed host lists the four common controls; standalone Assist can still
terminate an empty list. Only one selected backend owns a session;
unsupported/competing subscribers cannot displace it.

HA's experimental wake-audio verification path additionally requires negotiated
`WAKE_WORD_AUDIO`, actual wake evidence, active models supported by a configured
upstream Voice Support verifier, and Labs enabled. The adapter implements the
version-scoped opt-in; merely running this dev version or enabling Labs does not
establish support. This HA revision needs the public wake-word configuration
bootstrap again after TCP reconnect. A normal wake-end event cannot distinguish
successful verification from upstream verifier-error fallback.
Quality-based cross-device arbitration for Assist is also not implemented.

Native Incubator preserves upstream election evidence and honors its decision;
no local pre-election runs. Negative HANGUP ends a call without disconnecting a
healthy transport. See [executed proof](proof.md) and the scoped reports for
real-service coverage and remaining gaps.

Mute silences and flushes local voice output, clears microphone pre-roll and cancels
the current pipeline: Assist receives an abort request; native Incubator receives
`CALL_END {reason:cancelled}` for calls. The shared ESPHome transport stays connected,
so Bluetooth controls need not restart. Hosts must still stop/gate physical capture.
Already executed actions cannot be undone. HA may finish streaming an already
produced reply; the muted/cancelling engine discards it without claiming playback.

Active native cancellation sends `CALL_END` and then retires only the native
voice socket, not ESPHome/BLE. Ordinary and cancellation `AUDIO_OUT_END` frames
are identical, so neither may admit a new call on the retiring connection.
Mute also retires output interrupted by Stop, even if its final drain is pending.
Pinned upstream does not stop independent announcements on `CALL_END`.
Old native tasks can survive reconnect; local call output remains blocked until
upstream signals `listening`, after processing CALL_START and cancelling the old
announcement. Voice recovery follows upstream's reconnect backoff. Idle
mute/unmute does not reconnect and restores playback for fresh announcements/timers.

Unmute does not revive the old turn. A new turn waits for Assist's `RUN_END` and
any expected TTS stream end, or a fresh Incubator connection generation. During
this interval the snapshot says `recovering`; capture/pre-roll are ignored.
An early Assist mute defers its wire abort until `RUN_START` because HA can cancel
an unscheduled pipeline without emitting `RUN_END`; unsent microphone audio is
purged immediately, without removing queued start/control messages. Missing
terminal messages leave voice blocked; no timeout drops the shared BLE connection.
A deliberate transport reset clears that fence and can interrupt BLE/GATT.
Network reconnection is accepted while still muted and cannot resume capture or
old playback. The separate Stop command retains its existing cancellation policy.

The core retains live-selection fencing for embedding hosts and its existing
tests, but the lab exposes only the persistent restart-bound selector described
above. No device settings screen or production lifecycle host is implemented.
Media is intended to use Sendspin, not an ESPHome media-player entity; neither
Sendspin nor shared music/voice playback arbitration is implemented by this work.

There is no Dot/Android runtime, installer, APK, update mechanism or binary
release. The user's planned hardware remains aspirational. New Encore code's
license remains unselected; `../ACKNOWLEDGEMENTS.md` distinguishes research credit
from included MIT code and lists required notices.
