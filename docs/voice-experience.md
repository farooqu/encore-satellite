# How Encore works with Voice Incubator

Encore uses the native Incubator protocol for voice. Devices with ESPHome
controls use a separate encrypted connection for those controls. The voice
connection has no encryption or authentication. Keep it on a trusted LAN and
don't forward its port.

## A turn from wake to reply

1. Incubator sends compatible microWakeWord models in MixedNet format. Encore
   runs them on the device without sending idle mic audio to Incubator. Live
   Monitor streams audio on request.
2. On a wake, Encore sends detection scores and a short audio lead-in. Incubator
   verifies the wake and picks the answering satellite. If it rejects the wake
   or picks another room, Encore goes back to waiting and keeps the connection.
   You can skip the wake word with Talk, the phone's assistant shortcut, or
   the Dot's Action button.
3. Incubator handles recognition and the conversation. Home Assistant runs the
   requested action. Encore plays Incubator's reply and shows the text on devices
   with a display. Incubator can also send timer alerts and announcements between calls.
4. A normal hangup returns Encore to idle. If Incubator asks a follow-up, Encore
   opens the mic for your answer without a wake word or audio lead-in. If
   Incubator supplied a Stop model, you can say “Stop” to cut off a reply or
   announcement or dismiss a ringing alert. Encore recognizes Stop on the device.

Mic mute does not dismiss or silence a ringing alert on a connected satellite.
Use the Dot's Action button or **Stop alert** on Generic Android while muted;
the mic stays off. Android Assistant's **Pause mic** stops its satellite runtime,
so it cannot receive alerts while paused.

