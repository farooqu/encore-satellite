# Real upstream Voice Incubator validation

Verified on the local Linux x86-64 stack with the **actual upstream app, wake
verifier, speech recognizer, HA integration/intents and Piper**, through Encore's
native TCP adapter. Synthetic capture, injected host wake detections and a
clocked virtual speaker replace device boundaries only. No physical device,
acoustic quality, household deployment or release claim is made.

## Exact baseline and environment

- Fetched Encore foundation: `250af95320b3a3c4887f103ee1ce23f1dcce4a72`.
- Voice Incubator 0.3.2: `OHF-Voice/voice-incubator`
  `38b573b13a92f0654b24611a3a6355c8e8c4d5c3`.
- HA custom integration: `OHF-Voice/integration-voice-incubator`
  `e02de9447868b7cdc90dc6437618662b3ebef6f6`.
- Real HA: foundation's `2026.12.0.dev0`,
  `1395798e2338bd46ee522539f7281b336c44e3b8`.
- App venv: Python 3.13.5, `lab/incubator/requirements.lock`; HA keeps its separate
  Python/lockfile. Upstream app and HA source are unpatched. Setup builds the
  verifier/extensions from upstream; no model binaries or secrets are tracked.
- Main model repository: `rhasspy/voice-incubator-models`
  `e359089e2dfe430415b24410a089fdcf36fd5bf9`. English Parakeet shared CTC/TDT,
  Kaldi and Piper voice provenance are recorded in
  [lab notices](../lab/incubator/NOTICES.md), including secondary asset pins and
  independent Piper SHA-256 values. Do not treat all downloaded dependencies as
  permissive: the installed Piper engine is GPL-3.0-or-later.

Reproduction commands and the real/fixture boundary are in
[lab/incubator/README.md](../lab/incubator/README.md). Services are supervised
`home-assistant`, `encore` and independently started `incubator`; no shared setup,
module, service declaration, release or device-package file was changed here.

### Startup prerequisite resolved, not worked around with fake recognition

Reproduced the missing-English-model readiness on an unlinked model root.
`downloads.fetch` installs upstream `IMAGE` assets (wake models and Kaldi tools),
not the speech assets. `downloads.missing` only visits linked speech directories,
so an unlinked working checkout can falsely report nothing to download while
`languages.load("en").missing(root)` still names the shared CTC model.

Scoped setup follows upstream's own image layout: `downloads.link`, `fetch`,
download missing English/Piper files using upstream's pinned checksum manifests,
then assert English readiness. The Kaldi verifier also needs OpenBLAS. Real
announcement chimes need FluidSynth/SoundFont packages. Startup and a warm setup
rerun both succeeded; no hardware prerequisite caused the missing-model check.

## Actual before/after, not just peer contract tests

The coordinator's exact local `native-hangup-v1.patch` was received and applied
as a verification dependency. Its SHA-256 is
`7536c3a7af7da7169ef9c8c94d089c5252eaaa2239db52b056e2d1b13d9894a6`.
It owns the shared session lifecycle fix; this scope adds the narrow native
`data.active` mapping and a public-wire announcement regression test.

The wire regression also fails if only the adapter's Active mapping is removed:
the first idle announcement stream never drains (TCP read timeout, exit 1).
Restoring the mapping passes both streams and rejects PCM after `active=false`.

Ran **the same strict `lab/incubator/test_native.py`** against rebuilt/restarted
Encore processes, first with the parent's patch reversed, then restored. The
adapter's Active parsing remained present in both runs.

- Before core/session/session.go SHA-256:
  `5c6aa45634dc1008d79b03408dd78438cdad5256729cf60ffba12af47e4e118b`.
  Real handshake, accepted wake/recognition/HA action, held playback and
  cancellation passed. Real Kaldi rejection then failed:
  `AssertionError: Timed out: quiet idle after rejected candidate` (exit 1).
  The foundation closed the healthy native transport on negative HANGUP;
  upstream's connection loop applies exponential reconnect backoff.
