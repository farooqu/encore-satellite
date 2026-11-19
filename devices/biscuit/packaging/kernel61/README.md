## Historical ARMv7 bring-up (not a supported build or install path)

`assemble.py` accepts the freshly built kernel/runtime artifact and an explicitly
hashed retained Encore userspace ZIP. Its original manifest (including its old
target, revision and dirty-source flag) remains nested dependency provenance;
it is never relabelled as a new Biscuit release. The output has the distinct
format `encore-biscuit-kernel61-trial-v1`, not the release/OTA format.

```sh
python3 devices/biscuit/packaging/kernel61/assemble.py \
  --kernel-artifact /path/to/new/kernel61-build \
  --userspace-bundle /path/to/retained/userspace.zip \
  --userspace-sha256 RETAINED_ZIP_SHA256 \
  --output .amp/in/orb-backup/biscuit61-trial
```

This produces `rootfs.tar.gz`, `initramfs.gz`, `kernel.zImage-dtb` and a new hash
manifest. It verifies the retained rootfs, kernel artifact and current runtime
source hashes; it replaces runtime and boot/service scripts from this checkout.
It preserves the Alpine package inventory and dependency notices. It does not
fetch/extract firmware or contain a staging/flashing command.

Normal assembly keeps automatic Encore startup, with a USB-first control
plane. Add `--rescue-only` to assemble an optional diagnostic envelope instead:
it enters the initramfs serial shell before mounting p15 or extracting the
rootfs, without starting WMT, Wi-Fi, Bluetooth or Encore. Kernel probe-time
hardware/FPGA initialization still occurs; this is not an electrically inert
kernel. Its manifest records `boot_mode: rescue-only`; normal output records
`full-service-usb-first`. Use separate new output directories for each.

An **optional, already retained unit-specific recovery image** can be passed as
`--recovery-template /path/to/saved/recovery.img --template-sha256 SAVED_SHA256`.
No default template or invented addresses are used. It requires Android v0,
an ARM32 bootopt, a raw zImage without an MTK wrapper, and no second/outer-DT
sections. It preserves addresses and checks the new compressed/decompressed
kernel, ramdisk and tags against the artifact's DT RAM/ATF/ramoops layout;
it removes root/skip-initramfs/init overrides in both command-line fields,
sets `rdinit=/init`, recomputes Android's SHA-1 ID and bounds the output to
16 MiB. Otherwise **no `trial-recovery.img` is generated**. Synthetic headers
test the code; they are not a substitute for the unit's saved image.

For a private **header-only transfer**, supply the complete saved header page
as `--recovery-template`, its SHA256, and
`--template-metadata /path/to/owner-attested.json --metadata-sha256 METADATA_SHA256`.
The metadata must give `header_page_bytes`, `header_page_sha256`,
`full_saved_file_bytes`, `full_saved_file_sha256`, `kernel_first48_hex`,
`kernel_first48_sha256`, and `section_bounds` (kernel offset/end/aligned_end,
ramdisk offset/end/aligned_end, second offset/size, and
`all_sections_within_saved_file`). The builder cross-checks bounds against the
received header and verifies the raw-zImage prefix. It neither fabricates an
old kernel/ramdisk nor fetches the original backup. The manifest distinguishes
locally verified header/metadata hashes from the **owner-attested** original
payload and full-image hash; hashes do not establish boot authenticity.

Offset 40 must be zero (v0 header version / legacy unused or DT-size word).
Offset 44 is AOSP v0's `os_version`, **not another header version**; preserve
its bytes, including nonzero values. See the
[AOSP Android 8.1 header](https://android.googlesource.com/platform/system/core/+/android-8.1.0_r1/mkbootimg/bootimg.h).
Inherited metadata is not the new Linux image's OS version or security claim.
The ramdisk may occupy audited RAM on either side of ramoops; it must avoid
ATF, ramoops and the reserved 64 KiB tags window. The DT reservation inventory
and empty explicit reservation table are checked, including CONSYS's dynamic
2 MiB allocation. In the pinned source, `arch/arm/mm/init.c:arm_memblock_init`
reserves the kernel/initrd before `early_init_fdt_scan_reserved_mem`, whose
`drivers/of/of_reserved_mem.c` allocator uses memblock to avoid those ranges.
This is an offline source/layout check, not proof of bootloader behavior,
unreported firmware reservations or usable recovery on a real unit.

The trial initramfs expects the matching archive at
`/store/encore/kernel61-trial/rootfs.tar.gz` and existing retained firmware at
`/store/encore/firmware`. This is an offline layout contract, **not authorization
to put files on a device**. The initramfs mounts only p15 as `ro,noload` (no
journal replay), checks an embedded rootfs SHA-256 and unpacks it into a bounded
128 MiB tmpfs. Current file contents must fit within 96 MiB before packaging;
directory/page overhead and total runtime memory still need hardware measurement.
It moves proc/sys/dev/run/store into that RAM root. A corrupt archive, missing
storage or failed mount/extraction enters rootfs-independent configfs ACM
rescue and never automatically reboots. A failed mount transfer
attempts to move the already-transferred mounts back before rescue.

The PID-1 wrapper enters BusyBox init immediately. Its inittab independently
respawns the USB console and starts the base once; voice, Bluetooth, SSH and
network workers wait on base readiness. Base startup applies the firewall and
waits boundedly for a live USB-console owner after gadget/tty registration
before enabling radios. It does not wait for a host to connect. Missing USB or
failed base/radio startup leaves the console supervised, not blocked behind
that failure. Startup diagnostics are in `/run/encore/base.log`. This follows
LibreEcho's USB-before-connectivity ordering while keeping Encore's serial
protocol, BusyBox service model, application and local board contracts.

The console exclusively locks its ownership, closes the lock in interactive
children and reuses only its complete matching configfs composition on
respawn. It never rebinds/recomposes unknown, partial or ambiguous gadgets.
Kernel/UDC failure is still outside this software rescue guarantee; the proven
3.18 USB rescue cannot establish 6.1 enumeration or recover a kernel that never
initializes USB. Retain the existing backup and establish the actual loader
return method before a separately authorized boot/flash; do not guess a
fastboot method or write an old marker into expdb.

`/data` and `/tmp` are separate bounded tmpfs mounts;
credentials, bonds, configuration and keys intentionally disappear on reboot.
The tagged runtime does not construct or advertise its release OTA controller.
There is no slot-health commit, MISC selection, recovery restoration, expdb
write, automatic flash operation or persistent A/B policy in this experiment.
It is not a new production recovery mechanism: an authorized later trial must
establish its boot-chain generation and preserve a working recovery separately.

The trial Bluetooth service reads
`/sys/firmware/devicetree/base/idme/bt_mac_addr/value` rather than `/proc/idme`.
This follows the vendor IDME child's `value` property (legacy `idme.c`) and the
6.1 Wi-Fi driver's direct `/idme/*` value reads. The existing validated-address
and WLAN-derived local-address fallback remains; no factory values are baked
into the image. Bootloader population of DT-IDME and actual radio identity
remain hardware acceptance, not established by an offline archive.

## Contracts implemented

- `-tags=biscuit_kernel61` selects only this kernel's standalone privacy node
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
go test -race -tags=biscuit_kernel61 ./devices/biscuit ./platforms/linux/...
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

