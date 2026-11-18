# Echo Dot (2nd generation) development package

**Experimental, never released, not hardware-qualified.** There is now a real
Linux environment: project-built ARMv7 kernel, recovery initramfs, Alpine userspace,
radio services, Satellite runtime and USB/recovery tooling, not an app requiring an
existing Linux installation. Do not flash a household device expecting a finished Techo5 replacement.
`profile.json` and the central registry remain planned/revision-zero because no
supported release or provisioned public update channel exists. Other Echo models are not supported.

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
`encore-biscuit-v1`, and default generated names use `encore-biscuit-…`.
The backend remains `incubator`; configuration/key locations and recovery/slot
mechanics are unchanged. There are no legacy source aliases or target mappings.

Kernel and Sendspin builders, source-kit paths and provenance use Biscuit.
Rebuild old development artifacts: the renamed build inputs intentionally have
new identities, and strict validation rejects stale artifacts instead of
relabeling their records. No compatibility with unpublished old bundles is
promised. Do not rewrite retained provenance, source kits, backups or device data.
The frozen first private candidate remains built from its original commit and
does not need this rename for physical preflight; no device migration is performed.

## What is implemented

- Static Go Linux ARMv7 executable explicitly assembling the shared engine,
  Linux host primitives, Biscuit wiring and the native Incubator backend only.
- Source-backed ALSA capture: card 0/device 24, 16 kHz packed24, nine channels.
  Channel 6 (center microphone) becomes bounded mono PCM16. Channels 7/8 are
  playback references and are **not** mislabeled as microphones or a second stream.
- Source-backed speaker routing, conservative software volume, 16→48 kHz stereo
  output to card 0/device 23, filtered voice interpolation and source-scoped drain.
- Optional official Sendspin SDK companion with authenticated Music Assistant
  pairing, private persistent trust and an independent default-OFF music switch.
  Voice and music share one ALSA mixer, not competing speaker devices.
- Authoritative hardware privacy-state reads around acquisition/delivery, buffer
  invalidation on mute, Action start/stop, volume buttons and a full-ring renderer.
  Driver-first mute is not toggled a second time. Never read `power_button_state`.
- Incubator is the default Dot package. A possible future package for the
  **Home Assistant Assist pipeline** would be opt-in and separately qualified.
  There is no runtime voice-backend
  selector or fallback, and the binary excludes the Assist adapter.
  The configured name is used for hostname, discovery and backend identity;
  the real wlan0 MAC identifies the device. No lab HTTP controls are packaged.
- Clean application state, first-boot Improv BLE or prompted USB Wi-Fi credentials, optional custom name,
  recovery/misc backups, USB transfer read-back checks and two Encore rootfs slots.
- Experimental signed Linux application OTA, persistent dev/stable selection,
  ESPHome availability and explicit trial installation, with locally provisioned
  trust and no production feed/key or physical OTA qualification.

**Not implemented by this package:** AEC/beamforming, speaker EQ calibration,
Voice PE ring parity, base/kernel OTA migration or a production release service.
Voice activation uses Action or
app-pushed microWakeWord/MixedNet models. Incubator does not advertise AEC,
monitoring or intercom. A source-backed
route is not an acoustic result; playback level and every physical control still
need testing on an Encore unit. The deliberately quiet untuned output is not a
calibrated safe-output guarantee.

## App-pushed wake models

The Dot advertises `wake_format=mixednet`, six model slots, max-pooling and a
permanently resident spoken Stop model. Voice Incubator lowers microWakeWord
TFLite models into MNB1 blobs and pushes them over protocol 2; the Dot does not
download arbitrary URLs or run a TFLite interpreter. The pure-Go frontend and
executor are ported from pinned Techo5; their licenses/provenance are retained in
`core/wake/mixednet`, `core/wake/mwwfront` and `licenses/`, and included in the
rootfs notices.

Each model is limited to 512 KiB and checked for matching ID, exact size, SHA256,
valid settings and executable graph bounds before a success acknowledgement.
Replacement is by ID, not the wire slot hint, and an invalid replacement keeps
the existing model. Six residents normally allow five wakes plus Stop. Models
are volatile per app connection and are re-pushed after reconnect; no trained
wake model ships in the runtime.