- After core/session/session.go SHA-256:
  `54bd1c017fe142ce7804704ddf8684c102194d21675cfde126bc9fc857d742a0`.
  Same suite passed (exit 0). In particular:
  `PASS real Kaldi rejection → quiet idle → immediate successful next turn, same native transport`.
  It asserts no new HELLO between rejection and the successful next turn.
- A first comparison attempt ran before Encore's HTTP socket was ready and
  failed with connection refused. That run was discarded, not counted as the
  regression. Both services were readiness-checked before the valid comparison.

Negative HANGUP now ends/flushes the turn and stale captions, retains transport,
and never invents automatic follow-up. Idle PCM needs an active call, actual
timer alert or explicit announcement ownership. Stop's existing discard fence
still lasts through `AUDIO_OUT_END`. Announcement inactive/Stop/disconnect ends
announcement ownership. These shared changes are the parent's work, not a new
local arbitration policy.

## Executed real-service cases

`lab/incubator/test_native.py` passed repeatedly with the corrected core and
Active mapping. It checks frames, actual upstream call records, recorded WAV,
HA registry/entity/helper state and virtual sink consumption, not just logs.

- **Discovery/handshake:** actual multicast `_voice-incubator._tcp.local.` to a
  transparent recording proxy, then real protocol-2 HELLO from Encore. Stable
  MAC `02:00:00:ec:00:01`; honest no-AEC/single-capture capabilities.
- **Wake → recognition → HA → reply:** generated “Okay Nabu” is accepted by real
  Kaldi; shared CTC/TDT selects `encore_lamp`; HA's real `HassTurnOn` turns on the
  actual helper; Piper produces PCM; sink consumption precedes drain and HANGUP.
  The final comparison after-run consumed 18,019 reply samples. Piper replies
  use upstream synthesis settings and can vary in length; exact output length
  is derived from the actual wire PCM, never hard-coded as a speech oracle.
- **Original evidence:** exact probability 173, max 231, cutoff 51 and 32,000
  pre-roll bytes reach upstream unchanged. All captured wire PCM equals the
  supplied PCM. The independently fetched upstream WAV equals its prefix,
  SHA-256 `a7be072bea88d3c3e6ce0cb0ff229cdf4c2a840ac535ec3c91968452182119dc`.
- **Held reply/drain:** with real Piper output queued and the virtual speaker
  held, zero samples play and no `PLAYBACK_DRAINED` or HANGUP is sent. Releasing
  the speaker consumes the exact output, sends drain, then upstream hangs up.
- **Cancellation/reconnect:** Stop flushes held real output without playback or
  false drain and sends `CALL_END {reason:cancelled}`. The test then deliberately
  drops the recorded socket and observes a fresh actual native HELLO/recovery.
- **Real negative wake:** silent pre-roll reaches real Kaldi and produces
  upstream `wake_rejected`. The immediate next real recognized turn succeeds
  without reconnect; no local score filter manufactures the rejection.
- **Selection lifecycle:** selecting Assist disconnects native transport in the
  real app; selecting Incubator reconnects the same MAC/HA registration. The HA
  timer entity remains available, explicitly disproving it as voice readiness.
- **Unknown speech:** real CTC/TDT produces `not_recognized`; actual upstream
  error reply PCM drains and the transport remains usable.
- **Upstream election:** two simultaneous identical audio candidates with peer
  probability 241 versus Encore 173 choose the peer; peer 17 versus Encore 173
  choose Encore. The real upstream arbiter decides; the loser receives
  `answered_elsewhere`, emits no reply and retains transport. Equal audio makes
  probability the upstream clarity tie-break, not a cross-device quality claim.
