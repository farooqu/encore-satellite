# Optional TSL2540 measurement safety patch

**Dot illuminance remains deferred.** This is a source-level, transaction-safe
measurement ABI candidate, not hardware qualification. The patch is intentionally
not part of the current AArch64 kernel, wired to an adapter, advertised as a
capability, or required for Dot bring-up. It targets the retired 3.18 tree. No running device
was accessed. This does not audit the vendor driver's entire debug/control ABI.

## Pinned source

Amazon's [Echo Dot archive](https://fireos-audio-src.s3.amazonaws.com/dMUQiRDxI3hFuRDaF0WTumrp71/Echo_Dot_src-6.5.7.1-20251024.tar.bz2)
has SHA256
`2f6b7eed8c09cecf7633f01909c6a4085bef691c29ed0d106c75e7b48c7b4721`.
Inside `platform.tar`, the kernel root is `kernel/mediatek/mt8163/3.18_hl`.
`verify.py` pins the two modified C files and three headers individually,
checks the original cached-read failure, and applies both patches with zero fuzz
only to a temporary private copy. The source matches the Encore recipe; it is
not proven bit-identical to the running Techo5 kernel.

Vendor copyright/GPL notices remain in the patched source. The harness is
GPL-2.0-or-later. Corresponding-source and other publication obligations still
apply to any future binary distribution; no publication is performed here.

## Follow-up fixes after source review

Apply `0002-teardown-and-write-errors.patch` after `0001-measurement-abi.patch`.
The first candidate remains byte-for-byte frozen with SHA256
`3aa5ff967f900009ea2fc78bde728069b3c11bfe383d4ed4bba7a66088f26924`;
its earlier compile-only image hash below is historical, not the revised image.
Neither patch is connected to the kernel recipe.

The follow-up fixes both inherited defects found in review:

- Unbind removes/drains the ALS and raw-register sysfs groups before releasing
  IRQ/input callbacks, platform resources or chip state. It does not hold the chip
  mutex while draining callbacks. Probe failures unwind only acquired resources,
  including optional input and interrupt paths. DT-allocated platform data uses
  device-managed allocation; caller-provided platform data remains borrowed.
- Gain, integration, wait, autozero and persistence stores propagate their
  full-flush bus errors. An earlier gain-write failure is not overwritten by a
  later flush. Partial flush failures invalidate readiness and software enable
  state. Retrying a setting alone does not reenable measurements: successful
  explicit enable and the normal holdoff are required. This is a kernel control
  contract, not authorization for userspace adapters to write settings.

The ABI v1 format, units, bus-only freshness limits and hardware qualification
gates below are unchanged. This is not a general audit of all vendor controls.

## ABI v1

The read-only `als_measurement` attribute is next to the existing ALS attributes
on the bound I²C device. It returns one ASCII line:

```text
1 sequence start_ns end_ns lux ch0 ch1
```

- `1` is the format version. Exactly seven unsigned decimal fields, single spaces,
  and a final newline are emitted.
- `sequence` counts **successful safe acquisitions**, modulo 2⁶⁴. It starts at 1
  after allocation/probe and resets on reprobe/reboot. Zero is valid on wrap.
  `als_lux` and `als_calibrated_lux` also consume this per-device counter; IRQ
  reports do not. Gaps are valid. Consumers cannot compare counters across binds.
- `start_ns` is sampled immediately before the four-byte channel bus read;
  `end_ns` is sampled after successful conversion. Both use `ktime_get_ns()`:
  kernel `CLOCK_MONOTONIC`, nanoseconds since boot, excluding suspended time.
  Equal timestamps are possible at clock resolution. They bracket this bus read
  and conversion, not the integration/exposure time or the preceding config reads.
- `lux` is the driver's integer estimated lux, clamped to 0–65535, **scale 1**.
  There is no userspace division by 1000, new calibration, or accuracy claim.
  Existing baseline subtraction and the vendor CPL formula are retained. Wide
  arithmetic corrects overflow; undefined/wrapped old results are not preserved.
