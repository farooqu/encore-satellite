# Real Music Assistant Sendspin lab server

This runs the **unmodified real MA core, Sendspin provider, queue and streaming
controllers**, not a protocol mock. The small launcher adds one local WAV source
route; it does not implement playback or Sendspin itself. Run from the repository
root, inside the orb.

## Pinned components

* [MA server `07780231361dd45668f937fc3fe950b38527fde0`](https://github.com/music-assistant/server/tree/07780231361dd45668f937fc3fe950b38527fde0)
  (current development source when prepared, November 16, 2026).
* Python 3.14.7; frontend 2.17.372; models 1.1.242.
* `aiosendspin[server]==10.3.0`, PyAV 19.0.2. All installed dependencies and hashes
  are in `uv.lock`; setup uses `uv sync --frozen`.
* FFmpeg n8.1.3, BtbN build `f1acb0a266`, release
  `autobuild-2026-11-16-15-29`; archive SHA-256
  `18565f806f14e81508f6fcdcaf54c514e8ae6d5d16f775418096e6354961bf52`.
  The orb's FFmpeg 7.1.5 is too old: this MA revision requires FFmpeg >=8.
* Receiver compatibility target: `sendspin-cpp`
  `a4a9b65fb356657e3a39a2ab70b4aae957bf53e0`.

MA is run using its supported-in-code `safe_mode=True` switch: builtin providers
including Sendspin **still load**; optional default LAN players, Smart Fades and
the community catalog do not. This avoids automatic DLNA/Chromecast dependency
installation and optional heavyweight ML providers. The lab environment explicitly
omits upstream's torch, torchaudio, librosa, and experimental WebRTC
aiolibdatachannel dependencies. No stubs, import shims or patches to upstream are
used. Consequently this is a deliberately limited **source development install**,
not a supported/full MA distribution. MA reports version `0.0.0` because its source
package is not installed as a wheel; the commit above identifies it precisely.
Observed idle MA RSS: approximately 138 MiB in the 2 GiB orb; no torch import.

## Setup, start and readiness

```sh
lab/sendspin/server/setup
amp orb service start sendspin-ma \
  --command /home/user/workspace/repo/lab/sendspin/server/run --port 8097
python3 lab/sendspin/server/control.py bootstrap
python3 lab/sendspin/server/control.py status
amp orb service logs sendspin-ma --lines 30
```

Use `amp orb service restart sendspin-ma` for an existing service. The launcher
runs in the foreground under Amp supervision and handles SIGTERM cleanly.
No portal is automatically published. API, source fixture, streams and Sendspin
listeners bind to **127.0.0.1** only:

| Port | Purpose |
| --- | --- |
| 8097 | MA frontend, authenticated `POST /api`, local `/lab/fixture.wav` |
| 8098 | Actual MA queue/flow stream HTTP server |
| 8927 | Actual Sendspin WebSocket `/sendspin` |

All state, logs, SQLite databases, credentials, server identity and trust records
are private under `~/.local/state/encore-sendspin-lab` (directory mode 0700).
The venv is there too. `auth.json` contains the helper's long-lived token (0600);
`credentials.json` holds a randomly generated lab-only administrator password.
Never copy these files into git, scratch, screenshots or logs. MA's Sendspin
identity lives at `ma/sendspin/identity.key`; client approvals/pairings are written
to `ma/sendspin/pairing_store.json` when the first is made. Keep them across restarts.
The receiver's separate `~/.local/state/encore-lab/sendspin-keys.json` is not modified
by these scripts.

Sources/downloads are only in `.amp/in/orb-backup/upstream-research/music-assistant-server`
and `.amp/in/orb-backup/sendspin-server-tools`. Setup never overwrites existing MA
settings or fixture and refuses a dirty upstream checkout. Do not modify the
private settings JSON while MA is running; use its API.

MA's **shared core discovery** still uses its normal default mDNS interface;
this is not an OS network namespace or complete egress firewall. The Sendspin
provider's own mDNS uses loopback, UPnP scanning is off, no nonbuiltin integrations
load, and no production account or endpoint is configured. Do not broaden the
listener bindings or import production configuration.

## Receiver registration and deliberate encrypted unpaired approval

The proposed contract is compatible at the source level: protocol version **1**,
Noise `KKpsk2_25519_ChaChaPoly_SHA256`, and `player@v1`, `controller@v1`,
`metadata@v1` exist on both sides. Start the real C++ receiver with:

* target `ws://127.0.0.1:8927/sendspin`;
* `accept_inbound_connections=false` (MA need not dial the client);
* persistent unique upstream `FilePersistenceProvider` identity;
* `client.set_unpaired_access_enabled(true)` explicitly for this isolated lab;
* only PCM signed 16-bit, 48000 Hz, 2 channels as the supported audio format.

This revision starts with `client/init`, Noise handshake, then hello/activation;
do **not** send the older plaintext `client/hello` protocol. Both setup and
`control.py bootstrap` set the real Sendspin provider's `allow_legacy_clients`
to **false**. Encrypted unpaired does **not** mean legacy/plaintext: it uses the
published sentinel PSK and encrypts transport but does not authenticate the peer.
It is unsuitable for an untrusted network.

The receiver appears automatically after its hello; there is no separate fake
player registration API. Inspect actual MA-assigned player and queue IDs:

```sh
python3 lab/sendspin/server/control.py players
python3 lab/sendspin/server/control.py api player_queues/all
```

**This pinned MA revision automatically approves guest playback** for the proposed
player/controller/metadata-only receiver. In `SendspinProvider._auto_trust_guest_access`
(`provider.py`, lines 1849-1880), MA calls `server_api.trust_unpaired(client_id)` when:

* the client's hello exists and advertises `unpaired_access.enabled=true`;
* its live connection is not authenticated with a `LONG_TERM` pairing PSK;
* **none** of its negotiated roles requires pairing (these three roles do not;
  a source/line-in role can prevent this path);
* there is no existing trusted-unpaired record for that client; and
* the client event is still current after the asynchronous trust-store lookup.

Thus an available player with `needs_setup=false` normally needs **no operator
action** in this lab. This is upstream automatic policy, not a consent guarantee
implemented by our helper. Encrypted-only mode does not disable this guest
autoapproval or authenticate guest peers. Do not treat loopback lab behavior as a
safe general-purpose policy for untrusted networks. An existing stored pairing
alone also does not prevent autoapproval if the live connection is a guest.

Only if the real player still needs setup and its flow offers unpaired consent:

```sh
python3 lab/sendspin/server/control.py approve-unpaired '<actual-player-id>'
python3 lab/sendspin/server/control.py players
```

The helper runs the real `config/players/setup` flow, requires its `approve_device`
step, then submits `{"connect_method":"unpaired"}` via `config/flows/submit`.
For an already-approved guest, rerunning setup may instead offer `enter_token`
for optional pairing. The helper deliberately refuses that step and explains the
operator no-op; it does not infer guest trust merely from `needs_setup=false`,
submit a token, or revoke/edit live trust. Check `players` before calling it.
The helper itself does not globally auto-trust clients, edit trust JSON, approve
a source/audio input role, or fall back to plaintext; upstream's automatic guest
approval described above still occurs independently.

For deliberate **pairing instead**, use the same setup flow and submit
`connect_method: "pair"` if the consent step is offered; select
`pairing_method: "token"` if a method-selection step appears; then submit
`pairing_token` from `client.client_pairing_token()` on the `enter_token` step.
Use `config/flows/get` with the returned `flow_id` for progress and inspect each
step before submitting. Tokens/PINs are secrets: pass them from private files in
a Python process using `control.api(...)`, never CLI arguments or terminal output.
No receiver listener is needed for the connected-client flow.

## Deterministic local fixture through the actual MA queue

`prepare.py` creates 120 s of 1 kHz integer triangle wave, identical stereo,
PCM S16LE / 48 kHz, peak 4096 (~-18 dBFS). SHA-256 of the whole WAV:
`58e64fc2c79b10c42b6b0c1ab0d86c42a751df7fee0780fff3e785cd538bbd03`.
The fixture is generated locally; there are no music services or credentials.

Current MA intentionally rejects `file://` and filesystem paths in its builtin
URL provider. The launcher therefore exposes **only this one file** at
`http://127.0.0.1:8097/lab/fixture.wav`; MA resolves it through its real builtin
provider, probes it with FFmpeg, queues it, and streams it through its actual
stream controller/Sendspin provider. It is not direct binary injection into a
Sendspin socket. Do not point the receiver at the fixture URL.

```sh
# Safe even before a receiver exists: validates real MA media resolution.
python3 lab/sendspin/server/control.py api music/item_by_uri \
  '{"uri":"http://127.0.0.1:8097/lab/fixture.wav"}'

# Once available (automatic guest approval or explicit setup): optional for stable amplitude measurements.
python3 lab/sendspin/server/control.py api config/players/save \
  '{"player_id":"<actual-player-id>","values":{"volume_normalization":false}}'

# Use actual queue_id from player_queues/all (often the MA player_id).
python3 lab/sendspin/server/control.py play '<actual-queue-id>'
python3 lab/sendspin/server/control.py api player_queues/get \
  '{"queue_id":"<actual-queue-id>"}'
python3 lab/sendspin/server/control.py api player_queues/stop \
  '{"queue_id":"<actual-queue-id>"}'
```

`play` invokes `player_queues/play_media` with the fixture URL and
`option: "replace"`. MA may classify this tagless WAV as a `sound_effect`; the
validated media item still has duration 120, 48 kHz, 16-bit, stereo, provider
`builtin`. This command goes through the queue, not the announcement API.

### Programmatic API example

Run with the system Python from the repository root. Do not print auth tokens:

```python
import importlib.util
spec = importlib.util.spec_from_file_location("ma_lab", "lab/sendspin/server/control.py")
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)
players = lab.api("players/all")
queues = lab.api("player_queues/all")
# lab.api("player_queues/play_media", {
#     "queue_id": actual_queue_id,
#     "media": "http://127.0.0.1:8097/lab/fixture.wav", "option": "replace",
# })
```

Underlying API: `POST http://127.0.0.1:8097/api`, JSON
`{"command":"players/all","message_id":"lab","args":{}}`, with
`Authorization: Bearer <private token>`. HTTP returns the result directly, not a
WebSocket-style `result` envelope.

## Validation performed

* Frozen environment install and repeat setup succeeded; upstream checkout clean.
* Real MA startup succeeded, actual Sendspin and builtin providers available.
* Authenticated admin creation, token creation, `auth/me`, `providers`, player and
  queue APIs succeeded; bootstrap is repeatable.
* All three listeners verified on 127.0.0.1; actual fixture HTTP SHA-256 matches.
* Actual `music/item_by_uri` resolved the fixture with expected duration/format.
* A plaintext connection was rejected; MA logged "Refused unencrypted legacy
  connection ... transition mode is off". No probe player was registered.
* Shell/Python syntax checks succeeded.

These are server-only checks. The combined [Sendspin lab](../README.md) also
exercises real C++ receiver registration, encrypted guest playback and voice
ducking with Incubator. No explicit-consent pairing claim follows
from automatic guest approval. No production services or credentials were used.
