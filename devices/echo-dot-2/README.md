# Echo Dot (2nd generation): planned package

**Planned, never released, not installable.** This directory contains only a
neutral planning profile and package notes. No runtime, device binary, build
recipe, installer, maintained base image or hardware test result is provided.
See the [package contract](../../docs/device-packages/README.md).

## Local ownership and evidence

This module owns Dot 2 channel wiring, microphone geometry/calibration, speaker EQ,
button/ring mapping, target composition and packaging inputs. Reusable Echo OS
primitives belong in `platforms/echo/`; shared audio/session contracts and DSP do
not belong here. No other Echo model is implicitly supported.

[Techo5's Dot capture source](https://github.com/farooqu/techo5/blob/2b091439765d5f881de4b89255b8cf0637d80cba/echod/internal/hardware/mic/device_dot.go)
is design/code evidence for the following mapping, **not a measurement of an
Encore device or a promise that an unspecified base exposes it unchanged**:

| Source capture property | Inspected Techo5 value |
| --- | --- |
| Rate / format | 16,000 Hz / little-endian packed 24-bit (`S24_3LE`) |
| Interleaved channels | Nine total |
| Channels 0–6 (zero-based) | Seven microphones; source identifies channel 6 as center |
| Channels 7–8 | Two playback references, left then right in the source |

Keep hardware selection, alignment, geometry and calibration local. A future host
must demultiplex/convert this input into the shared bounded mono PCM16/16 kHz
contract and report timing/discontinuities. Do not feed nine-channel packed24
bytes to the engine as mono PCM16 or claim aligned/AEC capture from channel count
alone. Playback route, physical drain, mute enforcement, button identities and
ring mapping still require Encore implementation and on-device confirmation.

`profile.json` selects bypass EQ and advertises no validated capability. It does
not import Techo5's calibration or claim AEC, beamforming, a wake model or a second
processed stream. Any measured Dot correction stays in this module's profile and
must not become a shared or Android default.

## Prerequisites still to establish

- Maintainer and test unit, exact hardware revision, and a lawful existing route
  to run custom application code. Unlocking/exploitation is not provided here.
- Exact maintained base identity/version and provenance, supported kernel,
  userspace/libc, CPU ABI, Go/native dependency toolchain and resource budget.
  None is selected for Encore by these notes.
- Audio/control device access and privileges, driver/mixer prerequisites,
  capture/reference alignment, safe output levels and authoritative physical
  mute behavior. Do not copy device indices or calibration from another unit.
- Application service lifecycle, filesystem layout, configuration permissions,
  installation/replacement procedure and recovery after a failed app update.
  These are unknown, not working service/install instructions.

## Intended assembly and application/base boundary

Once those prerequisites and the protocol foundation are validated, the target's
explicit composition root would assemble the shared Go engine, reusable Echo host,
Dot-local wiring/profile and selectable Assist/native Incubator backends. Package
only that application, necessary target data and audited notices. Do not bundle
Android code or a second voice engine.

The intended deliverable is a versioned application bundle over an independently
maintained base, **not** a boot/kernel/driver/recovery replacement. No concrete
archive layout, ABI, service manager, signing key, install path or command is
invented here. Central release metadata will bind the core SemVer, Dot package
revision, source commit and verified base compatibility when there is a package;
the current central revision is zero and release support is false.

Before support is claimed, record pinned base/toolchain/backend versions and real
device results for capture/playback, wake during playback, physical drain,
stop/mute with queued audio, both backends and switching, disconnect/recovery,
reboot/service recovery, resource use and app replacement without a base update.
Hardware/wiring/tuning changes need evidence from a Dot maintainer; the profile
validator is only a metadata check, not any of these acceptance results.

## Acknowledgement

Techo5 and its upstream audio/device work informed these notes. See
[ACKNOWLEDGEMENTS.md](../../ACKNOWLEDGEMENTS.md) and the
[package attribution/licensing caveat](../../docs/device-packages/README.md#evidence-attribution-and-licensing).
No Techo5 implementation, calibration, model or vendor blob is copied here; the
license for new Encore material remains unselected.
