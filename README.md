# Encore Satellite

**Your devices aren't done yet.**

Encore Satellite aims to turn existing speakers and displays into satellites
for [Voice Incubator](https://github.com/OHF-Voice/voice-incubator) and
[Home Assistant Assist](https://www.home-assistant.io/voice_control/).

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

Several projects I looked at offered more features than I needed. I wanted a
small set of features that worked well, with Home Assistant or Voice Incubator
doing the heavy lifting. Encore's job is to connect the device, handle its audio
and controls, and present what comes back—not become another assistant to manage.

## With or without a screen

The goal is a voice experience like
[Home Assistant Voice Preview Edition](https://www.home-assistant.io/voice-pe/)
on more hardware. On displays, that also means your Home Assistant dashboard
with a small conversation overlay. Timers, alarms, and household automations
stay with Home Assistant or Voice Incubator.

**Early implementation has started.** There's nothing to install on a device
yet, and device support hasn't been verified. The shared Go protocol core has a
[reproducible Home Assistant lab](lab/README.md) pinned to a 2026.12 development
build. [Native Incubator](docs/incubator-validation.md) is tested with its real
upstream wake verification, recognition, HA actions and speech output.
[Experimental Assist wake verification](docs/assist-wake-validation.md) is an
explicit opt-in. Capture and playback are virtual; these are protocol checks,
not hardware or acoustic-quality results. See the [foundation scope](docs/foundation.md)
for what works and what remains. The first device packages will target native
Voice Incubator, with exactly one backend per package and no runtime switching.
Changing backends will require an explicit application-package replacement.
Shared device controls remain to be implemented.

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

The [device package sections](docs/device-packages/README.md) are planned
metadata and documentation only, not installable support. The Tab A7 Lite is the
intended first Android validation tablet.

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