Privacy-gated 16 kHz center-microphone PCM feeds a bounded asynchronous detector
worker. Inference never runs under the capture/privacy or session lock. Reported
capture gaps reset detector history; mute/disconnect revoke the connection generation,
clear audio and discard its models. Idle wakes carry their model identity,
0..255 scores and two seconds of pre-roll to upstream verification. Spoken Stop
is armed only during voice output, announcements and ringing alerts; it cancels
those instead of starting a new call. It is not a music transport command.

The protocol/executor and recorded-speech host checks are not Dot CPU-budget,
far-field wake accuracy, echo rejection or physical privacy qualification. The
pre-hardware review's recovery, first-boot health, ALSA, capture-loss and build
provenance findings are corrected in revision 8. Revision 7 includes models but
predates those fixes; revision 6 predates models. No candidate is hardware
qualified or approved to flash. Revision 8 changes the recovery initramfs and
therefore the shared-base identity: it is not an app-slot update from revision 7.

Revision 11 is the current local maintenance candidate, rebased onto
`ec2167426d43c67afcc40b21cd5c33f2470117d7`. It adds key-only SSH, transactional
Wi-Fi management and the explicit dual-stack firewall described below. Its
Alpine base adds Dropbear and legacy firewall tools, so it is not an ordinary
app-slot update from revision 8 either. The exact bundle/provenance and source
kits must be retained; no shared-base migration or hardware acceptance is implied.

## Physical buttons

The Dot 2 mappings follow the pinned Techo5 wiring, not generic PC key guesses:

| Button | Linux key code | Runtime behavior |
|---|---|---|
| + | 115 | Raise the shared physical speaker gain one step immediately; holding repeats every 200 ms. |
| − | 114 | Lower that gain one step immediately; holding repeats every 200 ms down to silence. |
| Action | 138 | Start manual Incubator voice when idle; stop a ringing timer or active voice and local music otherwise. |
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
100% of the software ceiling and ordinary motion. Invalid policy fails startup;
an invalid command or failed save leaves live settings unchanged.

`led_current=3` is unchanged. The software ceiling is 32 per channel and 48 summed
channels per segment (576 across the ring), **not a calibrated current or comfort
guarantee**. Night **on or unknown** caps channels at 8 and stops decorative
motion; reduced motion freezes the processing crest and steadies output/alerts.
Brightness cannot black out privacy: red retains a floor of 8. Real-input energy
can still change the mint ring under reduced motion. The 50 ms frame tick,
2.4 s rotation, 1.8 s output pulse, 3.2 s alert swell, scalar easing and microphone
visual gain are initial software tuning, not hardware-qualified values.

One frame writer coalesces unchanged driver payloads and recomputes the current
state rather than resuming a saved sequence. Provisioning/update/music status
animations and other Echo models are not implemented here. Independent music,
capture processing, release scope and audio cancellation policy are unchanged.
Fake-PCM/clock/sink and encrypted-control tests plus ARMv7 cross-builds validate
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
It exposes one persistent `Bluetooth proxy` switch, **OFF by default**. When ON,
HA can scan, make active GATT connections and request supported Just Works bonding.
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
Possession of the device's Noise key grants maintenance access, including the
ability to authorize root SSH keys; treat it as an administrator credential.

### Key-only SSH

SSH is **OFF by default**. Initial USB installation accepts
`--ssh-public-key-file /path/to/owner.pub` and, separately, `--ssh-enable`.
Enabling requires a valid key; supplying a key alone does not enable the service.
Only single Ed25519 or RSA 3072–8192-bit OpenSSH public keys are accepted.
Private keys, authorized-key options, certificates and multiline input are
rejected. Never give the installer a private key.

Home Assistant discovers an **SSH enabled** switch, **SSH running** status and
public-key fingerprints. Its ESPHome actions are `ssh_status`,
`ssh_set_enabled(enabled)`, `ssh_add_key(public_key)` and
`ssh_remove_key(fingerprint)`. Rotate with **add → test a new SSH login → remove
the old fingerprint**; deleting the last key while SSH is enabled is refused.
Adding/removing keys does not terminate existing sessions; disabling SSH closes
the firewall gate and terminates the managed listener and sessions.