Generic Android shows the next countdown in a chip. Tap it to open the same
bottom sheet used for listening; the sheet opens on its own when an alert rings.
The [native timer projection](https://github.com/OHF-Voice/voice-incubator/blob/38b573b13a92f0654b24611a3a6355c8e8c4d5c3/voice_incubator/timers.py#L358-L393)
also drives [Voice PE's LEDs](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/devices/voice-pe.yaml#L1168-L1184).
It supplies up to six anonymous running countdowns, not names, IDs, paused timers,
or upcoming alarms. Only `timer_ringing` opens the alert; locally reaching zero
does not. The ring uses the supplied label and count when available. **Stop alert**
dismisses the current ring without acting on the other countdowns. Verify by
starting several timers, letting one ring while muted, then dismissing it here
or on another satellite and checking the remaining countdowns.

Alert dismissal is best effort. Android's **Stop alert** and the muted Biscuit
Action button are tied to the ring Encore last observed, so they ignore a press
for a ring Encore already knows has ended or been replaced. But the native
[`stop_timer` event](https://github.com/OHF-Voice/voice-incubator/blob/38b573b13a92f0654b24611a3a6355c8e8c4d5c3/voice_incubator/server.py#L2049-L2053)
carries no ring occurrence token. If a replacement starts before Incubator
receives Stop, the app dismisses whichever ring group is current there. It can
also [stop other satellites ringing any of the same timers](https://github.com/OHF-Voice/voice-incubator/blob/38b573b13a92f0654b24611a3a6355c8e8c4d5c3/voice_incubator/server.py#L4474-L4486),
without cancelling unrelated running countdowns. Full protection would require
Incubator to issue an opaque token for each ring occurrence and check the token
echoed by Stop. A running-timer ID alone cannot identify a ring group occurrence.

If Android loses or is denied audio focus, it silences playback locally without
dismissing the ring. To check, interrupt focus while a timer rings in two rooms:
the other room should keep playing, and **Stop alert** should remain available.
While the mic is muted, Encore also drops untagged audio when a native announcement
overlaps a ring, so it cannot play announcement audio as if it were an alert.
The ring stays visible and dismissible. To check, trigger a timer while a native
announcement is in flight and verify that only unambiguous ring audio plays.

## Comparison with Voice PE

We compared Encore with the Incubator firmware for Home Assistant Voice
Preview Edition at
[`8ba314e`](https://github.com/OHF-Voice/firmware-voice-incubator/tree/8ba314e68ff4a23b5c08b78fa21926104bbabba2).
The lab uses
[Incubator app revision `38b573b`](https://github.com/OHF-Voice/voice-incubator/blob/38b573b13a92f0654b24611a3a6355c8e8c4d5c3/voice_incubator/server.py#L2760-L2775)
to verify wakes and pick the answering satellite. This
comparison comes from source; it does not compare the devices' audio, lights,
or physical controls.

| Behavior | Voice PE reference | Encore's choice |
| --- | --- | --- |
| Wake and room selection | [The firmware receives models from the app and starts a candidate from local detection](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/components/voice_incubator/voice_incubator.cpp#L547-L604). | Same split: local detection, upstream verification and election. Screens can distinguish a tentative wake from an accepted one. |
| Follow-up | [The app requests a new, zero-pre-roll turn when unmuted](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/devices/voice-pe.yaml#L1235-L1245). | Same wake-free handoff, but Encore only accepts it for a current accepted response with no output still open. A late or early event shouldn't open the mic unexpectedly. |
| Stop and timeout | [Voice PE arms Stop when responding](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/devices/voice-pe.yaml#L1215-L1233) and can [cancel on the same socket](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/components/voice_incubator/voice_incubator.cpp#L752-L779). | Stop cuts off the current reply; it can also dismiss alerts. Encore retires interrupted call or announcement connections after cancellation so stale audio or events can't affect the next turn. It uses the same 30-second call deadline but drops interrupted playback on timeout. The independent management connection stays up. |
| Mic mute and ringing | Voice PE [mutes capture](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/devices/voice-pe.yaml#L272-L295), [shows the ring and mute together](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/devices/voice-pe.yaml#L1443-L1464), and [lets the center button dismiss the ring](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/devices/voice-pe.yaml#L413-L449). The app sends `timer_ringing` for countdowns, scheduled alarms and timed reminders. | Same separation for alert playback and mic privacy. Mute cancels an active voice turn as above, so an alert cannot use a retiring connection where old PCM is still untagged. Verify by muting before and during a ring, checking that only explicit Stop sends `stop_timer`, and confirming Action/touch Stop never starts capture. |
| Monitoring and intercom | [The protocol has separate monitoring and intercom modes](https://github.com/OHF-Voice/firmware-voice-incubator/blob/8ba314e68ff4a23b5c08b78fa21926104bbabba2/PROTOCOL.md#monitoring-and-the-intercom); Voice PE can report wake scores without answering. | Encore also keeps monitor detections from starting calls. It requires the host to confirm the recording indicator before sending fresh audio, never includes pre-roll, and doesn't revive a previous stream after privacy or connection loss. Targets only advertise the routes they implement. |

See the [foundation notes](foundation.md#single-backend-packages-and-common-controls)
for the safety rationale, pinned companion-app paths, and regression checks.

## Controls and features by device

The table lists features in the development builds. Check each device guide for hardware test results.

| | [Android Assistant](../devices/android-assistant/README.md) | [Generic Android](../devices/android-generic/README.md) | [Echo Dot (2nd gen)](../devices/biscuit/README.md) |
| --- | --- | --- | --- |
| Local start and feedback | Talk; optional Android assistant overlay on Android 12+. Explicit Arm mic and Pause mic. | Talk and conversation overlay over the optional HA dashboard. Local mic mute. | Action button, volume buttons, hardware mute-state gate and light ring; the ring isn't a copy of Voice PE. |
| Off-call audio | No Live Monitor, enrollment, wake teaching, or intercom. | Live Monitor with a visible, focused recording indicator. No AEC monitor route or two-way intercom advertised. | Implements Live Monitor, the AEC route used for enrollment and wake teaching, and intercom transport with recording-ring gating. The full intercom experience needs compatible companion behavior and physical qualification. |
| Other jobs | Personal voice only; no ESPHome controls, room dashboard, or music player. | ESPHome pairing and controls, optional Bluetooth proxy and compatible Sendspin playback, and an illuminance sensor if the device exposes one. | ESPHome controls and optional Bluetooth proxy. The AArch64 starter does not include the retired ARMv7 package's Sendspin companion. |

The [native lab](../lab/incubator/README.md) exercises wake verification, room
selection, recognition, Home Assistant actions, replies, timers, and follow-ups
against Incubator and Home Assistant with virtual capture and playback. Use the
device guides for physical audio, privacy, and install results.
