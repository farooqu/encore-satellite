# Biscuit illuminance: explicitly deferred

Umer's decision on 17 November 2026: **“Keep lux deferred until the driver
interface is safe.”** Biscuit (Echo Dot 2) lux is deferred, not conditionally
ready. There must be no production runtime wiring or entity advertisement,
and it is not an initial-release gate.

`illuminance_linux.go` is retained as a local, fixture-tested candidate reader
for `/sys/bus/i2c/devices/0-0039`, not an enabled Biscuit feature. Reconsideration
requires acquisition-error propagation, lock-safe conversion/CPL checks,
freshness/autogain review and exact Encore-image qualification as detailed below.
This decision does not authorize kernel changes or hardware work.

Existing Android/shared local support is separate. Neither its implementation
nor this decision authorizes shipping or integration, or implies Biscuit support.
The TSL2540 candidate is Biscuit-specific; shared Linux helpers do not imply
sensor support on Radar (Echo 2) or any other board.

## Audited source and units

On 17 November 2026, downloaded Amazon's
[Echo_Dot_src-6.5.7.1-20251024.tar.bz2](https://fireos-audio-src.s3.amazonaws.com/dMUQiRDxI3hFuRDaF0WTumrp71/Echo_Dot_src-6.5.7.1-20251024.tar.bz2)
and checked SHA256
`2f6b7eed8c09cecf7633f01909c6a4085bef691c29ed0d106c75e7b48c7b4721`,
matching `packaging/kernel-inputs.json` and its pinned upstream recipe. Extract
`platform.tar`, then prefix the paths below with
`kernel/mediatek/mt8163/3.18_hl/`:

- `drivers/misc/ams_tsl2540.c`, SHA256
  `7fb81e524e4a1b30862c93bd1eb216ace515e5a834262d662fad969683e301a0`.
  Lines 50–55 and 684–710: probe accepts ID masked with `0xfc` = `e4`, AUXID
  `01`, revision masked with `0xe7` = `61`. Binding is stronger than an I2C
  name/DT node. ID reads themselves do not propagate bus errors, although an
  accepted tuple and subsequent successful initialization are required to bind.
- `drivers/misc/ams_tsl2540_als.c`, SHA256
  `250d8c237cf9d32813c82df30256d762278206b7d2a372561370422176511ca6`.
  Lines 76–91 read two little-endian optical channels. Lines 104–128 calculate
  CPL using integration time, gain, gain extension and `d_factor` (integer
  arithmetic). Lines 272–341 optionally subtract configured dark baselines,
  compute two weighted channel combinations divided by CPL, choose the maximum
  and clamp to 0–65535. Default coefficients describe **NO GLASS**. Lines
  433–448 expose the resulting integer through `als_lux` with no additional
  sysfs scale. Preserve that driver-estimated lux value; do not divide by 1000
  or advertise raw channels as lux. This is not independently calibrated lux.
- `include/linux/i2c/ams_tsl2540.h`: integration-cycle constant 2816, maximum
  lux `0xffff`. Runtime parameters, baseline build option and housing affect
  conversion; source intent does not establish absolute accuracy on a unit.

The recipe provenance connects this source to Encore's current kernel inputs;
it does **not** prove binary/source identity for the already running Techo5 image.
The retained exact-unit probe tuple and cover/uncover response are compatible
with this source, not proof that every driver bit is identical.

## Why production wiring is withheld

`tsl2540_device_als_lux` ignores the return from `tsl2540_read_als`. A bus error
can therefore return a successful-looking value calculated from old channels.
There is no conversion timestamp/counter in this ABI. Repeated equal readings
cannot distinguish steady illumination from stale data. Sysfs mtime cannot fix
this. `tsl2540_get_lux` also divides by CPL without a zero guard, and can change
gain/write registers internally when autogain is enabled.

The adapter checks the audited software-only `als_power_state` and `als_cpl`
show callbacks before `als_lux`, never writes sysfs, and never opens I2C or
calibration/debug attributes. Those separate checks reduce risk but cannot
atomically guarantee safe driver state. Its observation timestamp means only a
completed sysfs read, **not proven fresh optical acquisition**. Application-level
read/permission/parse/timeout failures clear state, but hidden kernel failures
cannot honestly be detected by this reader. It must not be enabled as a live
freshness-qualified entity with this legacy ABI.

Prerequisites for reconsidering the deferral, not authorized next actions:

1. Provide/audit a driver measurement callback that propagates acquisition errors,
   rejects disabled/uninitialized conversion safely under its lock, and provides
   a defensible acquisition/freshness contract. Review autogain side effects.
   A kernel change/build/qualification is separate work, not silently included.
2. Qualify that exact Encore boot image's enabled driver, successful hardware-ID
   probe/binding, measurement node and read access. Do not infer them from DT,
   this unit's Techo5 readings or the failed `tsl258x` probe at `0-0029`.
3. Under separately authorized hardware access, verify samples, cover/uncover,
   failure/recovery and voice/music coexistence. Absolute accuracy additionally
   requires a calibrated reference meter and controlled geometry/illumination.

No physical device access, driver changes, runtime enablement or image changes
were performed for this implementation. Fixtures exercise discovery, driver
units, darkness, malformed/out-of-range values and reported read failures only.
