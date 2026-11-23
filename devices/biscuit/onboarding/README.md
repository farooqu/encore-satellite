# Biscuit setup prompts

The runtime embeds only these eight pre-rendered WAVs (PCM16 mono, 16 kHz).
It does not embed or fetch a model, voice weights, or a TTS runtime.

The approved review render (24 kHz, SHA-256
`cdaaad46d6681e9d96e56aacd720c6518cbe2812ad295a716e352fd78a6e358b`)
was synthesized locally using Kokoro 0.9.4 with `am_puck`, speed 0.97. The
individual clips were cut in the silence between phrases, then resampled.

| Clip | Spoken text |
| --- | --- |
| `improv-ready` | Ready for Wi-Fi setup. Press the Action button to begin. |
| `wifi-connected` | Wi-Fi is connected. |
| `ha-ready` | Ready to add to Home Assistant. Press the Action button to start pairing. |
| `pairing-open` | Pairing is open. Add this device in Home Assistant. |
| `pairing-complete` | Pairing complete. Encore is ready. |
| `wifi-failed` | Could not connect to Wi-Fi. Check your Wi-Fi settings and try again. |
| `app-failed` | Wi-Fi is connected, but Encore could not start. |
| `pairing-timeout` | Pairing timed out. Press the Action button to try again. |

## Source and redistribution

The exact inputs were from `hexgrad/Kokoro-82M` revision
`f3ff3571791e39611d31c381e3a41a3af07b4987`:

- `kokoro-v1_0.pth` SHA-256 `496dba118d1a58f5f3db2efc88dbdc216e0483fc89fe6e47ee1f2c53f18ad1e4`
- `voices/am_puck.pt` SHA-256 `dd1d8973f4ce4b7d8ae407c77a435f485dabc052081b80ea75c4f30b84f36223`

The pinned [model card](https://huggingface.co/hexgrad/Kokoro-82M/blob/f3ff3571791e39611d31c381e3a41a3af07b4987/README.md)
identifies the weights as Apache-2.0 and welcomes production use. The pinned
[voice list](https://huggingface.co/hexgrad/Kokoro-82M/blob/f3ff3571791e39611d31c381e3a41a3af07b4987/VOICES.md)
lists `am_puck` without a separate restriction. Neither states explicit terms
for redistributing these rendered WAVs. The project owner approved including
the WAVs in the public Biscuit image with that limitation disclosed; this is
not an assertion that upstream licensed the WAVs under Apache-2.0.

## Setup behavior and hardware qualification

Amber means ready for Improv. A brief green sweep acknowledges a leased,
persisted Wi-Fi connection, not readiness for Home Assistant. White means a
fresh Action press can open HA pairing; the white countdown starts only when
the bootstrap API is listening and advertised. Completion requires the saved
key, the advertised encrypted API, and normal runtime readiness. A short red
half-ring indicates failure; steady full-ring mic mute takes precedence even
when the privacy state is unreadable. Setup speech never opens capture. The
completion clip uses the existing session announcement lane and may be
suppressed by mic mute or other output ownership; an already paired boot does
not replay onboarding. Feedback is best effort and newer prompts can interrupt
stale ones.

The [Voice PE firmware at `8ba314e`](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/devices/voice-pe.yaml#L807-L849)
puts Improv twinkle ahead of its other LED states and changes its startup
color when Wi-Fi connects. Biscuit keeps these milestones distinct too, but
gives steady mute priority over setup effects: its short red fault indication
must not be mistaken for, or obscure, the hardware mute indication. Biscuit's
Action-gated ESPHome bootstrap is separate from Incubator voice readiness.
This is a firmware source comparison, not an observation on a Voice PE device.

On a clean install of the signed seed, keep the Improv client connected after
it reads Success/Result. Check the Wi-Fi cue, Dot-initiated disconnect, and HA
ready cue on that same boot. Verify no bootstrap listener before a new Action
press, then check HA discovery, encrypted reconnect, and the completion cue.
Exercise bad Wi-Fi credentials and a pairing timeout/retry; check the red
half-ring when unmuted and steady full red throughout setup when muted. Also
reboot with the saved credentials and confirm no unsolicited prompts.
