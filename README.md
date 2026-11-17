# Encore Satellite

**Your devices aren't done yet.**

Encore Satellite aims to turn existing speakers and displays into satellites
that expose their useful hardware to Home Assistant and provide voice input and
output for [Voice Incubator](https://github.com/OHF-Voice/voice-incubator).
[Home Assistant Assist](https://www.home-assistant.io/voice_control/) remains a
possible later option through separate device packages.

I'm building it for my own home and sharing it in case it's useful to yours.

**Voice Incubator is my focus for now.** It's the setup I'd prefer to use at
home, so I'm concentrating on getting that working on real devices first. The
Assist code will stay in the repo, but further Assist development and device
packages are deferred for now. **The default Encore package will use Incubator.**
If a package for the **Home Assistant Assist pipeline** is offered later, it will
be a separate opt-in package rather than another default.

I'm keeping the Assist code so I can reevaluate it from time to time as Incubator
features reach the Home Assistant Assist pipeline. Before I'd move over, I'd want the behaviors
I value: wake-word detection and verification, identifying who's speaking,
stopping a response when someone says "stop", and keeping the microphone listening
throughout an active conversation so it can catch interruptions and follow-ups
without another wake word. That listening must still respect mute and privacy
controls. These are my evaluation criteria, not features already proven on Encore
hardware or a claim about what Assist currently lacks.

## Why Encore?

I like giving things a second chance. A speaker or tablet can still be useful
long after its manufacturer moves on. Reusing one keeps working hardware out of
a landfill, and buying secondhand can make it much more affordable to put a
satellite in another room.

I want Home Assistant to make good use of the hardware already in each device,
with Home Assistant and Voice Incubator doing the application-level heavy lifting.
Encore's job is to expose that hardware, handle voice input and output, and
present what comes back—not become another assistant to manage.

### Broad hardware support, focused purpose

Encore aims to expose as many real sensors and useful ESPHome capabilities to
Home Assistant as each target can practically support—not a minimal sensor set.
This is a long-term goal, not a claim that every sensor works today or a gate
for the first release. Support depends on hardware, platform access and safe
driver/image qualification. Temporarily unavailable readings are valid;
advertising hardware entities that can never work on a target is not.

Voice/media controls, buttons, privacy, status lights, dashboards and device
management belong where they support the satellite. Optional Sendspin music
also belongs and works independently of voice. The boundary is unrelated
standalone applications such as SIP calling—not sensors or satellite controls.
That's a choice for my home, not a judgment on projects with different goals.

## With or without a screen

The goal is a voice experience like
[Home Assistant Voice Preview Edition](https://www.home-assistant.io/voice-pe/)
on more hardware. On displays, that also means your Home Assistant dashboard
with a small conversation overlay. Timers, alarms, and household automations
stay with Home Assistant or Voice Incubator.

**Early implementation has started; no device is release-supported.** An
[Incubator-only Android development APK](devices/android-generic/README.md)
can be built locally; device support has not been verified. An
[experimental Echo Dot 2 package](devices/echo-dot-2/README.md) builds the Linux
environment itself: kernel, recovery initramfs, Alpine radio services and ARMv7
runtime, with clean-slate recovery provisioning/update tools. It has not been
booted or acoustically tested on hardware. Activation supports Action and
Voice Incubator-pushed microWakeWord/MixedNet models. Improv BLE provisioning and a default-off
Bluetooth proxy with active connections are integrated, but require a compatible
Bluetooth-enabled kernel and hardware validation. The installer stages the
bundled project-built kernel from ordinary TWRP; no installed Linux or Fire OS
version is required.
The shared Go protocol core has a
[reproducible Home Assistant lab](lab/README.md) pinned to a 2026.12 development
build. [Native Incubator](docs/incubator-validation.md) is tested with its real
upstream wake verification, recognition, HA actions and speech output.
[Experimental Assist wake verification](docs/assist-wake-validation.md) is an
explicit opt-in. Capture and playback are virtual; these are protocol checks,
not hardware or acoustic-quality results. The lab includes a test-only Assist/native
Incubator selector applied after restart, with independent BLE controls. Production
packages must link one voice backend: Incubator by default, with a possible
separate Home Assistant Assist pipeline package later. Changing backend requires
application-package replacement, not a runtime switch. See the
[foundation scope](docs/foundation.md) for what works and what remains.
Android starts with Incubator and shared dashboard controls.
Dot packages contain only native Incubator. The encrypted common-control endpoint handles the optional
Bluetooth proxy and Sendspin independently of voice. Sendspin uses an authenticated
official SDK companion and the Dot's shared audio mixer; physical music/audio
qualification is still outstanding.

## Devices

The focus isn't finding new ways to hack or unlock devices. I want to build on
the work already done in the community, choosing hardware with an existing way
to run custom code and making it useful as a satellite.

My planned home setup includes multiple units and variations of:

- Echo Dot (2nd generation)
- Samsung Galaxy Tab A7 Lite
- Meta Portal (2nd generation, 10-inch)
- Echo (2nd generation)
- Echo Dot (3rd generation)

These are the devices I intend to use, not a tested compatibility list.

The [device profiles](docs/device-packages/README.md) remain planned release
metadata, not compatibility claims. The Dot's development builder is separate
from supported releases; Android has a development APK, with the Tab A7 Lite as
its intended first validation tablet.

## Want to help?

Contributions for other devices are welcome where the shared core can work.
If you have a way to run custom code on one and can help test it, I'd love to
hear from you.

The [foundation checks](.github/workflows/check.yml) run code and build checks;
the real-service suites run separately in the lab. The
[CI and release policy](release/README.md) explains that boundary and why there
are no device releases, updater or signing trust yet.

I intend to use a permissive license so others can build on the work. The license
hasn't been selected yet.

Encore's design and proof of concept build on Techo5, Techo5 Panel and their
upstream foundations. [Acknowledgements](ACKNOWLEDGEMENTS.md) distinguish that
design credit from the implementation actually included and its required notices.

Encore Satellite is an independent community project, not an official Home
Assistant or Nabu Casa product.
