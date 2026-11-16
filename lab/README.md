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
playback hold/mute on exit. Do not repurpose these scripts for a real household
instance.

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
`backend` and `hold_playback`. Capture accepts 1–1024 signed PCM16 samples. The
native encrypted API is port 6053 and advertises the local virtual mDNS identity;
the unencrypted native Incubator adapter is restricted to loopback port 6055.
Do not expose these lab control/Incubator ports to an untrusted network.

## Compatibility boundary and gaps

Default Assist bits are exactly voice assistant, speaker and API audio (7).
There are no invented partial captions, local timer scheduling, media-player
entities, Assist announcements, downloads, AEC/multichannel audio or working
hardware profiles. Native Incubator can supply explicit announcement and timer
audio; Encore only presents and drains it, and forwards Stop upstream.
An empty entity list terminates normally. Only one selected backend owns a
session; unsupported/competing subscribers cannot displace it.

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

The core and loopback fixture support live backend selection, not yet a product
setup/HA selector. Common ESPHome controls should share one connection and stay
independent of voice selection. The pinned Incubator integration creates a
separate persistent timer device, not another Assist entity or a voice-availability
signal. Marking only the existing Assist entity unavailable while retaining
common controls needs HA-side support; no duplicate identity is fabricated here.

There is no Dot/Android runtime, installer, APK, update mechanism or binary
release. The user's planned hardware remains aspirational. New Encore code's
license remains unselected; `../ACKNOWLEDGEMENTS.md` distinguishes research credit
from included MIT code and lists required notices.
