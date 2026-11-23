# Echo Dot (2nd generation) development package

The Echo Dot (2nd gen) development build uses Linux 6.1.178 and Alpine 3.22
AArch64. Follow the [install guide](INSTALL.md) for an unlocked Dot. The
[packaging notes](packaging/README.md) cover private builds and recovery.
We retired the Linux 3.18/ARMv7 build. This package supports the second-generation
Echo Dot only.

See the [Voice PE comparison](../../docs/voice-experience.md) for voice and
monitoring features by device.

## Internal names

**Biscuit** is Echo Dot (2nd gen); **Radar** is Echo (2nd gen), a separate board
with no runtime/package in this tree. Neither name implies matching routes,
DSP, thermal settings or sensor support. Shared MT8163 ancestry is not board
qualification. User-facing product names stay Echo Dot and Echo.

Source map: `devices/echo-dot-2` → `devices/biscuit` (`package biscuit`),
`platforms/echo` → `platforms/linux` (`package linux`), including `alsa/`,
`input/`, playback, the fixed-format 48 kHz stereo mixer, privacy-gate mechanics,
`LICENSE.techo5` and `NOTICE`. This is a move, not a format-neutral mixer redesign.
Board PCM/channel maps, mixer routes, volume policy, sysfs paths, privacy-driver
selection, key codes and ring wiring remain Biscuit-local. No generic MT8163
API or speculative `devices/radar` package is introduced.

The unpublished target/discovery board ID is `biscuit`, the package format is
`encore-biscuit-arm64-starter-v1`, and default generated names use
`encore-biscuit-…`. The packaged backend is `incubator`.

ESPHome advertises `Encore Echo Dot · 0AB2` as the friendly name, using the last
four uppercase hex digits of the unit's Wi-Fi MAC (here `0AB2`). Home Assistant
uses it as the title for new ESPHome config entries and appends the adapter MAC
to that title for the Bluetooth proxy adapter's device name. Existing ESPHome
entry titles can be renamed in Home Assistant without re-pairing.

ESPHome reports manufacturer `Amazon`, model `Echo Dot (2nd gen)`, and the
booted image's version without the package revision.
The project name `Encore Satellite.Amazon Echo Dot (2nd gen)` makes Home Assistant
display `Amazon Echo Dot (2nd gen)` by `Encore Satellite` in Device info.
The `Firmware update` entity uses the title `Encore Satellite` and reports core
versions without package revisions. The configured per-unit name stays the
hostname, and the real wlan0 MAC and entity object IDs/keys remain unchanged.
Existing Home Assistant user-assigned names are not rewritten; no re-pairing is needed.

Kernel and Sendspin builders, source-kit paths and provenance use Biscuit.
Rebuild old development artifacts: the renamed build inputs intentionally have
new identities, and strict validation rejects stale artifacts instead of
relabeling their records. No compatibility with unpublished old bundles is
promised. Do not rewrite retained provenance, source kits, backups or device data.
The frozen first private candidate remains built from its original commit and
does not need this rename for physical preflight; no device migration is performed.

## Runtime and device behavior

- Static Go Linux AArch64 executable explicitly assembling the shared engine,
  Linux host primitives, Biscuit wiring and the native Incubator backend only.
- Source-backed ALSA capture: card 0/device 24, 16 kHz packed24, nine channels.
  Microphone channels 0–6 are equally averaged into bounded mono PCM16; playback
  references 7/8 are excluded from the average. Signed 24-bit values are summed before one division
  by 7×256, truncating toward zero. This is plain normalized averaging, without
  steering, AEC, additional gain or buffered history; real-array pickup remains
  unqualified.
- Source-backed speaker routing, conservative software volume, 16→48 kHz stereo
  output to card 0/device 23, filtered voice interpolation and source-scoped drain.
- On images with the official Sendspin SDK companion installed, authenticated
  Music Assistant playback uses the same production ALSA output as voice.
  Sendspin is off by default and independent of the Incubator voice backend.