Dropbear runs independently of the voice runtime, with root public-key login
only, password/keyboard-interactive authentication and forwarding disabled.
Keys, enablement and the device-generated Ed25519 host identity persist privately
under `/data/encore/ssh`, outside application slots. No host or authorized key is
shipped in the bundle. Verify the host fingerprint through the trusted physical
USB console before accepting the first SSH connection. An enabled setting is
not proof that the supervisor is actually running: inspect **SSH running** too.

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
| TCP 22, SSH | Wi-Fi only, and only while the SSH supervisor enables its gate; the gate precedes established-connection acceptance. |
| TCP 6053, ESPHome controls/Bluetooth proxy | Wi-Fi only; requires the nonzero per-device Noise key. |
| TCP 6055, native Voice Incubator protocol 2 | Wi-Fi only; **unauthenticated**, use only on a trusted LAN/VLAN, never port-forward. |
| UDP 5353 | mDNS multicast destinations and unicast replies on Wi-Fi. |
| DHCP and selected ICMP/IPv6 discovery | Allowed on Wi-Fi for address configuration, diagnostics and network operation. |
| Sendspin | No inbound port: outbound authenticated connection and its replies only. |

Loopback and established/related replies are allowed, except that the SSH gate
can block established SSH traffic. USB network SSH and unrelated Techo5 ports
remain closed. Physical USB serial is a separate root console, not a firewall
exception. Host namespace packet tests do not qualify the Dot's vendor kernel.

## Sendspin music

The encrypted ESPHome endpoint exposes `Sendspin music`, `Sendspin server`,
`Resume music` and `Stop music` in the Incubator package. Set the server to
`ws://YOUR_MA_HOST:8927/sendspin`, then enable music. This is a WebSocket carrier
for the SDK's authenticated Noise protocol, **not** permission for plaintext or
guest playback. There is no automatic discovery or duplicate media-player
entity: use Music Assistant and its HA integration for queues and media controls.
Music being OFF or MA being absent does not stop voice, BLE or first-boot Improv.

On initial pairing, leave music OFF and use the physical USB console privately:
`encore-sendspin pairing-token --state /data/encore/sendspin`. Enter that token in
Music Assistant's player setup, after enabling music so the receiver connects.
Do not publish the token, pass it in the server URL or put it in logs. Maintenance
commands require exclusive access, so turn music OFF before obtaining the token
or running `encore-sendspin revoke-all --state /data/encore/sendspin`. Revocation
retains the receiver identity, removes all trusted servers and rotates the token;
the next connection requires pairing again. Trust survives rootfs-slot updates
in the private `/data/encore/sendspin` directory. Corrupt state fails closed,
not into an unpaired guest mode. Losing this private directory loses identity.

The companion is a separate C++20 process connected only by an inherited,
versioned Unix packet socket. No HTTP PCM sink or new network control listener
is installed. It negotiates PCM16/48 kHz/stereo; the Go mixer bounds music to
100 ms plus a 20 ms DMA window and bounded IPC, and reports frames actually
consumed according to ALSA delay and the SDK's raw monotonic clock. Voice uses
separate queues and a filtered 16→48 kHz converter; voice Drain waits only for
voice. The driver cannot atomically stop and snapshot DMA. A cancellation that
drops shared DMA discards uncertain frames and fences affected voice/music;
music reconnects with a fresh epoch rather than replaying potentially heard
samples or claiming they were consumed. Underruns similarly reset the output
clock without terminating the runtime. Ordinary volume changes do not drop DMA
and take effect within the bounded 20 ms window.
Aggregate engine activity ducks **music only** to current user gain × 0.2 with
a 20 ms ramp. Countdown alone does not duck; ringing timers do. Physical volume
still applies the conservative Dot master limit to the combined, pre-limited
signal, including simultaneous sources and filter overshoot. This route is
not a claim that the Dot's single speaker is physically stereo.

Privacy and Stop silence music immediately. Unmuting, reconnecting, a new stream
or voice completion cannot clear that latch: use **Resume music** explicitly
(and resume the MA queue if it is stopped). Enabling/reconfiguring also requires
an explicit Resume. Action starts voice when idle and stops foreground voice
and music otherwise. This does not pause the MA group or turn music into a voice
session. Automatic room targeting, “play here” intents, wake-over-music and AEC
are not implemented. Physical presentation latency, acoustic quality, power,
CPU/RAM, underruns, Wi-Fi/BLE coexistence and timer recovery remain Dot tests.

## Base and recovery prerequisites

Initial installation requires an already-unlocked **biscuit** with root adb in
compatible recovery (tested host-side contract is TWRP-style), the expected
partition layout and its own usable Wi-Fi firmware. Unlocking,
exploitation and bootloader modification are out of scope.

