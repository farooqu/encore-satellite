# Reproducible protocol lab

The lab runs real Home Assistant and Voice Incubator with a virtual Encore
satellite on Linux x86-64. Capture, local wake detections and playback are
fixtures; this does not qualify physical audio, privacy switches or radios.

Home Assistant is `2026.12.0.dev0`, commit
`1395798e2338bd46ee522539f7281b336c44e3b8`, with Python 3.14.7 and
aioesphomeapi 46.30.0. `prepare_ha.py` verifies the source archive and uses HA's
own translation tool to generate English resources. No HA protocol code is
patched. HA runs with `--skip-pip`; its dependencies come from `uv.lock`.

## Run in an Amp orb

Follow the [native Incubator setup](incubator/README.md) for the full `make lab-test`
run, including the prerequisites, HA integration, upstream models and generated
PCM. `make check` runs the dependency checks, vet and race-enabled Go tests
without those services.

`make lab-up` starts the supervised Home Assistant and virtual Encore services
from `.amp/services.yaml`. Restart `home-assistant` after changing its integration
or configuration; restart `encore` after changing Go code. Use `amp orb service
logs home-assistant` and `amp orb service logs encore` to diagnose startup.

`make lab-provision` onboards **only the isolated loopback HA instance**, exposes
`input_boolean.encore_lamp`, confirms a healthy pending HTTP configuration if
present, and pairs the encrypted ESPHome endpoint using real mDNS discovery. On
first pairing it checks that an incorrect Noise key is rejected. Do not point
the lab at a household installation.

Private state defaults to `$HOME/.local/state/encore-lab`. Use the same
`ENCORE_LAB_STATE` override for services and clients if you need a separate test
instance. `credentials.json` contains the random password for the `encore` user
if you need to log into the lab HA portal. Credentials and the generated Noise
key are stored with mode 0600; never publish them or substitute production keys.

## Independent ESPHome and Bluetooth controls

Voice Incubator is the only voice backend. The encrypted ESPHome endpoint on
6053 advertises the **Bluetooth proxy** switch, not an ESPHome voice satellite.
Mute and Bluetooth settings persist in `settings.json`; older saved voice
selection fields are ignored. Bluetooth starts off when no setting is saved.
Mute takes effect even if persistence fails; unmute requires a successful write.
Voice mute, cancellation and reconnect do not restart Bluetooth, and an offline
voice peer or a native listen-port bind failure does not remove management.

The virtual radio emits changing advertisements. See the separate [Bluetooth
proxy tests](../docs/bluetooth-linux.md) for synthetic GATT, pairing and slot
coverage. See the [Sendspin lab](sendspin/README.md) for an optional real Music
Assistant queue and C++ client feeding a virtual mixer. Run the voice and
Sendspin suites serially because they share the lab instance and ports.

## Fixture interfaces

On loopback port 8099, `GET /state` reports the session, playback, Bluetooth
and native listener startup error. `POST /command` accepts `start`, `wake`,
`capture`, `stop`, `mute` and `hold_playback`. Capture accepts 1–1024 signed
PCM16 samples. The unencrypted native voice adapter listens on loopback port
6055; the suite's recording proxy supplies its native mDNS advertisement.
Do not expose these fixture or native ports to an untrusted network.

Older dual-backend results in [historical protocol proof](proof.md) are not
instructions for this checkout. Downloaded models, services and tools keep
their own terms; see the [Incubator lab notices](incubator/NOTICES.md).
