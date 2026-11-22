# Real upstream Voice Incubator validation

Verified on the local Linux x86-64 stack with the **actual upstream app, wake
verifier, speech recognizer, HA integration/intents and Piper**, through Encore's
native TCP adapter. Synthetic capture, injected host wake detections and a
clocked virtual speaker replace device boundaries only. No physical device,
acoustic quality, household deployment or release claim is made.

This page records historical runs, including the removed Assist lab and backend
selector. For current setup and the Incubator-only `make lab-test` entry point,
see the [native lab guide](../lab/incubator/README.md).

## Independent commit-review follow-up

The three additional review findings are fixed. Native mute now sends cancellation
before retiring its voice connection, and neither ordinary nor cancellation
`AUDIO_OUT_END` releases admission on that retiring generation. Interrupted output
from Stop is also retired when mute follows, including delayed announcement drain
cleanup. The two public-engine regressions fail before the fixes and pass after;
the native TCP test checks CALL_END before EOF, retained mute on reconnect and
exact fresh playback. Race-enabled session/native/outbox tests pass 30 repetitions.

The lab fences HTTP admission and command mutations at retirement, closes active
connections when graceful shutdown times out, and joins admitted handlers before
replacement. A real HTTP `Expect: 100-continue` request proves Decode is waiting
for a body across real HA Apply. Before the fix it returns 200 after replacement
and changes persisted mute while the new engine stays unmuted. Afterward it cannot
succeed or mutate shared settings; a fresh request mutes the live engine normally.

The full pinned Incubator/model/HA suite passes **16/16** with this policy: real
CALL_END precedes native-only reconnect, mute remains set, fresh recognized HA
intent/reply works, and HA receives BLE advertisements without scan restart.
Idle mute/unmute does not reconnect. Both real-HA Assist/selector suites and the
separate encrypted virtual BLE/GATT fixture suite pass. Evidence is under
`.amp/in/orb-backup/cancellation-fix/` and `.amp/in/orb-backup/http-teardown/`.
The controlled buffered-tail and Stop/drain interleavings are component proof,
not newly executed real-service races. Earlier same-native-connection results
below predate this stricter generation policy and are historical.

## Production mute fixes and lab-only switching are separate

The shared mute/cancellation/reconnect fixes are independent of the lab selector.
Their exact commit snapshot passes `make check` and the real HA Assist suite with
the original lab host, without any new selector, settings or BLE composition.

Only the virtual lab links both voice adapters. Its selector controls live in
`cmd/encore-lab/controls.go`, not an importable shared backend package. Production
packages link one voice adapter, Incubator by default; changing backend requires
deliberate application-package replacement. The earlier switching experiments
below are lab coverage, not a production feature or package release requirement.
`make lab-test` runs Assist; `make lab-test-modes` explicitly runs selector tests.
The effective-mute persistence latch stays with those lab settings and their
failed-save/Apply regression, rather than adding a persistence API to the core.

## Four pre-landing review findings fixed

The post-review native suite passed **16/16 cases** against the same pinned,
unmodified Incubator app/models and real HA. In addition to the earlier coverage:

- Idle mute/unmute now precedes the real announcement and HA duration timer.
  Chime and Piper consume their exact wire PCM and both drain normally.
- Active announcement mute closes only the native voice link. The app retains
  its announcement task even across reconnect, so local call output also waits
  for upstream `listening`, not just a local start. A transparent fault fixture
  holds the next CALL_START until the real 30-second chime drain timeout causes
  old Piper PCM on the new socket. None of it plays or claims drain. Releasing
  CALL_START then permits a recognized HA lamp action/reply without another reset.
  HA receives changing BLE advertisements with unchanged scan counters throughout.
- Failed mute persistence now retains a separate effective privacy latch; later
  mode/BLE saves include it and Apply initializes from it. A failed unmute save
  stays muted. The live-host obstruction probe changed from `live_muted=false`
  after Apply to `live_muted=true` using the same procedure.
- Assist mute purges queued capture immediately while preserving queued starts
  and controls; wire abort still waits for RUN_START. The delayed-writer public
  engine/outbox regression failed before the fix and passes after it. This queue
  timing is deterministic component coverage, not a claimed HA wire replay.