**Staging TWRP does not need Bluetooth or an installed Linux environment.** The
installation bundle includes Encore's project-built Bluetooth-enabled biscuit
kernel. The installer assembles the recovery image automatically and preserves
the original TWRP backup. Preflight
checks the actual kernel's embedded configuration (`CONFIG_BT=y`,
`CONFIG_BT_HCIVHCI=y`), requires both appended device trees to match the unit's
saved recovery byte-for-byte, and preserves its load addresses. Missing/different
device trees require investigation, not a forced flash. The installer never
silently substitutes TWRP's kernel. Update staging restores the previously saved
Encore image; changing the shared kernel is not an app-slot update.

There is **no Fire OS version check, no requirement to boot or restore Fire OS,
and no migration of its Wi-Fi, Amazon account, Techo5 configuration or HA keys**.
The Wi-Fi firmware remains a genuine hardware dependency: `WMT_SOC.cfg`,
`WIFI_RAM_CODE_8163` and ROM `*_hdr.bin` patches. The installer reads an already
accessible vendor firmware directory or accepts `--firmware-dir`; it never bundles
or downloads vendor blobs. A unit that no longer has usable firmware needs its
own saved files, not a cosmetic version-string bypass.

Factory calibration/identity partitions are **not** erased. “Clean slate” means
new Encore state under `/data/encore`, not formatting the eMMC. An existing
Encore install is refused by `install`; use `update` to preserve it.

The prototype boot chain uses recovery because biscuit boot_a/b adds
`skip_initramfs`. Installation replaces recovery only after saving TWRP on the
host and device. It leaves bootloader, Android boot/system slots and factory
partitions alone. Cache (p15) holds `/encore/{slots/a,slots/b,firmware}`; userdata
(p16) holds `/encore` configuration and Noise identity. MISC's recovery command
field selects Linux on future boots; Android slot metadata is not changed.

## Build locally

Requirements: Linux x86-64, Go from `go.mod`, Python 3.12+, Git, kernel build tools
(see the workflow below), CMake 3.22+, Ninja, a host C++20 compiler, binutils/readelf,
and Docker for Alpine base assembly. The music builder downloads checksum-locked
Zig 0.14.1 for the ARMv7 musl userspace build, targeting Linux 3.18/Cortex-A7;
the AOSP kernel compiler is not used for C++ userspace.
No root or QEMU is required by the builder when Docker is already available.
The host image is digest-pinned; apk verifies Alpine ARMv7 package signatures.
Repository packages can advance: retain the exact resulting base tarball and its
installed apk database for app updates. This is not a reproducible release lock
or a commitment to support the old recovery kernel indefinitely.
The host-built base used here resolves BlueZ 5.82-r0 and D-Bus 1.16.2-r1; neither
has been tested on a Dot kernel. Those versions are build inventory, not a proven
minimum or a compatibility promise.

```sh
make check biscuit-check biscuit-build
make biscuit-base
make biscuit-package BISCUIT_REVISION=1
```

Use a new monotonically increasing **local development** revision for each build.
The builder prints the ZIP SHA256 and records source commit, dirty-tree status,
core version, backend variant (`incubator`), base digest and payload digests.
`make biscuit-check` also verifies that the Dot dependency closure includes Incubator
and excludes Assist. The ZIP contains only the rootfs,
recovery initramfs, project kernel, kernel provenance and manifest, with no
credentials or separately supplied Wi-Fi/Bluetooth radio firmware. The kernel
does embed `i2s_to_spi_v34.bin` audio FPGA firmware; its redistribution terms are
unresolved. The owner-approved [good-faith exception](packaging/FPGA-PROVENANCE.txt)
covers this exact binary for dev and production distributions, not legal
clearance or approval of a particular publication. Preserve the decision with
the distribution and meet the remaining [source obligations](packaging/KERNEL-SOURCE.md).
Its Linux-base identity binds the kernel bytes,
kernel build inputs, Alpine base and recovery initramfs. The recovery image is
assembled per unit using its saved header/load addresses and the bundled kernel.
No package has been published and the central released revision remains zero.

### Sendspin artifact