- `ch0` and `ch1` are the raw unsigned 16-bit channels from the same complete
  four-byte bus transaction. The record and conversion are serialized with driver
  configuration and IRQ/gain changes by `chip->lock` through formatting.

A sequence/timestamp proves a complete new **bus acquisition**, not a distinct
optical integration, ADC progress, cross-channel hardware latching, or an upper
bound on the age of the sensor's register contents. Repeated equal values are
valid. A stuck ADC can still serve unchanged registers successfully. Do not label
these fields as optical sample timestamps or use sysfs mtime as sample freshness.

### Errors return no record

- `EAGAIN`: probe not initialized, suspended, unpowered, disabled in software or
  hardware, or configuration holdoff still pending.
- `EBUSY`: driver autogain mode is enabled. The reader does not disable it.
- `ERANGE`: zero/unrepresentable CPL, zero `d_factor`, or negative baseline.
- `EIO`: short channel/config read, or hardware config differs from its shadow.
- `EOPNOTSUPP`: hardware sleep-after-interrupt is enabled (PON can otherwise look
  enabled while the oscillator sleeps).
- Negative I²C errors propagate unchanged. No counter advances on failure; no
  cached lux record is emitted. Sysfs may re-serve the same formatted buffer on
  partial reads of one open file, as usual; reopen/rewind for another acquisition.

The reader verifies actual ENABLE, ATIME, WTIME, CFG0/1/2 and AZ_CONFIG against
the locked shadow, checks CFG3 without reading/clearing STATUS, recomputes CPL,
and requires exactly four channel bytes. No measurement-read path writes any
sensor register or invokes autogain. Access through another driver, direct I²C,
or a second bus master is outside this locking contract.

### Autogain and settling are deliberately restricted

The old `get_lux` mixed conversion with gain/register writes and ignored their
errors at its sysfs callers. Conversion is now separate. `als_lux` uses the same
safe acquisition/error contract while retaining its one-integer text format;
it now refuses autogain mode instead of changing gain as a read side effect.
The calibrated legacy reader also propagates failures and guards its coefficient;
it is not the proposed ABI. The separate legacy `als_ch0`/`als_ch1` attributes
remain cached diagnostics, no longer invoke conversion/autogain, and are not a
fresh/coherent measurement pair.

Legacy IRQ reporting can still request automatic gain changes. Its acquisition
and writes are locked, failed/short acquisitions are not reported, gain enable
read/disable/config/restore errors are checked, and failed gain writes invalidate
enabled state. These control paths do not establish the new read ABI's freshness.
The optional interrupt path is not enabled by the pinned biscuit build.

Before configuration/gain/power writes, the driver invalidates the read holdoff,
including raw `regs` writes and suspend. Failed enable writes leave ALS disabled
in software. The first subsequent matching configuration observation returns
`EAGAIN`, then holds off for two cycles computed from `(ATIME + 1)` integration
steps plus enabled `(WTIME + 1)` wait steps (×12 for WLONG), at 2.90 ms per step.
The +1 is used only for the holdoff, not to recalibrate the vendor lux formula.
The longest holdoff is 19.3024 seconds. Further configuration writes restart it.