- Authoritative hardware privacy-state reads around acquisition/delivery, buffer
  invalidation on mute, Action start/stop, volume buttons and a full-ring renderer.
  Driver-first mute is not toggled a second time. Never read `power_button_state`.
- Incubator is the Dot's only voice backend, with no runtime selector or fallback.
  The configured name is used for hostname, discovery and backend identity;
  the real wlan0 MAC identifies the device. No lab HTTP controls are packaged.
- Clean application state, first-boot Improv BLE, optional custom name, recovery
  and MISC backups, USB transfer read-back checks and two Encore rootfs slots.
- Experimental signed AArch64 rootfs updates with explicit trial installation;
  no live feed or physical OTA qualification is implied.

**Not implemented by this package:** beamforming, speaker EQ calibration,
Voice PE ring parity, base/kernel OTA migration or a production release service.
Start a voice turn with Action or a microWakeWord model from Incubator. The
runtime includes an intercom route; we haven't tested it on the Dot. We still
need to test the physical controls and measure playback levels. The build uses
a low default volume without speaker calibration.

### Loopback AEC and Incubator monitoring

Capture uses one 1,024-tap (64 ms) linear adaptive filter on the seven-mic
average, using co-captured lane 7. Playback is unchanged. The adapter advertises
in-call `aec` and sends the same processed PCM on the primary (`0x03`) and AEC
(`0x08`) routes, with equal pre-roll lengths and no relative sample lead.
The filter keeps subtracting while listening but freezes learning during tentative
and listening phases; this is not a general double-talk detector. Capture loss,
privacy denial and delivery errors invalidate filter history and weights.

Incubator live listening uses `monitor` (`0x0A`); Enroll Voice and Teach Wake
Word use `monitor_aec` (`0x0C`). Both use the same processed stream, only in
response to an idle-session request from the existing trusted Incubator peer.
The recording ring must be displayed before audio is sent. Monitoring does not
send pre-roll or dispatch wake actions. Live Monitor and Teach Wake Word receive
`WAKE_REPORT` (`0x0B`) about every 100 ms of capture, with per-model 8-bit scores
and detections. Intercom does not receive wake reports. App stop, physical mute,
Action, capture loss, or disconnect ends capture and scoring. The native Incubator
transport is not authenticated or encrypted; run it on a trusted network only.

[Voice PE at `8ba314e`](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/components/voice_incubator/voice_incubator.cpp)
also reports detections without acting on them. Encore resets detector history
when entering or leaving Live Monitor so no monitored speech becomes part of an
idle wake decision, and waits for the local recording indicator before scoring.
`go test ./backends/incubator -run 'TestLiveMonitor|TestMonitorOff'` checks the
wire reports, action suppression, and revocation without hardware.

Intercom uses the same recording indicator and echo-cancelled capture. Like
Voice PE at `8ba314e`, it sends only `0x0A` during relay; `0x0C` remains available
for Live Monitor and training. For prompt connect and end tones, pair with an
Incubator app that marks those tones with its existing `announcement` event.
The `MONITOR` intercom request owns only relayed
speech. Ending the monitor flushes that speech without a playback-drained
acknowledgment; an explicitly marked end tone can play afterward.

Lane mapping, causal delay and tail, reference behavior at playback recovery,
single-core cost, wake/Stop during playback, and the recording indicator are
not qualified on hardware.

## Home Assistant TTS announcements

The encrypted ESPHome endpoint exposes one enabled `Announcements` media-player
entity on the same device/MAC as its other controls. Voice Incubator can select
this as the satellite's own speech target, instead of falling back solely because
no ESPHome media player exists. This does not choose or configure a TTS provider.

The entity advertises only URL playback, announcements and Stop. Home Assistant's
ESPHome ffmpeg proxy converts the selected provider's audio to the advertised
PCM16, 16 kHz mono WAV; Encore streams it in at most 20 ms chunks through the
existing voice mixer, including physical drain. Both fixed-size WAV and ffmpeg's
unknown-length pipe header are supported. There is no local Piper, MP3 decoder,
playlist, pause/resume or separate volume policy; Sendspin music remains separate.
Direct URLs must already contain this WAV format. URLs must use HTTP(S), headers
are bounded to 64 KiB and each response to three minutes; signed URLs and speech
content are not logged. HTTPS uses normal certificate verification.