`make biscuit-sendspin` builds the locked official SDK and dependencies from
`platforms/linuxsendspin/dependencies.lock.json`. `make biscuit-package` requires
this artifact as well as the kernel. The stripped static ARM hard-float companion
needs no added Alpine shared libraries or C++ loader, so companion-only changes
do not change the base identity. It lives in each application slot. Packaging verifies
the target ABI, integration-source hashes, dependency lock and artifact digests;
it refuses a host binary, damaged output or stale integration build.

Keep `build/biscuit/sendspin/` (or `BISCUIT_SENDSPIN_ROOT`) with its `source.tar.gz`,
notices and provenance. The source kit includes the exact SDK/dependency trees,
integration recipe and bundled runtime sources. The rootfs carries the companion,
provenance and notices, not the build toolchain or Music Assistant server. The
source kit must accompany any eventual binary distribution; the local development
ZIP alone is not a completed license audit or release process. These are build
checks, not evidence that the ARM binary has played through a physical Dot.

### Project-owned kernel artifact

`make biscuit-package` builds or verifies/reuses the required kernel artifact via
`make biscuit-kernel`. The kernel is not recompiled for every Satellite build. It
uses the Techo5 Dot recipe at
`b2a50c74bbe8b34785748574aa39fbd045fc4e46` using AOSP arm-eabi 4.8 at
`26e93f6af47f7bd3a9beb5c102a5f45e19bfa38a`. It needs the normal Linux kernel
build tools (including make, host C compiler, Perl, bc, patch, curl, tar and xz).
It verifies the Amazon and upstream Linux source hashes, applies the pinned
Bluetooth/security patch series, validates the resulting configuration and
embedded image, and retains output under
`build/biscuit/kernel/<build-inputs-sha256>/`. That directory contains
`zImage-dtb`, its config/System.map, `provenance.json` and `source.tar.gz` with
the prepared corresponding kernel source, patches and build inputs. The rootfs
includes the kernel's COPYING and source-kit instructions. It never flashes a device.

The recipe and toolchain caches live under `.amp/in/orb-backup/biscuit-kernel`.
Build inputs are locked in `packaging/kernel-inputs.json`; the identity also
includes the builder script. The builder fixes the kernel build timestamp/version,
records actual tool versions/environment, checks cached artifacts before reuse,
and refuses to overwrite a damaged artifact. Input changes select a new directory;
an artifact's actual kernel/source hashes identify its exact bytes. Input identity
alone does not guarantee identical bytes on different hosts. Keep the exact
artifact you qualify. `BISCUIT_KERNEL_ROOT` changes the artifact store location.

[Echo Dot Linux kernel CI](../../.github/workflows/biscuit-kernel.yml) runs for kernel
input changes or explicit workflow dispatch, using a digest-pinned Ubuntu image.
It caches only checksum-verified source downloads. Kernel/source-kit uploads are
withheld pending concrete publication approval; adopting the FPGA exception does
not enable automatic uploads. Build local artifacts under `build/biscuit/kernel/`; packaging verifies
them against the current inputs and all recorded file hashes. CI verification
does not clear binary or source-kit distribution. No workflow signs, publishes
a release or flashes hardware.

These Encore builds have not booted on a Dot. Techo5's previously booted `.4`
binary remains reference evidence, not proof for our compiled bytes. Linux 3.18
with the pinned security backports still needs security maintenance and target
qualification. Before binary publication, retain the corresponding source kit
and build inputs durably with the exact release, and complete the system license
audit. See [kernel source/rebuild instructions](packaging/KERNEL-SOURCE.md).

## Initial USB provisioning

Start with read-only preflight; substitute the exact adb serial and trusted local
build hash. Omit `--name` for deterministic `encore-biscuit-<six hex digits derived
from serial>`, or supply a lowercase DNS name such as `office-speaker` (63 characters
maximum). Default setup is Improv BLE after flashing; no Wi-Fi data is taken from
Fire OS. Alternatively add `--wifi-ssid YOUR_SSID` to prompt for its WPA2 passphrase
over USB when applying. The passphrase is never a command-line argument or log
field; only a private derived PSK is stored. WPA-Enterprise/open networks are not
implemented by this setup path.

```sh
python3 devices/biscuit/packaging/install.py install \
  --serial YOUR_ADB_SERIAL --bundle build/biscuit/encore-biscuit-1.zip \
  --sha256 TRUSTED_BUILD_SHA256 --backup-dir /your/private/dot-backup \
  --name office-speaker
```

