# Experimental Assist wake-audio validation

**Verified locally against real pinned Home Assistant and upstream app-voice
Kaldi. The extension is explicit opt-in; the default remains feature bits 7.**
Local edits only: no commit, push, merge, release or deployment. Reproduction is in
[the scoped lab](../lab/assist-wake/README.md). This is protocol integration with
injected capture/detection and fixture STT/TTS/speaker, not hardware acceptance.

## Compatibility is a specific development contract

Executed against the published foundation
`250af95320b3a3c4887f103ee1ce23f1dcce4a72`, HA `2026.12.0.dev0` at
`1395798e2338bd46ee522539f7281b336c44e3b8`, aioesphomeapi `46.30.0`, and
app-voice `b9461db9e6c5f19d30a9dc9563f93d2bc572c3c8`. No HA protocol code was
patched. This does not establish stable-HA or future development compatibility.

HA's [ESPHome negotiation](https://github.com/home-assistant/core/blob/1395798e2338bd46ee522539f7281b336c44e3b8/homeassistant/components/esphome/assist_satellite.py#L264-L392)
and [Voice Support eligibility](https://github.com/home-assistant/core/blob/1395798e2338bd46ee522539f7281b336c44e3b8/homeassistant/components/voice_support/__init__.py#L58-L80)
require all of:

- Device `API_AUDIO` plus `WAKE_WORD_AUDIO` feature (128), not just the Labs toggle.
- A nonempty active wake configuration, with **every** active phrase supported by
  the configured Voice Support API-1 app and Labs next-generation voice enabled.
- Subscription flags API_AUDIO `1 << 2` and WAKE_WORD_AUDIO `1 << 3`.
- Start metadata describing the actual mono PCM16/16-kHz pre-roll, followed by
  those bytes in order before continuing capture. This is not a second channel.

The pinned Go dependency predates these fields. The adapter locally encodes
protobuf request field 6 (model ID/string) and 7 (actual pre-roll bytes/uint32)
with normal unknown-field encoding; no dependency/schema fork. Peer tests decode
with real aioesphomeapi descriptors. Fields 8/9 (float probabilities) and 10
(channel-2 length) are absent. Protocol-2 uint8 scores are not HA probabilities.

`Server.ExperimentalWakeWords` must be configured before `Serve` with actual
host model IDs/phrases and must not be mutated during serving. Nil preserves
bits 7; explicit configuration adds only 128 (135). The lab's model/detection is
an explicit injection fixture, not a claim to execute an installed trained model.
Active selections are validated. When verification is negotiated, unknown or
inactive models, empty evidence, fractional milliseconds and over-limit evidence
are rejected before a start request; they do not silently become unverified turns.

The adapter uses existing core pre-roll, queued PCM and generation fencing.
There is no shared-contract, pre-roll-size or Go dependency change. Real
rejection event 13 explicitly disconnects the Assist generation, independently of
native Incubator HANGUP policy. Baseline cancel/reject fencing remains necessary
because this protocol has no turn ID. Experimental HA resubscription instead
revokes capture, flushes output, waits for the old writer, and sends stop before
rebinding on the same TCP connection. A forced pending-verifier test demonstrated
that replacing callbacks alone otherwise leaves HA's old pipeline running.

### Pinned HA needs an explicit configuration bootstrap

HA initially calls `_update_satellite_config` only under ANNOUNCE
([lines 383–392](https://github.com/home-assistant/core/blob/1395798e2338bd46ee522539f7281b336c44e3b8/homeassistant/components/esphome/assist_satellite.py#L383-L392)).
Advertising unimplemented ANNOUNCE would be false. The legitimate public
`assist_satellite/set_wake_words []` path loads the available configuration;
then setting `['fixture_model']` activates it. HA's
[set-configuration implementation](https://github.com/home-assistant/core/blob/1395798e2338bd46ee522539f7281b336c44e3b8/homeassistant/components/esphome/assist_satellite.py#L237-L282)
fetches it again and recomputes negotiation. This works without HA patches or
ANNOUNCE. It must be repeated after TCP reconnect on this revision. That usability
gap is a reason to retain opt-in status, not to add fake capabilities.

## The real positive fits the existing one-second buffer

`setup` synthesizes the complete eSpeak `en-us -s 200 'okay naboo'` utterance;
the spelling produces the spoken phrase `okay nabu`. The raw 16-kHz mono PCM is
27,034 bytes (844.8125 ms), SHA-256
`5db5b46ab46c254e74c49d08d31314a388c049fe86f2ddafbfff2c6252e72133`.
Only trailing silence pads the last transport frame, producing 27,520 bytes /
860 ms. Nothing is trimmed or fabricated to fit pre-roll. No core buffer extension
is needed for this fixture; other real phrases/detectors may need longer bounds.

The independent real upstream `/run` verdict was `wake-word-end` at 680 ms.
Through actual Encore Noise → HA → app-voice, HA then delivered exactly 21,760
bytes to fixture STT, SHA-256
`6509255106e0d121fd95edddc334c5f8c8c524ae0b91aec9c6a6a6fbed9937e5`:
the remaining 180 ms after upstream alignment plus the asymmetric 500-ms command
fixture. HA's real intent turned on `input_boolean.encore_lamp`; virtual playback
consumed 16,000 samples before drain completion.

The negative control was an equal-duration silence window. Real app-voice returned
`wake-word-rejected`, reason `prefix`; HA forwarded enum 13. There was no STT,
intent, playback or invented text, and a subsequent positive turn succeeded.
This establishes integration, not recognition accuracy or false-positive rates.

## Executed checks and negative controls

The following commands passed locally; run serially as documented in the lab:

- `lab/assist-wake/setup`: checksum-verified pinned archives, locked environment,
  built actual upstream resident decoder and regenerated the whole fixture.
  Warm rerun passed. Compiler warnings came from upstream OpenFST headers.
- `make check`: dependency boundaries, `go vet ./...`, `go test -race ./...`.
- `uv run --project lab --script lab/test_assist.py`: unchanged full real-HA
  baseline suite, including exact bits 7, encryption denial, correct pairing,
  PCM/hash, real helper intent, drain, cancel capture/recognition/playback, mute,
  failures, backend revocation and HA integration reload/reconnect.
- `uv run --project lab --script lab/assist-wake/test.py`: real positive/negative,
  missing verifier, Labs alone, empty/unknown evidence, cancel/reconnect, ordinary
  manual regression, live disable/enable and held-TTS revocation without false
  playback/drain. Full suite passed repeatedly after restarting current code.
- `uv run --project lab --script lab/assist-wake/test_peers.py`: encrypted fixture
  peers checked exact capabilities, unsupported subscription, omitted and present
  metadata, exact asymmetric 640-byte PCM, omitted scores/channel 2 and six-byte
  malformed evidence denial. Host ran with `go run -race`.
- `uv run --project lab --script lab/assist-wake/test_failures.py`: real upstream
  unsupported phrase declined negotiation; real HA exercised a labeled fake
  verifier for timeout, malformed negative timestamp, disconnect, pending-run
  cancellation and live renegotiation. Verifier WebSockets closed; old text and
  audio did not leak. Actual listener-down HA availability also checked.

Two deliberate/observed failures strengthened these checks:

- Omitting the real start-request metadata and restarting the host made the
  **same** real test fail: STT received 43,520 bytes/hash
  `91b4abea17c260a1a80e82ce65cd17d85934667d9522b7318ed441d50b4b9e17`,
  instead of 21,760/verifier-aligned hash. Restoring encoding and restarting
  passed. A normal successful conversation alone would have missed this fault.
- The forced pending-verifier case failed before explicit upstream stop on
  resubscription (`Timed out: live renegotiation aborts old HA verifier run`),
  then passed with the stop. It checks a real HA task/resource boundary.

Discarded runs included a cold race build exceeding the original 20-second host
readiness limit and HA subscription arriving before async entity registration.
The harness now waits up to 120 seconds for the build/listener and separately
waits for HA's entity before configuring it. A failed live-toggle run also exposed
cleanup while Labs was disabled; cleanup now enables it before restoring the app.
These were not counted as passing runs.

### Verifier failure is fail-open, not successful verification

HA's [Voice Support client](https://github.com/home-assistant/core/blob/1395798e2338bd46ee522539f7281b336c44e3b8/homeassistant/components/voice_support/client.py#L173-L183)
waits at most two seconds for a verdict and returns `None` on failure. Its
[pipeline](https://github.com/home-assistant/core/blob/1395798e2338bd46ee522539f7281b336c44e3b8/homeassistant/components/assist_pipeline/default_pipeline.py#L1268-L1291)
then retains the satellite's boundary. Real-HA timeout/malformed/disconnect
fixtures proceeded to STT/intent/TTS with exactly the 16,000-byte command stream,
not the 21,760-byte verifier-aligned stream. This is intentional upstream
fallback, **not a positive verifier verdict**. The normal WAKE_WORD_END event
does not distinguish it on this ESPHome path; do not manufacture a runtime
“verified” badge or claim fail-closed security from it.

## One common identity needs a separate voice-readiness contract

Runtime: with the actual satellite listener stopped and its HA config entry still
loaded, HA showed Assist `unavailable`, `restored: true`. The reason is not an
Assist `.available` override: [manager.py 905–931](https://github.com/home-assistant/core/blob/1395798e2338bd46ee522539f7281b336c44e3b8/homeassistant/components/esphome/manager.py#L905-L931)
updates the whole device on disconnect and unloads the Assist platform. Ordinary
ESPHome entities use connection-level `entry_data.available`
([entity.py 650–675](https://github.com/home-assistant/core/blob/1395798e2338bd46ee522539f7281b336c44e3b8/homeassistant/components/esphome/entity.py#L650-L675)).

Subscription/verification changes do not expose independent per-voice
availability. There is no native device message in this checked path that keeps
one healthy common connection while marking only existing Assist unavailable.
HA can disable/unload an entity or entry administratively, but that is not a
device-driven live ownership/readiness protocol. Do not disconnect common TCP
just to emulate availability, or invent another ESPHome/native-Incubator identity.
Parent-owned selected-backend/readiness state can preserve one identity and
healthy controls; mapping it onto the existing Assist entity needs HA-side
support. No selection/control-plane changes were made here.

## Remaining evidence contracts

Future arbitration needs actual model identity/version and threshold semantics;
capture format, channel/processing provenance and continuity; wake sample/time
bounds and real queued length; detection timestamp/clock domain; connection and
turn generation; and upstream accepted/rejected/fallback outcome with provenance.
Protocol-2 uint8 confidence is not automatically comparable across devices or a
HA float probability. This increment omits it and implements no local election.

No hardware runtime, trained host wake execution, optional CTC, second capture,
physical privacy/drain measurement, multi-device arbitration, local assistant,
timer scheduler, native Incubator integration or production packaging is proven.
Existing acknowledgements/notices are unchanged; separately downloaded upstream
sources/assets retain their terms. Encore-owned code now uses the
[MIT License](../LICENSE).