The idle-announcement and native-reset regressions also failed before their fixes.
A deliberate control removing only the native listening gate fails with
`played before upstream accepted the new CALL_START`. The first real-service
announcement fault attempt did not meet the race's precondition: earlier forced
disconnects had raised app backoff to 40 seconds, beyond its 30-second chime wait.
That run is retained as failed, not counted as a pass. Waiting for a real stable
connection before forcing loss resets backoff; the strict old-PCM case then passes.

Before the commit split, `make check`, 30 repetitions of the affected race-enabled
Go packages, `make lab-test` (then running both Assist and selector suites) and
`lab/test_bluetooth.py` passed. The HA selector suite now obstructs the real mute settings rename,
recovers storage and uses HA Apply to verify the privacy latch is retained and
persisted. Assist still cancels safely at startup, capture, delayed recognition,
held playback and drain, with fresh intent/PCM-checked turns after each case.
Optional Incubator was stopped; normal HA/Encore remain in Assist, unmuted, BLE on.

Evidence is under `.amp/in/orb-backup/review/`. Physical capture/privacy, RF/GATT,
device packaging, power-loss persistence and Sendspin remain unqualified. The
earlier 15-case runs below are historical and predate these four fixes.

## Restart-bound selector and mute revalidation

On November 16, 2026, reran the full pinned stack against the local selector/mute
changes on BLE foundation `8215e7a27b03102850178e9f4da6370577d8b9b9`.
The expanded `lab/incubator/test_native.py` passed all **15 cases twice** after
restarting the actual Incubator service between runs. No further production
engine/adapter changes were needed. The earlier 13-case suite also passed before
adding real native mute/reconnect and HA BLE observations.

The suite now uses HA's actual select/button services over the encrypted
ESPHome connection, not a live backend command or a native protocol-peer stand-in.
It verifies that saving leaves the running route and BLE scan unchanged, Apply
replaces the runtime in both directions, common registry identities and the BLE
choice persist, and actual upstream native connection/voice recovers. The native
HA timer remains persistent while the route is inactive, as before.

Real Kaldi accepted the supplied wake audio, CTC/TDT recognized the lamp command,
HA executed `HassTurnOn`, and real Piper output was consumed/drained by the virtual
speaker. The first expanded run consumed 18,390 reply samples; the repeat consumed
17,461. Each count was checked against that run's actual wire PCM, not an assumed
fixed synthesis length. Both upstream WAV-prefix checks retained SHA-256
`a7be072bea88d3c3e6ce0cb0ff229cdf4c2a840ac535ec3c91968452182119dc`.

New mute cases cancelled held real Piper output with the actual
`CALL_END(cancelled)`/`AUDIO_OUT_END` exchange. No discarded audio played, no false
drain or new HELLO occurred, and a fresh recognized HA turn succeeded on that same
transport. Deliberately dropping native transport while muted produced a new
real HELLO without unmuting; new starts were rejected. HA continued receiving
changing virtual advertisements during mute/reconnect with unchanged radio
start/stop counters. Deliberate mode restart reconnects BLE; this does not prove
uninterrupted physical GATT.

The first invocation exposed a harness-only dependency error:
`ModuleNotFoundError: No module named 'aioesphomeapi'`, because the new lifecycle
helper imported the Assist suite into the separately locked native environment.
Replacing that import with HA-driven selection fixed the coupling without
expanding dependencies or changing upstream source. The app's cold startup was
also awaited using `/api/settings` before the valid runs.

Final `make check` and `make lab-test` passed after stopping optional Incubator;
the normal HA/Encore services were restored in Assist, unmuted, with BLE enabled.
Captured native logs, setup log, source hashes and final baseline logs are under
`.amp/in/orb-backup/incubator-selector/`. No production HA, package, commit, push
or deployment was involved. The earlier sections below retain historical proof.

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

Changes remain local and unpushed, separated into shared mute and lab-only
commits. The current selector/common controls and revalidation are described above
and in the foundation lab proof; the earlier baseline comparisons remain
historical evidence. No branch, merge, deployment or release was made.