Add `--apply --allow-development` to perform the reviewed writes. This flag
explicitly acknowledges an **unsigned, local development** bundle; its out-of-band
hash is not a production signature or a secure OTA mechanism. Backups must be
retained privately. The tool does not reboot automatically: after success, reboot
to recovery to start Encore. Neither a successful transfer nor a startup marker
means this untested image has passed device acceptance.

The installer generates a unique Noise encryption key and saves a private copy at
`BACKUP_DIR/encore-api-key.txt`. Keep it for ESPHome onboarding in HA after Improv
joins Wi-Fi; it is not included in advertising or the setup URL. This avoids
needing a second USB connection just to retrieve the key.

If Wi-Fi is unavailable, the physical USB CDC serial console remains the fallback.
It is a root shell regardless of SSH enablement: physical USB access grants root.
Use it to correct `/data/encore/config.json` or the supplicant configuration and
reboot. Assist's generated Noise key is `/data/encore/noise.psk`; read it privately
over this console for HA pairing, never publish it in a log or screenshot.

## Encore-only updates and recovery

From the USB console, `encore-recovery twrp --apply` restores the verified saved
TWRP image and reboots into it. Run `install.py update` with `--serial`, `--bundle`
and `--sha256` (preflight by default, or `--apply --allow-development`). It retains
Encore's name, Wi-Fi and Noise key and stages only the **inactive** rootfs slot.
This USB development path preserves the installed backend and channel. A future
Assist variant would require explicit application-package replacement with tested
state migration/recovery, not a config edit, ordinary update or Linux-base reflash.
That cross-variant replacement is not implemented. Older local prototypes with a
`backend` config field or missing manifest variant are rejected; they were never
released, and no automatic migration is claimed.
The current active slot must be healthy. Revisions cannot be reused or downgraded,
including after a failed trial. Any change to the kernel, its build inputs, the
Alpine base or recovery initramfs is refused as an app update. Both rootfs slots
share the recovery kernel, so replacing it could break both fallbacks. A safe
base-migration updater is not yet implemented; it is not silently approximated
by overwriting recovery during a normal update.

The old good slot stays intact. The new slot gets two trial boots and is committed
only after the runtime validates BLE, opens its capture/control paths and remains
alive for 30 seconds. Waiting for first-boot Wi-Fi credentials does not spend a
three-minute update health timeout or commit a slot. A failed trial falls back;
when neither slot is bootable the recovery
initramfs stays in USB rescue, never silently boots old Android. Initial installs
seed both slots on trial; neither is assumed known-good. Do not overwrite both
slots during an update and lose the recovery copy. A later successful update
naturally uses the other slot again.

The updater restores the same locally saved Encore recovery image after TWRP;
the bundle must identify the same Linux base/kernel. The saved `twrp.img`/`linux.img`
and their checksums are in `/cache/encore` from recovery (`/store/encore` in Linux).
A failed/partial initial installation or stale install-lock requires inspection
while recovery is still running, not blindly rerunning or erasing partitions.
Power-loss boot/rollback, USB enumeration and restoration remain hardware tests,
not guarantees established by filesystem unit tests.

### Signed selected-channel application OTA (experimental)

The USB/TWRP installer above remains an unsigned local **dev** path and preserves
the installed channel. The Linux runtime separately implements signed network
application-slot updates through its **encrypted** ESPHome management endpoint:

- **Update channel** (`update_channel`) has exactly `dev` and `stable`. Desired
  channel and a generation counter are persisted privately in
  `/data/encore/update-state.json` before acknowledgment. The shared `config.json`
  is unchanged so the previous good runtime can still read it after rollback;
  older-runtime settings changes also leave updater state intact. An absent
  updater-state file initializes from the installed manifest,
  not a version suffix. Selection clears old availability and cancels old work;
  it never downloads, installs or restarts anything. A dev→stable→dev change also
  invalidates the original dev authorization.
- **Check for updates** (`check_updates`) and the `linux_update` entity's CHECK
  command discover only. Discovery also runs hourly. Installed and latest strings
  include core SemVer and package revision. Missing trust, network failures and
  compatibility/recovery failures report unknown with a reason, not up-to-date.
- Only an explicit `linux_update` UPDATE command requests download, inactive-slot
  staging, trial activation and an orderly reboot. Downloaded, staged, activated
  and health-accepted are distinct states. No automatic installation occurs.