- **Idle announcement:** a typed command enters upstream from a named second
  satellite fixture; Encore receives real `announcement active=true`, chime,
  Piper speech and `active=false`. Both output streams consume and drain
  separately (exactly two end frames and two completions), without a local call.
- **Confirmation:** actual upstream asks “Turn on the lamp?”, signals listen,
  receives a zero-pre-roll `follow_up`, recognizes generated “Yes”, then HA turns
  the helper on. Encore implements no confirmation/assistant logic itself.
- **Timer:** real HA `voice_timer_list.create_timer` with one-second duration
  causes upstream `timer_ringing` and idle playback. Device Stop sends upstream
  `stop_timer`; upstream returns `timer_stopped`; alert and queued PCM clear.

The second native satellite is a clearly named **wire/synthetic-sink fixture**,
not a second device or election stub. Typed `/api/try` only substitutes the
announcement sender's input; the real receiver lifecycle is exercised. Synthetic
PCM, host detection injection and the clocked speaker are explicit device
boundaries. None of these replace the verifier, recognizer, election, HA action,
Piper output or receiver protocol with deterministic service responses.

## Native HA identity, availability and minimal selection surface

Concrete source at the pinned integration:
`custom_components/voice_incubator/satellites.py` explicitly creates an
integration-owned device keyed by `(voice_incubator, canonical_mac)`. It supplies
the network MAC, copies an existing ESPHome device's area on first registration,
then keeps its area independent. Its source says HA no longer merges devices
across integrations. The overview comment in `__init__.py` suggesting a merge is
not the implementation contract.

Observed registry evidence confirms two distinct HA device IDs for the same
MAC: the ESPHome virtual satellite and native Incubator device. The unassigned
native device's only entity was `voice_timer_list.encore_lab_timers`, **not** an
`assist_satellite` entity. Area-assigned satellites can share the area's timer
list rather than having one entity per satellite. Integration registration is
persistent; disconnecting or switching Encore does not set that list unavailable
(observed state remained `0`). The app's own `/api/satellites` reports native
`connected`; that is not a per-satellite HA availability entity.

Minimal compatible proposal (no new HA/control behavior implemented here):

1. Retain a stable native MAC. Keep one Encore device/control identity for shared
   controls, mute, status and backend selection; their availability follows the
   device/control connection, not voice selection. Do not duplicate them or
   create a pretend second ESPHome assistant.
2. Let the parent-owned selector revoke the old voice transport and reconnect
   the chosen one; indicate voice selection/readiness separately. Native
   connection backoff is real, so “selected” must not imply “connected”.
3. Tolerate upstream's separately registered Incubator timer device/list. Do not
   delete/recreate it on switches or mark shared timer state unavailable just
   because one native transport is inactive. Two backend voice entities with
   symmetric availability would require an explicit additional control/integration
   contract; upstream does not expose that surface at this pin.

## Checks, limits and delivery

`make check`, `go test -race -count=30 ./core/... ./backends/...` and 30 repetitions
of the native multi-stream announcement wire regression passed with the exact
parent dependency. Scoped setup warm rerun passed. Regenerating all four PCM
fixtures produced identical SHA-256 values. Installed non-archive model files
were independently hashed against upstream's pinned manifests. The cached lab
token is mode 0600; no credentials/models are in the tracked change.

Still unproven: physical capture/playback, speech accuracy/range, AEC/dual capture,
local wake-model inference or transfer, monitoring/intercom, speaker ID,
embedding matching, scheduled/calendar alarms and empty-text streamed-agent
replies. HA Local Calendar currently lacks `ical` in the foundation environment;
no shared dependency expansion was made for an out-of-scope scheduled-alarm test.
Duration timer behavior is proven; it is not scheduled-alarm proof.

Changes remain local, uncommitted and unpushed for coordinator integration and
fresh user ship approval. Existing top-level foundation lab documentation still
describes the old exploratory state; updating it and shared selector/control
behavior remains parent-owned. No branch, merge, deployment or release was made.
