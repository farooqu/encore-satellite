# Historical Echo Dot 2 Linux 3.18 kernel source kit

The Linux 3.18 release path is retired. This file documents the old source kit
and the firmware exception, not the [current AArch64 build](kernel61/README.md).

`source.tar.gz` accompanied each legacy kernel artifact. It contains the
patched kernel source (`src/`, including its COPYING and component notices),
the upstream build recipe/configuration/security patches, Encore's build inputs,
the effective `.config`, and Amazon's generated Trapz header and build certificate.
It does not contain Wi-Fi/Bluetooth firmware or the compiled cross-toolchain.
`provenance.json` identifies and hashes the source kit, configuration, System.map
and actual kernel bytes. Build-input identity is not a promise of identical bytes
on different build environments; retain the exact artifact you qualify.

## Embedded audio FPGA firmware has an owner-approved release exception

The retained config sets `CONFIG_EXTRA_FIRMWARE="i2s_to_spi_v34.bin"` and
`CONFIG_EXTRA_FIRMWARE_DIR="firmware"`. The source kit contains that 30,964-byte
bitstream at `src/firmware/i2s_to_spi_v34.bin`, SHA-256
`77a558bacdaaf9e343f02f2d74f27a5f2bb2dc8b6d66cc2499b60ed14ef62fe6`.
The matching kernel map contains its embedded firmware symbols. This is audio
FPGA firmware, distinct from the owner's separately supplied radio firmware.

No firmware-specific redistribution grant, preferred source or license has been
established for this bitstream. Inclusion in the vendor archive and the kernel's
GPL COPYING do not by themselves establish its terms. Umer approved a documented
good-faith redistribution exception for this exact binary for both development
and production distributions on 2026-11-17. The firmware remains `NOASSERTION`;
the exception accepts unresolved risk, not a license grant or legal clearance.
See [the complete decision and limits](FPGA-PROVENANCE.txt). Preserve that record
with distributed kernels/images and source kits. An older immutable source kit
must remain unchanged; provide the current decision alongside it.

The original Amazon `Echo_Dot_src-6.5.7.1-20251024.tar.bz2` was inspected, not
just the prepared source kit. Its SHA-256 is
`2f6b7eed8c09cecf7633f01909c6a4085bef691c29ed0d106c75e7b48c7b4721`.
The nested `platform.tar` member
`kernel/mediatek/mt8163/3.18_hl/firmware/i2s_to_spi_v34.bin` matches the bitstream
above byte-for-byte. Inspected outer, parent, kernel-root and adjacent notices
did not supply an applicable bitstream grant; the outer README supplies warranty
disclaimers. Kernel GPL notices do not explicitly identify this separately loaded
FPGA program. No HDL/preferred source was found in the inspected payloads.

An authorized grant or authoritative confirmation of existing-license coverage
would resolve the remaining uncertainty, subject to its compliance conditions.
A suitable binary-only grant could suffice without HDL. Re-review any changed
firmware, provenance, use or authoritative rights evidence. The exception does
not waive applicable source obligations, approve a particular publication, or
qualify the hardware for stable use. Other proprietary blobs remain outside it;
do not infer permission to distribute the owner's radio firmware.

## Rebuild the archived prepared source

Extract the kit into a new working directory. Obtain the AOSP arm-eabi 4.8
cross-toolchain at the exact repository/commit in
`devices/biscuit/packaging/kernel-inputs.json`. It is a
separate build tool under its own licenses, not part of the shipped kernel.
Use the environment recorded in the retained provenance and refer to the
workflow at the original build commit. `make biscuit-kernel` in the current
tree targets AArch64 and cannot reproduce the legacy kernel.

To rebuild without downloading Amazon's archive again:

1. Export `ARCH=arm`, `CROSS_COMPILE=/absolute/path/to/toolchain/bin/arm-eabi-`,
   `KBUILD_BUILD_VERSION=1`, `KCONFIG_NOTIMESTAMP=1`, and `SOURCE_DATE_EPOCH` from
   the lock. Set `KBUILD_BUILD_TIMESTAMP` to that epoch formatted in UTC with
   `date -u -d "@$SOURCE_DATE_EPOCH"`.
2. Create `out/include/generated`. Copy `.config` and `verity-keys` into `out/`,
   and `include/generated/trapz_generated_kernel.h` into `out/include/generated/`.
3. Run `make -C src O="$PWD/out" HOSTCFLAGS=-fcommon USE_TRAPZ=true
   KBUILD_BUILD_USER=techo5 KBUILD_BUILD_HOST=techo5-dot LOCALVERSION=-bt olddefconfig`,
   then the same command with `-j2 zImage-dtb` instead of `olddefconfig`.

The upstream build banner is retained; Encore owns the artifact provenance and
packaging, not authorship of the vendor kernel. The pinned recipe checks the
original Amazon/upstream source hashes and applies its complete patch series.
Those backports do not make Linux 3.18 a currently supported or fully audited
kernel. Track security updates and qualify replacement artifacts explicitly.

Kernel and system-source/license compliance remains a release responsibility.
CI artifacts are unsigned development outputs with bounded retention, not a
durable public source distribution or a supported release. Before binary
publication, retain the corresponding source kit and build inputs durably with
the exact release; preserve all applicable licenses/notices.