This rejects immediate first-integration reads but is only a **minimum software
holdoff**. It does not establish an autozero completion bound or prove that a
conversion completed. Those remain qualification gates. The published
[TSL2540 datasheet DS000564 v2-00](https://look.ams-osram.com/m/d83ec5eb2896cf6/original/TSL2540-DS000564.pdf)
describes integration/wait timing on pages 6 and 13–14, STATUS interrupt flags
on page 20, sleep-after-interrupt on page 23, and autozero on page 24. STATUS is
not treated as a per-integration AVALID bit or every-integration notification.

The source's existing early return in `resume` is not repaired here: after the
driver marks itself suspended it fails closed rather than claiming availability.
Suspend/resume usability must be resolved/qualified separately before enabling
this optional feature. Other legacy control/debug defects are not a blanket
security guarantee from this patch.

## Reproduce the source harness

Requirements: Python 3, GNU patch, GCC with pthreads/UBSan. For `--m32`, install
`gcc-multilib` (including 32-bit libc and UBSan runtime). From the repository root,
with `SOURCE` naming an **unmodified**, extracted pinned kernel root:

```sh
python3 devices/biscuit/packaging/tsl2540-safety/verify.py "$SOURCE" --m32
```

The harness includes the entire actual ALS C translation unit and actual driver
header, not a rewritten lux algorithm or selected extracted functions. It mocks
kernel/sysfs/I²C boundaries and uses pthread mutexes and a controllable clock.
It reproduces the original successful-cached-lux error, then checks the patched
ABI natively and with 32-bit host arithmetic under UBSan: channel/config errors
and short reads, disabled/uninitialized/suspended states, metadata/repeated values/
sequence wrap, zero-lux recovery, zero/overflow CPL, high-channel arithmetic and
clamping, auto mode/read-side writes, control failures, holdoff boundary, and a
configuration writer contending with acquisition/conversion. None models optical
physics, bus timing or a booted kernel.

The verifier also reproduces both reviewed defects against the frozen first
candidate, before applying the follow-up. It injects faults at all ten gain-write
positions and all seven flush positions for integration/wait/autozero/persistence,
checks negative results and disabled measurement until explicit recovery. A second
harness compiles the actual entire main and ALS translation units separately.
It exercises bind/unbind/rebind, concurrent measurement/raw-register reads,
rejection of new reads during removal, DT/borrowed ownership, and probe failure
unwinding at sysfs/input/IRQ boundaries, with neither option, input only, IRQ only
and both, natively and in 32-bit mode. Its sysfs model deactivates and drains
callbacks according to the pinned kernel's `fs/kernfs/dir.c:333–360,983–1028`;
it does not run real kernfs or prove absence of all possible kernel races.

## Kernel compilation and remaining gates

Apply `0001-measurement-abi.patch`, then `0002-teardown-and-write-errors.patch`,
with `patch -p1 --fuzz=0` only in a disposable
copy of the verified source. Use the exact AOSP arm-eabi 4.8 toolchain commit
from `../kernel-inputs.json`, the pinned recipe's biscuit configuration, and its
generated Trapz header/certificate preparation described in `../KERNEL-SOURCE.md`.
After that preparation, with `OUT` an absolute output path and `TOOLCHAIN` the
pinned cross-toolchain checkout, the source-only stock-config check is:

```sh
make -C "$SOURCE" O="$OUT" ARCH=arm CROSS_COMPILE="$TOOLCHAIN/bin/arm-eabi-" \
  HOSTCFLAGS=-fcommon USE_TRAPZ=true LOCALVERSION=-tsl2540-safety olddefconfig
make -C "$SOURCE" O="$OUT" ARCH=arm CROSS_COMPILE="$TOOLCHAIN/bin/arm-eabi-" \
  HOSTCFLAGS=-fcommon USE_TRAPZ=true LOCALVERSION=-tsl2540-safety -j4 zImage-dtb
```

Original first-candidate checks on 2026-11-17 (follow-up results recorded below):

- Native and 32-bit UBSan harness: passed; original cached-lux failure reproduced
  before applying the patch. Patch dry-run/application used zero fuzz.
- Full ARM build: exit 0, `Kernel: arch/arm/boot/zImage-dtb is ready`, release
  `3.18.19-tsl2540-safety`. Compiler warnings, 28 modpost section mismatches and
  a nonfatal size-helper path warning were emitted; this was not a clean build.
- ARM driver objects with `KCFLAGS=-DTSL2540_ENABLE_INTERRUPT`: compiled, not
  executed. The normal build leaves that optional path disabled.
- Existing packaging suite: 16 tests passed at the pre-rename device path using
  local fixtures, not a device. `python3 tools/check_boundaries.py`: passed.

The private compile-only `zImage-dtb` SHA256 was
`b40f22251d9b5b183d3e3e2fc4d8ec5abdfce36720cb70b80339303724cb6c0f`.
It is not an Encore recipe change, qualified image, or artifact for
flashing/publication. A build with the complete Encore recipe and
provenance must be separately authorized and retain this patch in corresponding
source and build-input identity before any future image qualification.

Follow-up checks on 2026-11-17:

- Native/32-bit UBSan measurement suites and the eight lifecycle variants pass;
  the original cached-read bug and both first-candidate defects reproduce first.
  Recovery tests reject successful reads even beyond the maximum holdoff until
  explicit enable, so settling cannot hide an accidental reenable.
- Full revised stock-config ARM `zImage-dtb` build: exit 0,
  `Kernel: arch/arm/boot/zImage-dtb is ready`, release `3.18.19-tsl2540-safety`.
  Compiler warnings and 28 section mismatches remain; this is not a clean build.
- ARM main/ALS objects compiled in input-only, IRQ-only, both-enabled and stock
  configurations, using `KCFLAGS` defines `TSL2540_ENABLE_INPUT` and/or
  `TSL2540_ENABLE_INTERRUPT`. Stock objects were restored afterward. These are
  compilation checks, not ARM runtime or concurrent interrupt execution tests.
- Existing 16-test local-fixture packaging suite and dependency-boundary check:
  passed. No hardware was used.
- Independent re-review found no driver-code blockers. Its P3 observation about
  settling masking a recovery-test failure was addressed as described above;
  re-review confirmed it resolved with no remaining actionable findings. The
  reviewer injected five private mutations that incorrectly reenable after
  successful gain/itime/wtime/autozero/persist stores; all ten native/32-bit
  executions failed the new beyond-holdoff assertions. These are harness checks,
  not physical fault injection or a comprehensive vendor-driver audit.

Follow-up patch SHA256:
`6be512512edfdf05246c9c81fdf2436c07904413b94a3e131ce8fd375d58a737`.
Revised private compile-only image SHA256:
`3e25c74ff395d296f35888fa7bb7ff77138dd389d5f243a484f1eca45090f845`.
Local reproduction logs are retained under `.amp/in/orb-backup/tsl2540/` as
`followup-harness.log` and `followup-arm-{full,input,irq,both,stock}.log`.

## Biscuit relocation verification

The optional directory now lives at `devices/biscuit/packaging/tsl2540-safety/`
on the published Biscuit-renamed base. Both patches and all five harness/verifier
files remain byte-identical to the reviewed candidate; only README path references
and this relocation record changed. No recipe or adapter wiring was added.

Re-ran the verifier from the new path with `--m32`: all three baseline defects
reproduced, then native/32-bit measurement suites and all eight lifecycle variants
passed. `python3 -m unittest discover -s devices/biscuit/packaging -p
test_package.py` passed all 18 local-fixture tests, and
`python3 tools/check_boundaries.py` passed. The old safety-directory path is absent.
The kernel source/patch bytes did not change; ARM builds were not repeated for
this path-only move. Earlier ARM evidence remains as recorded above.

Relocation logs are `.amp/in/orb-backup/tsl2540/biscuit-relocation-harness.log`
and `biscuit-relocation-packaging.log` alongside it. The existing review-v2
archive is frozen historical evidence and intentionally retains pre-rename paths.

Before any Dot lux integration, separately authorize and complete:

1. Review/resolve physical conversion age/progress, channel coherence, autozero
   completion and first-integration settling on the exact intended hardware.
2. Resolve/verify power and suspend/resume behavior and gain/integration failure
   recovery. No automatic adapter writes are authorized by this patch.
3. Build and retain an exact Encore image/source/provenance set, then qualify that
   artifact on hardware: dark/covered/uncovered repeats, zero/bright boundaries,
   read faults/recovery, concurrency and configuration/PM transitions. Prove no
   measurement-induced register writes on the real path.
4. Obtain separate adapter/production integration authorization. A successful
   source harness or compilation does not lift deferral or expand initial release.