An eligible already-published release counts: there is no switch-time watermark.
Its signed selected-channel `core_version` must be strictly newer than the actual
installed core under SemVer precedence, **and** its revision must exceed the
highest attempted revision (including failed trials). Build metadata, equal core
at a higher revision and older cores do not qualify. A selected channel behind
installed waits; no downgrade or automatic fallback is allowed. Stable forbids
prereleases. Installed provenance stays immutable and separate from selection.

Linux and Android share these control semantics, not installers or privileges.
The Linux implementation is `platforms/linuxupdate` plus this device's adapter;
no Android or shared backend is imported by its updater.

#### Feed trust and offline approval

There is **no production endpoint or key built in**. A separately approved local
operator must provision `/data/encore/update-trust.json` (0600, root-owned, inside
the private device directory) with `base_url` (HTTPS) and `public_key` (base64 raw
32-byte Ed25519 public key). Optional `root_ca_pem` supplies a locally trusted HTTPS
CA PEM; otherwise Go uses system roots. The maintenance base may not contain a CA
bundle, so provision this CA explicitly when required. Never disable TLS validation.
The device clock must be correct: stale or implausibly future signed metadata
fails closed. This task does not provision trust, time services or any device.

Fetch exactly `<base_url>/biscuit/incubator/<dev|stable>.jws`. Metadata is compact
JWS (RFC 7515, EdDSA/RFC 8037) with the exact protected header
`{"alg":"EdDSA","typ":"encore-linux-update+jws"}`. Payload fields are:

`target`, `backend`, `format`, `channel`, `core_version`, `revision`, `base_sha256`,
`url`, `size`, `sha256`, `issued`, `expires`.

Times are Unix seconds; validity is at most seven days, with five minutes of
future-issued tolerance. Metadata is bounded to 32 KiB and bundles to 256 MiB.
HTTPS is required for metadata, artifacts and redirects, without URL credentials
or fragments. Query strings are allowed for hosts such as GitHub Releases that
redirect to temporary signed download URLs. Put the permanent versioned release
URL in metadata, not the expiring redirect URL.
Private per-channel replay checkpoints reject lower issue
times/revisions and replacement of an observed revision's immutable identity.
The signed SHA-256 covers the entire ZIP, binding its manifest and all payloads;
individual members and exact decompressed size limits are checked again locally.

`packaging/feed.py` validates an inspected local dev bundle and signs its metadata
using an **offline Ed25519 PEM key** and OpenSSL 3. Its arguments are `--bundle`,
`--url`, `--channel`, `--expires`, `--key`, `--output`, and optional `--issued`.
It refuses output overwrite and performs no upload or device operation. A stable
entry promotes the exact existing dev-provenance bytes/revision of a non-prerelease
core; it does not rewrite or rebuild the bundle. Channel is signed feed policy,
not a reason to modify installed provenance. A signature is publisher approval,
not evidence of hardware qualification. Key rotation/revocation and publication
need their own reviewed operational procedure; feed data cannot replace trust.

#### Staging, activation and recovery boundaries

Before staging and again before activation, the running slot must equal active
and be `good`; target/backend/format and the exact shared-base identity must match.
Saved Linux/TWRP hashes, the live recovery image and padded MISC `boot-recovery`
selection must be intact. The existing USB install lock serializes writes. A
stale lock fails closed and needs physical recovery inspection, not auto-deletion.

The updater verifies the ZIP, extracts a bounded safe rootfs into private staging,
fsyncs its files/directories, durably reserves the revision, invalidates the
inactive slot, and replaces **only that slot**. Active remains the running good
slot until a generation-fenced explicit activation writes `trial 2` and switches
active last. Cancellation can leave an unactivated bad inactive slot/reserved
revision, never authorization to reuse that revision. The lock is released before
the orderly reboot. If activation/reboot durability is uncertain, further requests
are blocked until reboot/inspection rather than claiming success or retrying.

The unchanged initramfs/health service owns the two-boot trial, health commitment
and old-good-slot fallback described above. OTA never writes the kernel, initramfs,
recovery or MISC and never mutates Wi-Fi, SSH keys, pairing or saved recovery
backups. **Base/kernel migration remains unsupported.** Host filesystem/Noise/TLS
tests exercise this path, but actual power-loss behavior and physical-device OTA
acceptance remain unqualified. Existing devices do not gain OTA until a separately
approved installation of an OTA-capable runtime and local trust provisioning.

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
