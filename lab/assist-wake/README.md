# Experimental Assist wake-audio lab

This opt-in lab proves the real Home Assistant → upstream Voice Support Kaldi
verification path using injected detection evidence and synthesized audio.
It does not execute a satellite wake model or validate hardware. Ordinary Encore
still advertises exactly feature bits **7**. See
[results and compatibility limits](../../docs/assist-wake-validation.md).

## Pinned services

- Foundation HA `2026.12.0.dev0`, source
  `1395798e2338bd46ee522539f7281b336c44e3b8`, aioesphomeapi `46.30.0`;
  foundation `lab/uv.lock` and Python `3.14.7`. No HA protocol source patches.
- [OHF-Voice/app-voice](https://github.com/OHF-Voice/app-voice) at
  `b9461db9e6c5f19d30a9dc9563f93d2bc572c3c8`, Voice Support API 1. Its separate
  Python `3.13.5` environment is locked here; it does not alter foundation deps.
  Use upstream's supported `--no-ctc` Kaldi-only mode; no Parakeet/CTC claim.
- Kaldi tools/model archives from rhasspy/rhasspy-speech asset revision
  `9e6bcf3af07909f39f71674f62372508d00dd4a9`. `setup` verifies SHA-256 before
  extracting either archive. Upstream source, assets, compiled decoder and
  generated audio stay under ignored `.amp/in/orb-backup/`.

## Run only in an isolated Linux x86-64 orb

First complete the [foundation setup/provisioning](../README.md). Use its private
HA state, credentials and Noise PSK consistently; set `ENCORE_LAB_STATE` for every
process if overriding it. Never point these tests at a household HA: they replace
the lab Voice Support entry, change Labs, run helper intents and restart services.

From the repository root, with foundation HA and Encore running:

```sh
lab/assist-wake/setup
amp orb service start assist-wake-verifier \
  --command 'lab/assist-wake/run-verifier' --port 10900
amp orb service start assist-wake-satellite \
  --command 'ENCORE_WAKE_PHRASE="okay nabu" go run -race ./lab/assist-wake/satellite' \
  --port 8100
uv run --project lab --script lab/assist-wake/test.py
uv run --project lab --script lab/assist-wake/test_peers.py
uv run --project lab --script lab/assist-wake/test_failures.py
uv run --project lab --script lab/assist-wake/test.py
make check
uv run --project lab --script lab/test_assist.py
```

Wait for the verifier's `/info` on loopback port 10900 before starting the tests;
initial graph compilation can take longer than a warm start. Use
`amp orb service logs assist-wake-verifier` to inspect readiness. Stop the verifier
before rerunning `setup`, which rebuilds its executable. Scripts start only
supervised services. No portal, shared service manifest or CI change is required.
The foundation's alternate supervised HA invocation is
`amp orb service start assist-wake-ha --command 'lab/run-ha' --port 8123` if its
portal setup is unavailable; do not run two services on that port.

`setup` needs sudo, a C++ compiler, git, curl, uv and foundation ffmpeg. It installs
OpenBLAS development libraries and eSpeak NG. The executed fixture used eSpeak NG
`1.52.0+dfsg-5` and ffmpeg `7:7.1.5-0+deb13u1`; neither OS package is a new
runtime dependency of Encore. Network downloads can be large.

The host binds encrypted ESPHome to loopback 6056 and injection control to 8100.
Its separate **lab-only** identity keeps the baseline fixture independent; it is
not a proposal for duplicate product identities or common control entities.
`ENCORE_WAKE_PHRASE` explicitly supplies `fixture_model`'s phrase. Without it the
same executable advertises only bits 7. The endpoint accepts capture/wake/start/
end/stop/hold_playback; it is test instrumentation, not a production control API.

The real test pairs the wake host through HA's ESPHome config flow, enables
`voice_support.next_generation_voice`, and configures the actual upstream app
through HA's Voice Support flow. This HA revision initially fetches satellite
configuration only for ANNOUNCE devices. We do **not** advertise ANNOUNCE. Instead
the test uses HA's public `assist_satellite/set_wake_words` operation to honestly
deactivate all words (which loads configuration), then activate `fixture_model`.
This is required again after TCP reconnect; live resubscription retains it.

## What each test establishes

- `test.py`: real HA plus real app-voice Kaldi, independent upstream verdicts,
  exact post-alignment STT bytes/hash, real intent, virtual playback/drain,
  rejection, missing verifier, Labs alone, empty/unknown evidence, cancellation,
  reconnect, live disable/enable and held-TTS revocation/recovery.
- `test_peers.py`: explicitly synthetic encrypted aioesphomeapi peers, exact
  bits 7/135, denied unsupported subscription, omitted versus negotiated wire
  fields, asymmetric PCM, omitted incompatible scores/channel 2, rejection and
  malformed six-byte evidence. Not a second real-HA integration proof.
- `test_failures.py`: real HA and real upstream unsupported-phrase negotiation;
  a **fake** API-1 verifier at loopback 10901 for timeout, malformed verdict,
  disconnect, pending-run cancellation and live renegotiation. Confirms actual HA
  fails open on verifier faults; these are **not accepted verification**. Observes
  HA Assist entity availability after stopping the actual satellite listener.
  Restores the real verifier entry and configured satellite on exit.

Run suites serially. They share one private HA and explicitly restart the wake
host. The peer suite temporarily disables only its HA entry so fixture peers do
not compete with HA. The real suite must run once first to create that entry.
Expected EOF/wrong-key messages are denial evidence, not successful sessions.

## Notices and remaining boundaries

Existing [acknowledgements](../../ACKNOWLEDGEMENTS.md) and included MIT notices
are unchanged. app-voice is downloaded separately with its Apache-2.0 license;
Kaldi/models, OpenBLAS, eSpeak NG, ffmpeg and Python packages retain their own
upstream terms and notices. Nothing from those downloads is copied into Encore's
runtime or a distributable package. New Encore code's license remains unselected;
a complete model/asset/dependency audit is still required before distribution.

No physical capture/playback, acoustic quality, AEC, second audio stream, trained
satellite wake execution, optional CTC, arbitration, announcements, timers,
device packaging, native Incubator service or HA common-control selection is
implemented or established by this lab.