Only one announcement fetch runs at a time, with one replaceable pending request.
Stop, hardware privacy, owning HA connection loss and shutdown cancel fetching
and flush output. Physical Action also stops an active announcement. Native wire
audio cannot interleave with URL playback. Incubator receives protocol-2 device
events `announcing` after the first accepted PCM and `announced` only after a
successful physical drain. HA receives ordinary playing/idle states, not the
unmapped protocol ANNOUNCING state. Failed or cancelled playback never claims
completion. A source implementation and off-device tests are not evidence that
the selected household TTS provider has played on the Dot; device activation,
HA rediscovery and acoustic acceptance remain separate.

## App-pushed wake models

The Dot advertises `wake_format=mixednet`, six model slots, max-pooling and a
permanently resident spoken Stop model. Voice Incubator lowers microWakeWord
TFLite models into MNB1 blobs and pushes them over protocol 2; the model loader
does not download arbitrary URLs or run a TFLite interpreter. The pure-Go frontend and
executor are ported from pinned Techo5; their licenses/provenance are retained in
`core/wake/mixednet`, `core/wake/mwwfront` and `licenses/`, and included in the
rootfs notices.

Each model is limited to 512 KiB and checked for matching ID, exact size, SHA256,
valid settings and executable graph bounds before a success acknowledgement.
Replacement is by ID, not the wire slot hint, and an invalid replacement keeps
the existing model. Six residents normally allow five wakes plus Stop. Models
are volatile per app connection and are re-pushed after reconnect; no trained
wake model ships in the runtime.

Privacy-gated 16 kHz averaged-microphone PCM feeds a bounded asynchronous detector
worker. Inference never runs under the capture/privacy or session lock. Reported
capture gaps reset detector history; mute/disconnect revoke the connection generation,
clear audio and discard its models. Idle wakes carry their model identity,
0..255 scores and up to 1.5 seconds of pre-roll to upstream verification. Spoken Stop
is armed only during voice output, announcements and ringing alerts; it cancels
those instead of starting a new call. It is not a music transport command.

The protocol/executor and recorded-speech host checks are not Dot CPU-budget,
far-field wake accuracy, echo rejection or physical privacy qualification. The
pre-hardware review's recovery, first-boot health, ALSA, capture-loss and build
provenance findings are corrected in revision 8. Revision 7 includes models but
predates those fixes; revision 6 predates models. No candidate is hardware
qualified or approved to flash. Revision 8 changes the recovery initramfs and
therefore the shared-base identity: it is not an app-slot update from revision 7.

The retired ARMv7 revision 11 introduced transactional Wi-Fi management and the
dual-stack firewall. Its Alpine base changed, so it was not an ordinary app-slot
update from revision 8. New images do not ship its optional SSH service.

## Physical buttons

The Dot 2 mappings follow the pinned Techo5 wiring, not generic PC key guesses:

| Button | Linux key code | Runtime behavior |
|---|---|---|
| + | 115 | Raise the shared physical speaker gain one step immediately; holding repeats every 200 ms. |
| − | 114 | Lower that gain one step immediately; holding repeats every 200 ms down to silence. |
| Action | 138 | Dismiss a ringing timer or stop foreground voice/announcements and local music first. Otherwise pause/resume eligible [Sendspin music](#sendspin-music), or start manual Incubator voice when idle. |
| Mute | 113 | Immediately inhibit capture/activation and silence voice/music, then follow the authoritative hardware privacy state. |

The untuned master range is 0..120 (silence..unity), independently starting at
60 (50%), with gain `step/120` applied to voice and music. The maximum is the
Linux mixer's existing no-additional-master-amplification ceiling, not a measured
speaker safety limit. It clamps the combined voice/music signal to PCM16 before
master attenuation, so unity cannot cause integer wrap, but simultaneous sources
can still saturate even below unity. Startup is not the maximum.
Volume release stops repeating. Repeated/duplicate downs do not retrigger Action
or mute. No multi-assistant long-press selection or factory-reset gesture is added.

Hardware gain is unchanged: `HP Driver Gain Volume=6` (0 dB), matching the pinned
[TECHO5 Dot speaker path](https://github.com/farooqu/techo5/blob/2b091439765d5f881de4b89255b8cf0637d80cba/echod/internal/hardware/speaker/paths_dot.go).
[LibreEcho's Biscuit profile](https://github.com/aslater3/LibreEcho-Platform/blob/37fc39aa6092491bcb5f12b77a8151179ffd122b/tools/mt8163-arm32/libreecho_platform_targets.py)
also selects raw 6, with its own DSP, pre-gain and compressor/limiters.
[Kiosk Satellite's Dot HAL](https://github.com/jxlarrea/lineageos-echo-kiosk-satellite/blob/229f994624a4cb91809ded1f0720cfcfdc8673d9/sources/device/amazon/biscuit/audio/audio_hw.c)
instead defaults to +3 dB (raw 9), chosen by ear, and reports distortion at +6 dB.
Those processing chains and subjective reports do not qualify Encore's untuned
output. No analog boost, vendor DSP or speaker protection is added here; 100%
means native software unity, not Amazon-equivalent loudness or acoustic safety.

The existing encrypted ESPHome endpoint exposes **Speaker volume**
(`speaker_volume`, Number) and **Speaker mute** (`speaker_mute`, Switch).
Volume is actual master software-gain percent, 0..100%, not a rescaled partial range
or a Sendspin-only setting. Requests round to the physical keys' 1/120 gain
steps (about 0.8333 percentage points); nonfinite/out-of-range values are rejected.
State subscriptions report the applied level and receive physical-key updates.
This master gain applies to both voice and music; Sendspin's per-stream gain
and voice ducking remain additional music-only factors.

Speaker mute silences both sources while remembering the selected level.
Changing the Number keeps that mute; either physical volume key adjusts the
same level and releases speaker mute. Neither control changes microphone
privacy or releases the music Stop/privacy latch. A level of zero is silence
independently of the mute switch. Level/mute are session-only (no flash writes):
restart selects 50% and speaker-unmuted, without bypassing microphone privacy.
The 50% startup is selected for the next supervised trial, not an acoustic safety
qualification. On-device HA discovery, key/state synchronization, privacy and
audibility still need a separately approved hardware trial. This feature does
not diagnose or fix playback underflows and claims no other board support.

Volume changes from either physical keys (including held repeats) or HA show a
steady white arc over a dim blue track for 1.2 seconds after the latest change.
The arc represents **actual master software gain**: full ring means 120 steps
(100%, unity); 60 steps (50%) fills half the ring. Zero/output mute shows an empty dim
track; startup does not flash a volume indication. Brightness/night limits still
apply. Privacy, alerts, unavailable status and Stop take priority, and feedback
hidden by those states is discarded rather than replayed afterward.

Only a fresh physical volume-key press requests a short (40 ms, gently ramped)
660 Hz cue at the newly selected master gain. Holds, kernel autorepeat,
duplicate key-downs and HA commands never replay it. Admission is best-effort:
voice activity/queued voice, any music stream, privacy, Stop, output failure or
busy mixer locks skip the cue instead of interrupting playback or waiting on
hardware. The reusable Linux mixer accepts bounded PCM cues only; the tone,
press policy and ring behavior live in Biscuit. A newly arriving voice can
follow an admitted cue; the queue does not promise preemption. These are host-
tested software semantics, not verification of physical brightness or acoustics.

The kernel owns the physical mute latch and button light. Encore never toggles
it again on receiving the event; it allows 500 ms for the driver's delayed state
report and fails closed on an unreadable privacy state. Capture rechecks that
state, and unmute does not resume stopped music. The ring reflects privacy with
priority over voice/timers. No code reads the unsafe `power_button_state` node.

Home Assistant can observe **Microphone muted** (`microphone_muted`), a read-only
ESPHome binary sensor. It is on whenever the privacy gate blocks capture,
including while the hardware state settles after a press or cannot be read.
It does not control or mirror **Speaker mute**.

Evdev-record tests through the real input reader cover taps/holds/releases,
volume limits, Action start/cancel/timer Stop, duplicate downs, muted Action and
driver-lag/privacy behavior. The event node, audio hardware and voice peer are
virtual in those tests; all four physical keys still require a Dot acceptance run.

## Full-ring presentation

The Incubator-only renderer uses all 12 RGB segments during active presentation:
mint follows actual gated mono microphone energy during accepted/local listening;
tentative wake is dimmer and steady. Processing has a blue rotating intensity
crest with a bounded secondary server-speaker RGB accent. Queued response text
is steady lilac; only successful local playback and its drain pulse together.
The pulse indicates playback ownership, not measured speaker amplitude.
Alerts swell more slowly in amber. Privacy/fail-closed input suppression is
steady red and wins over every other state. Healthy ready idle is off.

Disconnected, failed output, missing/stale capture, errors and idle without an
armed ordinary wake model use steady amber, not an invented readiness or network
diagnosis. Quiet wake arbitration rejection and the cancellation fence suppress
old animations. Connection/capture loss still wins over that quiet fence.
Missing capture is distinct from fresh silent PCM: the visual observer expires
after 250 ms without a delivered block. This is a conservative presentation
freshness guard, not a new microphone or network watchdog. It changes no audio
policy. Only an atomic scalar/time pair is shared with the renderer; no samples,
sound direction, extra recorder or extra loss-counter reader are added.

Encrypted ESPHome controls expose **Ring brightness** (`ring_brightness`, integer
1..100%) and **Ring reduced motion** (`ring_reduced_motion`). They persist before
applying in private `/data/encore/ring.json`, separately from strict `config.json`
so older good-slot binaries can ignore them safely. Missing policy defaults to
100% daytime brightness and ordinary motion. Invalid policy fails startup;
an invalid command or failed save leaves live settings unchanged.

Daytime brightness follows the stock Biscuit Fire OS 6.5.7.4 NS6574/7623
`rgbTable.default` user indices 1..100: quarter/third/half/full resistor-limited
current in bands 1..25/26..50/51..75/76..100, with the stock PWM multipliers.
At 100%, `led_current=0` and a full-intensity channel reaches PWM 223
(`floor(255 * 0.875)`), not the unused extra table entry 101's PWM 255.
The numeric curve was checked against the owner-held OTA (SHA-256
`64ab6d2dd85f8093abdd62c275d229c7e9fdd68e4d46892b48bdbd1d100d46d8`),
config SHA-256 `95272a4a7e67ec468e7a2f34a4a8d71ceb136d3ed82becda723df65974062d4a`;
no proprietary firmware/config files are distributed here.

This matches the stock **brightness drive curve**, not Amazon's animations,
perceived luminance, or thermal protection. Existing color ratios, squared
envelopes and the 1.5-channel segment budget remain; volume white rounds each
channel to half the PWM ceiling. Confirmed night **on** still uses quarter
current and caps channels at 8. Unknown/missing night state instead caps the
selected brightness at **50% of the stock curve** (current selector 2, PWM 192
at that endpoint), while respecting lower user settings. Both confirmed night
and unknown stop decorative motion; reduced motion does so independently.
Brightness cannot black out privacy: red retains a floor of 8. Changing current
blanks the old frame before applying the new drive to avoid a transient flash;
failed writes propagate without marking the requested output applied.
Stock LED thermal clamping is **not reproduced**; board current/temperature and
comfort remain unqualified, and this local change requires a separately approved
hardware trial before activation. Real-input energy can still change the mint
ring under reduced motion. The 50 ms frame tick,
2.4 s rotation, 1.8 s output pulse, 3.2 s alert swell, scalar easing and microphone
visual gain are initial software tuning, not hardware-qualified values.

One frame writer coalesces unchanged driver payloads and recomputes the current
state rather than resuming a saved sequence. Provisioning/update/music status
animations and other Echo models are not implemented here. Independent music,
capture processing, release scope and audio cancellation policy are unchanged.
Fake-PCM/clock/sink and encrypted-control tests plus cross-builds validate
software behavior only; actual LED order, diffuser color, brightness/night comfort,
capture-to-light latency and resource/audio continuity still require an authorized
physical-device run.

## Bluetooth provisioning and optional proxy

Bluetooth is a **required base capability**, independent of the optional proxy.
The runtime offers first-boot Improv BLE only if Encore has no Wi-Fi configuration.
Press Action to authorize a session, then use an Improv-compatible Web Bluetooth
client or Home Assistant's Improv discovery. The cleartext Improv service is
bounded to five-minute sessions and closes after successful setup; credentials
are saved only after association and IPv4 acquisition. A normal network outage
does not reopen setup. Only WPA2-Personal is currently accepted by either setup path.

The encrypted ESPHome common-control endpoint on 6053 stays available independently
of Incubator voice on 6055. It does not advertise a second Assist voice satellite.
It exposes one persistent `Bluetooth proxy` switch, **ON for new installs**. When
ON, HA can scan, make active GATT connections and request supported Just Works bonding.
There is no separate connection/pairing toggle and no invented PIN/numeric-comparison
confirmation. Improv works while the proxy is off, before voice/proxy workers start.
BlueZ bonds and the stable controller identity live under `/data/encore`, outside
both application slots. A required BLE startup probe runs even with proxy OFF;
a missing/broken BLE base must not be committed as a healthy voice-only update.

This is implemented software, **not a successful hardware qualification**. Direct
`/dev/stpbt` scanning alone is insufficient for GATT/Improv. The packaged BlueZ,
D-Bus and bridge require actual Bluetooth/VHCI kernel interfaces and a working
controller. See [Bluetooth implementation and evidence](../../docs/bluetooth-linux.md).

## Linux Echo maintenance

These controls are assembled only by `devices/biscuit`, on its **encrypted
ESPHome endpoint**. They are not shared voice-backend entities and are not
included in Android/APK packages. No plaintext management endpoint is enabled.
Possession of the device's Noise key grants maintenance access; treat it as an
administrator credential. Use physical USB for shell recovery.

### Transactional Wi-Fi changes

Actions `wifi_status`, `wifi_stage(ssid,password)`, `wifi_confirm(id)` and
`wifi_cancel(id)` are available through the same encrypted endpoint. There is no
readable Wi-Fi password entity. Keep passwords out of HA automation definitions,
service-call histories and traces; transport encryption cannot remove copies
retained by the controller.

Stage returns a transaction ID before switching networks. The independent
network worker allows two seconds for the response, then has 80 seconds to join.
Reconnect on the candidate network, inspect `wifi_status`, and confirm that ID
within 60 seconds of reaching `awaiting_confirmation`. Confirmation rechecks the
association/address before saving. Failure, timeout, cancellation or worker
restart restores the saved last-good configuration; no persistent credentials
change before confirmation. A voice-runtime restart does not cancel this worker.
The configured hostname remains unchanged. The encrypted endpoint and Incubator
advertisements restart when the IPv4 address changes. USB remains the fallback
if neither network is reachable; rollback cannot make an unavailable old access
point work. Hardware association, DHCP and reconnect timing remain unqualified.

### Firewall policy

Boot installs both IPv4 and IPv6 legacy filter tables **before enabling radios
or networking**. Installation failure prevents base readiness. INPUT and FORWARD
default to DROP; OUTPUT is allowed. The package owns these device filter tables.

| Inbound service | Policy |
|---|---|
| TCP 6053, ESPHome controls/Bluetooth proxy | Wi-Fi only; controls require the nonzero per-device Noise key. Unpaired starter builds listen only during the Action-authorized [first-pair window](packaging/README.md#add-the-esphome-controls-to-home-assistant). |
| TCP 6055, native Voice Incubator protocol 2 | Listens only after ESPHome pairing, on Wi-Fi. **Unauthenticated**; use only on a trusted LAN/VLAN, never port-forward. |
| UDP 5353 | mDNS multicast destinations and unicast replies on Wi-Fi. |
| DHCP and selected ICMP/IPv6 discovery | Allowed on Wi-Fi for address configuration, diagnostics and network operation. |
| Sendspin | Outbound authenticated connection only; optional discovery uses the existing Wi-Fi mDNS rule. |

Loopback and established/related replies are allowed. Unlisted inbound ports,
including TCP 22, remain closed. Physical USB serial is a separate root console,
not a firewall exception. Host packet tests do not qualify the Dot's vendor kernel.

## Sendspin music

On ARM64 images that include `/usr/local/bin/encore-sendspin`, the encrypted
ESPHome endpoint exposes **Sendspin music**, **Resume
music**, **Stop music**, **Pause music**, **Next track** and **Previous track**.
There is no duplicate music media-player entity: use Music Assistant and its
Home Assistant integration for music queues, grouping and ongoing playback.
Music being off or the server being absent does not stop voice, BLE or first-boot
Improv.

1. Enable the Sendspin provider on a Music Assistant version that supports
   encrypted Sendspin and the device pairing-token setup. Leave **Sendspin
   music** off on Biscuit. On the physical USB console, privately run
   `encore-sendspin pairing-token --state /data/encore/sendspin`.
2. Enable **Sendspin music** on Biscuit. It automatically discovers the
   Sendspin provider on Wi-Fi. Plaintext and guest playback are not allowed.
3. When the receiver appears in Music Assistant's player settings, open its
   setup and paste the token from step 1 into the pairing-token prompt. After
   pairing completes, press **Resume music** on Biscuit, then choose it as the
   player in Music Assistant to start a queue.

Biscuit browses `_sendspin-server._tcp.local.` on Wi-Fi for the
advertised IPv4 address, SRV port and `path=/sendspin`. It connects only when it
sees one compatible server. If it doesn't connect, check that multicast is
available between Biscuit and Music Assistant and that only one server is
advertising. Discovery finds the address without granting trust. Biscuit does
not advertise `_sendspin._tcp.local.` or open an inbound player port.

Do not publish the token or put it in logs. Turn the
music switch off before reading the token or running
`encore-sendspin revoke-all --state /data/encore/sendspin`; maintenance needs the
same exclusive lock as the player. Revocation retains the receiver identity,
removes all trusted servers, rotates the token and requires pairing again. Trust
survives rootfs-slot updates in the private `/data/encore/sendspin` directory.
Corrupt state fails closed. Losing that directory loses the receiver identity.

The companion is a separate C++20 process connected only by an inherited,
versioned Unix packet socket. No HTTP PCM sink or new network control listener
is installed. It negotiates PCM16/48 kHz/stereo; the Go mixer bounds music to
100 ms plus a 40 ms DMA lead and bounded IPC, and reports frames actually
consumed according to ALSA delay and the SDK's raw monotonic clock. Voice uses
separate queues and a filtered 16→48 kHz converter; voice Drain waits only for
voice. The driver cannot atomically stop and snapshot DMA. A cancellation that
drops shared DMA discards uncertain frames and fences affected voice/music;
music reconnects with a fresh epoch rather than replaying potentially heard
samples or claiming they were consumed. Underruns similarly reset the output
clock without terminating the runtime. Ordinary volume changes do not drop DMA
and apply to newly mixed frames.
Aggregate engine activity ducks **music only** to current user gain × 0.2 with
a 20 ms ramp. Countdown alone does not duck; ringing timers do. Physical volume
still applies the conservative Dot master limit to the combined, pre-limited
signal, including simultaneous sources and filter overshoot. This route is
not a claim that the Dot's single speaker is physically stereo.

Privacy and **Stop music** silence local music even when no server command can
be sent. Enabling, changing servers or losing the ready connection also latches
local audio off. Biscuit reconnects in the background but does not replay old
commands or clear that latch. Once the authenticated player is ready and hardware
privacy is clear, press **Resume music** again. If a group is already playing,
this can restore local sound even when the server does not offer a Play command.
Pause, Next and Previous act on the Music Assistant group only when the server
advertises them; Stop sends a group command on a best-effort basis in addition to
always stopping the speaker. No ESPHome entity or setting is added for Action.

When nothing in the foreground needs dismissal and hardware privacy is clear,
Action pauses a known-playing Sendspin group. If the group is playing but the
local speaker was stopped, Action resumes just the speaker instead, even if the
server does not advertise Play. An Action-paused group can be resumed with the
next press. These are best-effort group commands: if the required command is
unavailable, the press does nothing rather than opening the microphone.

The pinned Sendspin SDK reports Playing and Stopped, not a distinct Paused state,
and Music Assistant reports Stopped on pause. Biscuit therefore offers Action
resume only if it observes the same authenticated group go from Playing to
Stopped within five seconds of its own Action Pause. This intent is not saved;
regrouping, disconnect, new playback or another explicit transport control clears
it. The wire does not acknowledge Pause, so an unrelated stop in that short
window is indistinguishable. Cold idle, externally paused, disabled, unknown
or disconnected music falls back to the existing voice action. Use Music
Assistant or **Resume music** to resume an externally paused group.

Automatic room targeting, “play here” intents, wake-over-music and music AEC are
not implemented. Physical presentation latency, acoustic quality, power, CPU/RAM,
underruns, Wi-Fi/BLE coexistence and timer recovery remain Dot tests.

## Build, fresh install and updates

Use the [AArch64 build and install guide](packaging/README.md) for
current inputs, recovery preflight, fresh enrollment, persistent boot routing and
rollback. Compatible signed runtime updates stage an inactive Encore rootfs slot;
kernel and recovery changes are separate. Neither path writes the stock system
slots. Check the guide for the exact starter's limits before acting on a device.

The old Linux 3.18/ARMv7 kernel, fresh-install bundle and release feed have been
retired. Historical recipes and released bytes, if retained, are evidence only;
do not use them to install or update an AArch64 device.

## Provenance and qualification

Hardware evidence: [Techo5 `2b091439`](https://github.com/farooqu/techo5/tree/2b091439765d5f881de4b89255b8cf0637d80cba/echod/internal/hardware)
and [Techo5 Dot `b2a50c74`](https://github.com/farooqu/techo5-dot/tree/b2a50c74bbe8b34785748574aa39fbd045fc4e46).
Later physical Techo5 testing on the owner's Dot established boot of the `.4`
patched kernel, Wi-Fi, Incubator microphone/reply flow, Action and mute behavior.
That removes the need to rediscover the basic hardware wiring, but does not
qualify Encore's different ALSA/control implementation, userspace or boot scripts.
Phone Improv, remote Bluetooth GATT/bonds and power-loss rollback were not covered.
ALSA/evdev and wmtup reuse retains MIT notices; full historical notices are not
inventories of this package. Alpine packages retain their own licenses, including
GPL BusyBox; source/dependency/license audit is still required before distribution.
See [ACKNOWLEDGEMENTS](../../ACKNOWLEDGEMENTS.md). Encore-owned code uses the
[MIT License](../../LICENSE); third-party components retain their own terms.

Before support: record the exact unit/kernel/base/backend versions and test boot,
Wi-Fi/BLE/Improv, full proxy toggle and pairing, capture/playback and drain, physical
mute during queued audio, Action/volume/ring, Incubator voice/timers, reconnect, resource
use and interrupted update/fallback/restoration. No physical device was flashed
or tested by the host-side checks used to develop this package.
