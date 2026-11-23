# Encore Satellite

Encore connects Android phones, tablets, and older speakers to
[Voice Incubator](https://github.com/OHF-Voice/voice-incubator), the Open Home
Foundation's testbed for Home Assistant voice.

I started Encore to try Incubator on devices I already had. I use Home Assistant
Voice Preview Edition running the [Incubator firmware](https://github.com/OHF-Voice/firmware-voice-incubator)
as the reference for features like wake-free follow-ups and spoken Stop.
Encore detects wake words on the device and handles the mic and speaker.
Incubator checks the wake, picks the satellite that answers, and handles the
conversation. Home Assistant runs the requested actions. I'd rather add features
like timers to those projects so other satellites can use them too. See the
[Voice PE comparison](docs/voice-experience.md) for differences by device.

## Install and set up

Set up Home Assistant and Voice Incubator first. Encore builds are development
previews. Keep the unencrypted, unauthenticated voice connection on a trusted LAN.

| Device | Guide |
| --- | --- |
| Android phone | [Install and set up Encore Satellite Assistant](devices/android-assistant/README.md#install-the-signed-apk) for personal voice input. |
| Android tablet or display | [Install and set up Generic Android](devices/android-generic/README.md#install-the-signed-apk), with an optional Home Assistant dashboard. On a Galaxy Tab A7 Lite, 0.1.63 reached the Home Assistant pairing screen. |
| Echo Dot (2nd gen, Biscuit) | [Install the Alpine build and enroll the Dot](devices/biscuit/INSTALL.md). You need an unlocked Dot and verified recovery. |

[Development releases](https://github.com/farooqu/encore-satellite/releases) ·
[Report an issue](https://github.com/farooqu/encore-satellite/issues)

I plan to try Echo Dot (3rd gen) and Echo (2nd gen) when I have the hardware.
I haven't verified a Meta Portal install. Echo Show via LineageOS is an idea
for later.

Encore is an independent project.
See [credits](ACKNOWLEDGEMENTS.md), [license](LICENSE), and the
[native Incubator test lab](lab/incubator/README.md).
