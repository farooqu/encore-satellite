# Biscuit Linux 6.1 / AArch64

New Biscuit builds use Linux 6.1 and native AArch64 Alpine and Go binaries.
The development starter below is the supported local package format; neither
historical ARMv7 packages nor RAM-root trial artifacts are install inputs.

## Private persistent ARM64 starter

`make biscuit-package BISCUIT_SEALED_BASE=/path/to/starter-inputs
BISCUIT_REVISION=1` writes a new `build/biscuit/encore-biscuit-1/starter.zip`.
Put the retained audiofix1 `rootfs.tar.gz` and `audited-header.bin` and the
`6.1.178-encore-biscuit-arm64-stpfix1` `Image` and `biscuit-arm64-probe.dtb`
in that directory. The starter reuses the pinned AArch64 Alpine 3.22.8 packages
and FPGA firmware, and builds the current Encore runtime with the in-place
BlueZ advertisement update. These two Bluetooth fixes passed Chrome enrollment
on the supervised RAM-root test; that does not qualify the persistent starter.
The exact retained input/header policy is not a general loader compatibility
claim. Packaging does not contact a device or enable OTA.

**Before installing Encore:** Start with an Echo Dot (2nd gen, `biscuit`) on
stock Fire OS, with both `system_a` and `system_b` intact. The starter currently
accepts the exact radio firmware set from Fire OS 6.5.7.4. Prepare the Dot with
the upstream [Amonet Biscuit project](https://xdaforums.com/t/unlock-root-twrp-unbrick-amazon-echo-dot-2nd-gen-2016-biscuit.4761416/),
then boot its Biscuit TWRP and verify root ADB. Encore does not bundle Amonet
or TWRP, and Techo5 is not a prerequisite. The bench test established a
working recovery on one unlocked unit, not compatibility with every upstream
release or stock bootloader. Qualify the exact recovery, firmware extraction
and rollback on a stock-origin unit before publishing install instructions.

From the checkout root with the Dot in TWRP, run
`python3 devices/biscuit/packaging/install.py install` with the exact ADB serial, `--bundle`,
`--sha256`, an independently verified full `--twrp-sha256`, a private new
`--backup-dir`. Preflight briefly mounts each stock system partition read-only,
without journal replay, and verifies the complete four-file Wi-Fi/Bluetooth set
by size and SHA-256. Leave those partitions unmounted in TWRP before running it.
This is not a hash check of the entire Fire OS image. Preflight makes no
persistent writes, exports no firmware bytes and does not read credentials.
On the bench Dot both stock slots matched this set under `system/vendor/firmware`
inside the mounted partition; root-level `vendor` and `etc` symlinks are not
followed. The bench check ran from Encore recovery, not the TWRP installer.
Only proceed with a coordinated, explicitly approved `--apply --allow-development`
after verifying both real ext4 partitions and the recovery return path. Keep the
private backup directory off the device: it contains the verified full recovery,
MISC backup and new pairing key. Installation requires the Encore paths to be
absent and does not modify unrelated data. It publishes the format selector
after full p12 read-back, then leaves the device in recovery without changing
MISC or rebooting. After an interruption, inspect the retained backups and
pending state; do not automatically rerun the install.

Fastboot is the demonstrated recovery entry point on the unlocked bench unit:
it can flash and select recovery. `fastboot boot` without flashing has not been
verified. A TWRP-free installer would still need a booted rescue environment
to verify/mount ext4, stage the files and verify full partition read-back.

The first boot checks the published selector and embedded rootfs digest before
starting the RAM root. If the active slot is ineligible or its archive is
missing or corrupt, recovery tries the other slot against the same digest. It
selects a verified fallback without spending an activation attempt or marking
it healthy. If neither slot qualifies, it stays in USB rescue. This is not an
update mechanism: both slots still use the starter's single pinned digest.
p16 stores only the new installation's state, which USB rescue does not depend
on. On each boot, Encore reads only `system_a`, verifies and copies the radio
files to RAM, then unmounts stock. There is no persistent duplicate, and neither
Encore slot writes either stock system partition. If import fails, base and
radios stay unavailable; USB rescue and `/run/encore/base.log` remain available.
From USB, after `base-ready`, creating
`/run/encore/enable-enrollment` starts networking and provisioning without audio.
Wait for `enrollment-ready` before separately requesting `enable-app` in the same
directory. The app service refuses missing/unreadable or unmuted physical
privacy and never retries on its own. Requests disappear at reboot. The slots
start `held`, not `good`; only authorized app activation spends an attempt and
only stable app readiness can commit a slot. Failed health leaves USB available
without rebooting. Physical boot, enrollment and runtime acceptance remain
separate from offline assembly and installer checks.

## Build locally

Use Linux, Python 3.12+, Go from `go.mod`, AArch64 cross GCC 14.2.0,
binutils, make, patch, flex, bison, libssl/libelf development headers, bc and dtc.
The initial build was checked using Debian GCC 14.2.0-19 and binutils 2.44.
Supply the exact GitHub source archive:
`https://api.github.com/repos/aslater3/LibreEcho-Linux/tarball/5bc811d3cd7091e8d70f9faa0bc12c5cf4e77ae0`.
The builder verifies SHA-256
`2d763595ea9299add581aad7bac95749b82b0705ff9c992ddf1ed16d52747087`;
a differently compressed GitHub archive may not have that checksum.

```sh
make biscuit-build
make biscuit-base
make biscuit-kernel BISCUIT_KERNEL_SOURCE=/path/to/retained/linux-source.tar.gz
```

The output directory must not already exist. It retains the patched source,
build tree, AArch64 Image, Biscuit DTB, effective configuration, System.map
and a hash manifest marked `NON_RELEASE_NOT_FOR_FLASHING`.
The kernel builder itself does not create an Android boot envelope or rootfs.
The builder checks config requirements after `olddefconfig`, and refuses a
manifest if recorded source inputs changed during compilation. It records
local build input hashes alongside the pinned kernel source revision.

`stp-read.patch` makes `/dev/stpbt` blocking reads wait for queued RX bytes
or a completed chip reset, not a latched notification. An empty wake is not
an I/O error. The starter uses the pinned `-encore-biscuit-arm64-stpfix1`
kernel with the retained `audiofix1` rootfs as its userspace input.

## Contracts implemented

- The native runtime requires this kernel's standalone privacy node
  and exact input names `mtk-pmic-keys`, `keys`, `action_key`. The latter two
  are platform node names used by `gpio_keys`, not `gpio-keys` compatible
  strings. All three groups are required. There is no silent legacy fallback.
- Speaker configuration selects `Speaker Codec Profile=Flat` before playback
  prepare. Existing conservative software gain/routing remains; no copied
  positive gain boost, Radar crossover or claimed acoustic calibration.
- Ring writes use `scaled_frame`, appending scale 3 (Imax/4) for every channel
  on **every** frame. The newer driver's legacy `frame` resets current to full;
  simply skipping the missing `led_current` write would be wrong. Ring
  animation, priorities, software PWM limits and channel orientation are unchanged.
- `capture.patch` is GPL-2.0-only, applies with zero fuzz to the pinned capture
  driver, and restores integer, volatile, read-and-clear `FPGA Overruns` and
  `Kernel Overruns` controls. Increments/get-and-clear share the same spinlock;
  counters saturate at LONG_MAX and survive stream restart until observed.
  SPI/invalid-frame loss is reported too. Producer/copy serialization and
  absolute produced/consumed accounting detect full-ring overwrite, rather than
  comparing modulo pointers. A failed user copy does not consume samples.
- Capture STOP sets running false; the worker never sets it back to true.
  ALSA `sync_stop`/`prepare` flushes outstanding work; prepare clears the buffer
  and positions before restart. Existing Encore privacy/loss gates are retained,
  not bypassed to make startup pass.
- The DT adds a distinct experimental Biscuit model and restores the three
  vendor Biscuit virtual-thermistor parameter tuples, with their provenance.
  Upstream raw-temperature thermal zones/efuse handling remain. Vendor weighted
  contributions are not exposed as calibrated enclosure temperature, and no
  unverified trips or GPIO polarity changes are invented.
- `usb-rescue` configures one configfs ACM gadget without Android USB nodes,
  partition writes, a rootfs dependency or mic-power GPIO changes. It refuses
  an unknown/partial gadget or an ambiguous controller, and waits boundedly
  for UDC/tty registration. It is included in both initramfs and RAM-root service
  composition, **not proof of USB enumeration or working hardware rescue**.

## Verification and remaining acceptance

The Biscuit boot image targets the MT8163 board and its bootloader, not QEMU's
generic ARM `virt` machine. A substituted VM can check userspace orchestration,
but does not establish that the actual Biscuit image boots or its drivers work.
No QEMU build target or emulation configuration is part of this device package.

```sh
go test -race ./...
python3 -m unittest discover -s devices/biscuit/packaging -p 'test_*.py'
python3 -m unittest discover -s devices/biscuit/packaging/kernel61 -p 'test_*.py'
sh -n devices/biscuit/packaging/kernel61/usb-rescue
```

The source pin is stable 6.1.178 plus LibreEcho's substantial device/vendor
patches, not stock mainline. Keep `ARCH_MTK_PROJECT="radar"` as upstream
provenance, not an assertion that the boards are identical. The existing
release validator must continue rejecting this single-DTB payload.

Before any authorized hardware trial: supply/verify the saved unit recovery
header and independent recovery plan; audit amp/DAC mux polarity and safe speaker limits;
verify privacy timing/electrical behavior, channel order, loss/drain/underrun;
qualify raw thermal readings/trips/cooling, sensor/aux-LED population, actual
input events and ring mapping; qualify WMT startup, firmware paths, factory
DT-IDME identity and BLE bridge on 6.1; then test simultaneous voice, Sendspin,
BLE and Wi-Fi plus reset, rescue and power-loss/rollback behavior. Existing
base firmware links include `/lib/firmware`, but that is not radio acceptance.
The RAM-root trial's ephemeral state is intentional, not production update or
power-loss qualification. Establish USB peripheral role/enumeration on Biscuit;
this code does not force GPIO35 or claim Radar's USB wiring is equivalent.

The kernel still embeds `i2s_to_spi_v34.bin` and the upstream experimental
recovered `amzn-fpga-oracle-tail.h`. Firmware grant/preferred-source inventory
and the tail's provenance/protocol justification remain unresolved. Preserve
the source snapshot and patches privately; do not publish these binaries as
a release or treat a newer kernel as removing vendor firmware obligations.

Board-specific kernel, DT, boot, routing, privacy-path and ring-current contracts
remain in `devices/biscuit`. Runtime ALSA, input, mixer and privacy-gate mechanics
use `platforms/linux`. Radar is a separate future board; common SoC ancestry
does not establish equivalent wiring or justify a speculative shared board API.
Historical retained userspace provenance is preserved, not renamed or relabelled.

## ARM64-first hardware trial

The historical Linux 6.1 ARMv7 trial is retained in Git history only. The
remaining RAM-root builder is AArch64, for offline assembly and supervised
bring-up; its output is not an installer input.

`arm64_probe.py` builds one full-feature kernel: CONSYS/Wi-Fi, stpbt/vhci,
ALSA/ASoC, microphone FPGA, privacy, buttons, ring and sensors.
`arm64_assemble.py` builds native AArch64 helpers and an Alpine 3.22 RAM root.
Without `--private-runtime`, this composition starts only USB rescue; it does
not run the historical one-shot peripheral sweep. That sweep and follow-up
bench helpers are not part of the maintained source or runtime.

Secondary CPUs remain disabled: the ARM32-only `mt-boot` PSCI/MTCMOS sequence
needs an ARM64 port. CPU frequency scaling/idle are also deferred. Four Kconfig
gates expose the existing clock, pinctrl, CONSYS and MT8163 SCPSYS implementations
to ARM64. The MT8163-specific power-domain provider is required; the generic
SCPSYS option alone does not bind this board's DT. The first physical ARM64
trial booted with working USB but lacked that provider: codec MCLK attachment
failed and the unprobed CONSYS platform device caused a runtime-PM NULL
dereference. Require `CONFIG_MTK_SCPSYS_MT8163=y`; WMT power entry points also
reject an absent platform device rather than dereferencing it.
The connectivity patch was made in its owning repository's `linux-6.1-in-tree`
branch at `c96a44de2474d9f9c086a40156c132ab81772e82` and exported here; it removes
an unused ARM32-only `asm/memblock.h` include and guards missing platform devices.
Do not edit that subtree in place.

The second physical trial (`6.1.178-encore-biscuit-arm64-hw2`) booted AArch64
with working USB and completed all 19 stages: 18 passed, playback timed out.
It registered 192 ALSA controls, captured 15,809 microphone frames (9 channels,
16 kHz, S24_3LE; samples discarded), scanned Wi-Fi and powered Bluetooth hci0.
Kernel taint remained zero. The playback diagnostic reached SETUP with zero
delay before its deadline: nonblocking DRAIN still returned EAGAIN after
completion. The shared ALSA implementation now checks native STATUS for SETUP
before accepting completion. One corrected RAM-only helper on the same hw2
boot submitted/drained 24,000 frames and passed in 2,406 ms within the unchanged
four-second budget. Amplifier Off, kernel taint zero and unchanged radio/USB
owners were verified afterwards, and the temporary helper was removed. This
is not acoustic, signal-quality, physical button/ring, Wi-Fi association or
sustained simultaneous-load qualification; the installed trial helper still
predates that drain correction, so the original full run remains 18/19.

Use the same pinned source archive as above and AArch64 GCC 14.2.0. The assembler
requires the retained Alpine v3.22 AArch64 `busybox-static-1.37.0-r20.apk` from
`https://dl-cdn.alpinelinux.org/alpine/v3.22/main/aarch64/` and checks its exact
APK/executable hashes. Preserve its GPL-2.0-only source/notice obligations before
distribution. Retain the native base's `lib/apk/db/installed`, authenticated APKs
and hashes; never relabel ARM32 package evidence as AArch64. The trial includes
ALSA utilities, BlueZ 5.82 and wpa_supplicant, not PulseAudio or PipeWire.
`../base.sh OUTPUT.tar.gz` builds the AArch64 package base. The authenticated standalone apk tool can build the
same package set where a Docker daemon is unavailable.

The SPI microphone driver requests `i2s_to_spi_v34.bin` during kernel device
probe, before the store mounts. Pass the pinned source archive's retained
`firmware/i2s_to_spi_v34.bin` to the assembler: it checks SHA-256
`77a558bacdaaf9e343f02f2d74f27a5f2bb2dc8b6d66cc2499b60ed14ef62fe6` and includes
it in both initramfs and RAM root `/lib/firmware`. Its redistribution/source
grant remains unresolved; this is a private hardware trial, not a release.

```sh
python3 devices/biscuit/packaging/kernel61/arm64_probe.py \
  --source-archive /path/to/retained/linux-source.tar.gz \
  --output .amp/in/orb-backup/biscuit-arm64-kernel
```

The installed Kaeru `ab93750` payload contains the executing older LK, not the
generic `lk_a/b` image. Its embedded LK SHA-256 is
`2fafb90c5d3a7a844fb784ddc92851dc5ef18dbe868b7635814de08a16b7b30f`.
The decoded ARM64 path inflates gzip (28 MiB bound), takes the destination from
the Android header and locates an appended DTB with root `version` `evt`.
The assembler preserves the audited unit header except sizes/ID, sanitized
command line and deliberate kernel address `0x40000000`/selector
`bootopt=64S3,32N2,64N2`. It checks Image alignment, ATF/ramoops/staging/ramdisk/DT
windows and the 16 MiB recovery-partition bound. It invokes no device tools.
These layout checks are not proof the physical ARM64 handoff succeeds.

Supply `--kernel-directory`, `--base-rootfs`, `--base-sha256`, `--busybox-apk`,
`--microphone-firmware`, `--template` and `--output` to `arm64_assemble.py`. Stage the separate checksummed
rootfs at `/store/encore/arm64-probe/rootfs.tar.gz`, leaving ARM32's rootfs intact;
init mounts the store read-only without journal replay. The demonstrated test
route replaces recovery, then explicitly reboots to recovery. Cold Volume Down
→ fastboot → saved TWRP returns independently of Linux USB. No bootloader or
supporting partition change is required.

### Integrated private ARM64 runtime

`arm64_assemble.py --private-runtime DIRECTORY` consumes the sealed native
pre-rename runtime manifest/binary/overlay inputs, checking every recorded
source and binary hash before composition. It keeps the same hw2 Image and
DTB, but targets the separate `/store/encore/arm64-runtime/rootfs.tar.gz`.
It does not stage that file or flash/reboot anything. Preserve the previous
rootfs/recovery files when preparing this trial.
The runtime retains `speaker-office`, prior manufacturer/model metadata and
the prior BlueZ implementation without an adapter Alias write. Music and
Bluetooth proxy remain off; no ARM32 Sendspin helper is introduced.

BusyBox init owns USB independently, plus separate guarded base, WMT, WPA,
foreground DHCP, Bluetooth, network-manager and app services. Each gets one
attempt per boot; a failed service parks rather than respawning peripheral
access repeatedly. The base verifies firewall/USB before enabling radio
dependencies. WPA uses its normal RAM config path, DHCP owns `/run/udhcpc.pid`,
and the unchanged app entry path waits for DHCP and Bluetooth before normal
`PrepareNetwork`/`Run`. No saved Wi-Fi means physical-Action-authorized Improv,
even if some external mechanism has already associated wlan0. Provisioning
readiness is the fresh PID/heartbeat in `/run/encore/provisioning-ready` after
GATT registration/advertisement/BlueZ health; `/run/encore/ready` is later voice
startup readiness and cannot substitute for it. Config/keys/bonds/credentials
stay in RAM. Rootfs regular files must fit 96 MiB of its 128 MiB tmpfs.

Runtime USB shells use the retained BusyBox `setsid -c` to acquire a controlling
TTY, with explicit script input rather than an interactive line editor that
can request cursor-position responses from a framed serial client. RAM-root
and initramfs rescue both expose a single ACM console. USB rescue does not
depend on WMT, WPA, BlueZ, the app, or a working store, but still requires a
functioning kernel and USB controller.

### Private ARM64 playback SRAM repair

`playback-sram.patch` is applied after the capture/ARM64/connectivity patches
by the default Biscuit kernel builder.
The hw2 playback driver exposed Device-memory SRAM as `runtime->dma_area`,
letting generic ALSA silence use ordinary RAM memset during RUNNING SW_PARAMS.
The repair leaves that RAM pointer null and installs byte-based I/O-safe
`fill_silence` and `copy_kernel` operations through `pcm_construct`, preserving
ASoC's remaining operations. Linux 6.1 has no component-level hooks for those
two operations. Existing prepare/user-copy retain `memset_io`/`memcpy_toio`;
the advertised signed formats use zero silence, and MMAP is not advertised.

The patch adds a source-extracted SW_PARAMS/silence regression to LibreEcho's
contract runner. It covers signed formats, byte/frame conversion, ring wrap,
bounds, locked execution and kernel-copy; the unchanged driver is a negative
control. These host contracts and an AArch64 kernel build are not physical
SRAM qualification. The private `audiofix1` composition uses a distinct kernel
localversion and rootfs path, and boots USB plus base only: no automatic
radio/network/app/PCM startup. Its separately sealed handoff carries exact
inputs, build/assembly recipes, hashes and a manual fault-path diagnostic.
Keep the previous sealed images and recovery assets. A fresh healthy boot and
independent USB/health audit must precede any bounded PCM regression; no
Bluetooth fix or acoustic/capture/provisioning qualification is implied.

One separately approved physical `audiofix1` regression has now passed with
the corrected `pcm-silence-check2`: PREPARED through zero prefill, explicit
START, RUNNING/empty queue, boundary-sized RUNNING SW_PARAMS silence, continued
RUNNING and DROP. Exact kernel/taint0 and independent USB remained healthy,
with no new fault signatures, D-state or PCM owner. This qualifies that narrow
fault path only, not audible playback/Drain, capture, privacy, gain, thermal,
coexistence, general runtime or BLE enrollment. The normal codec lifecycle
unmutes/mutes; zero samples do not prove amplifier-off or acoustic safety.

The sealed `audiofix1` userspace also exposed an early bootstrap dependency:
`runtime-service` needed mkdir before base installed BusyBox applet symlinks.
Its source now invokes BusyBox explicitly for mkdir and sleep, including
failure/parking paths; the regression begins with an empty applet PATH. The
physical boot used a RAM-only applet installation workaround. Existing sealed
images are not changed by this source correction; a later separately prepared
package must include it for autonomous startup on the next boot.

### Persistent candidate activation hold

The shared `packaging/slotctl` and `health` now recognize an explicit `held`
candidate state for supervised bring-up. Repeated boots select that slot
without spending trial attempts, and health neither commits it nor runs its
failure/reboot budget, even if a readiness marker exists. Explicit `slotctl arm`
requires the booted slot to equal the active held slot. It transitions to
`trial 1`: the running boot is the first attempt, with one subsequent attempt
remaining. Repeating arm cannot replenish that budget. Stable runtime health
can then commit normally; a held slot cannot be committed directly. Existing
install/update defaults remain ordinary trial states, not held.

The volatile ARM64 trial output remains incompatible with the installer. Use
only the distinct persistent starter format described above for a transition.
