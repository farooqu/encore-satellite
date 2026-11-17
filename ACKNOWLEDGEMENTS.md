# Acknowledgements and included material

Encore's design and proof of concept were informed by **Techo5** and **Techo5
Panel**. Their practical work on Assist, native Incubator, Echo audio, wake-word
frontends and a native Android host made this investigation possible:

- [Techo5](https://github.com/farooqu/techo5), inspected at
  `2b091439765d5f881de4b89255b8cf0637d80cba`.
- [Techo5 Panel](https://github.com/farooqu/techo5-panel), inspected at
  `14c16fea2fbc64ce9b706009a3fed16766523e6d`.
- [Techo5 Dot](https://github.com/farooqu/techo5-dot), inspected at
  `b2a50c74bbe8b34785748574aa39fbd045fc4e46`.

Techo5's NOTICE traces historical daemon ancestry to Yuri Gelfand's MIT
**EchoLocal**, and its microWakeWord feature frontend to Julian Imhof's MIT
**echo-dot-assist**, itself drawing on **TensorFlow Lite Micro** and **KISS FFT**.
It also credits Serge Zaitsev's **microwakeword**, its model sources and other
dependencies. These foundations deserve acknowledgement even where Encore does
not include their implementations. This is not a claim that every historical or
current EchoLocal component has the same license as Techo5.

**Home Assistant Assist**, **ESPHome**, **Home Assistant Voice Preview Edition**,
**Voice Incubator** and **Linux Voice Assistant** are upstream protocol/product
and research foundations. Encore is independent and does not imply endorsement.
Timers, alarms, recognition and assistant behavior belong upstream, not to a new
Encore assistant. No Kiosk Satellite application code or assets are included.

## Code actually included in this increment

- `core/wake/mwwfront` and `core/wake/mixednet` reuse Techo5's pure-Go
  microWakeWord frontend and Voice Incubator mixednet executor at the Techo5 pin
  above, with Encore imports. The frontend's Julian Imhof MIT, TensorFlow Authors
  Apache-2.0 and Mark Borgerding BSD-3-Clause notices are in
  `licenses/Techo5-NOTICE.txt`; the executor's provenance and Apache-2.0 license
  are in its package and `licenses/Mixednet-NOTICE.txt` / `licenses/Apache-2.0.txt`.
  Model/reference fixtures are test-only; no production wake model is bundled.
- The Android build downloads pinned source and compiles only the `player` and `shared` library modules of
  [SendspinDroid](https://github.com/chrisuthe/SendspinDroid) at
  `bd1d90a8f79b8b78c1ee924a0e6c14016ed1f1e2`, verified by archive SHA-256.
  `licenses/SendspinDroid-MIT.txt` preserves its MIT license. No upstream Android
  app UI is included. Its pinned Gradle dependencies retain their respective
  upstream licenses; a release still needs a complete transitive inventory.
- `backends/incubator/protocol.go` adapts the small protocol framing implementation
  from Techo5's `echod/internal/feature/incubator/protocol.go` at the commit above.
  Its MIT copyright/permission text is retained in `licenses/Techo5-MIT.txt`.
  The complete inspected Techo5 NOTICE is retained as historical provenance at
  `licenses/Techo5-NOTICE.txt`, **not as an inventory of Encore's shipped code**.
- Bluetooth work also inspects [Techo5 Dot](https://github.com/farooqu/techo5-dot)
  at `b2a50c74bbe8b34785748574aa39fbd045fc4e46`. `cmd/btbridge` and its tests,
  `platforms/linuxble/bluez` GATT client/peripheral code, and `core/improv/protocol.go`
  reuse MIT Techo5 code at the Techo5 pin above; HCI framing and proxy UUID/flag
  conversions adapt small parts of that source. `licenses/Techo5-MIT.txt` applies
  to these portions too. Kernel/firmware research is not kernel/firmware inclusion.
- `github.com/godbus/dbus/v5` supplies D-Bus transport under its BSD-2-Clause
  license in the pinned module source. BlueZ and Linux are separate system
  components with their own licenses; no BlueZ C code or kernel binaries are
  included in the Go runtime. Any device image must carry its own complete
  kernel/module/firmware/system-package source and license compliance inventory.
- `github.com/ygelfand/go-esphome-device` is a pinned MIT Go dependency for the
  native API, Noise transport and mDNS. It and transitive modules remain under
  their own licenses supplied with their source; `go.mod`/`go.sum` identify the
  exact versions. No complete Techo5 feature registry is carried into Encore.
- `core/wake/mixednet` ports Techo5's pure-Go MNB1 executor/detector and
  microWakeWord frontend at the Techo5 pin above. Apache-2.0 applies to the
  Voice Incubator/mixednet-executor adaptations; frontend ancestry includes
  MIT echo-dot-assist, Apache-2.0 TensorFlow Lite Micro and BSD-3-Clause KISS FFT.
  The package retains the full applicable notices/license and external test
  fixture provenance. `backends/incubator/models.go` follows Techo5's MIT
  model-transfer validation/lifecycle contract. Device packaging includes the
  runtime notices. No trained wake model is shipped in the runtime; transferred
  models retain their own terms independently of the executor.
- `platforms/linux/{alsa,input}` adapts the pinned Techo5 daemon's Linux ALSA/evdev
  primitives, originally from Yuri Gelfand's MIT EchoLocal. Its license and source
  inventory are retained in `platforms/linux/LICENSE.techo5` and `NOTICE`. Dot-local
  routing/control mappings cite that same source rather than claiming new measurements.
- `devices/biscuit/cmd/wmtup` copies Techo5 Dot's two-file MediaTek bring-up
  helper at the pin above. That work credits Wil Bowes's MIT EchoMuse/emOS findings.
  `devices/biscuit/packaging/Techo5-Dot-{MIT,NOTICE}.txt` retain its license and
  complete historical provenance; the latter lists upstream components **not** all
  included here. Encore's small image/slot tooling is informed by that boot-chain work.
- `devices/biscuit/packaging/kernel.sh` invokes the exact Techo5 Dot kernel
  recipe at the same pin with its Bluetooth/security patch series and AOSP
  arm-eabi 4.8 toolchain at `26e93f6af47f7bd3a9beb5c102a5f45e19bfa38a`.
  The separately built kernel combines Amazon's checksum-verified biscuit GPL
  source release with missing Bluetooth files from Linux 3.18.19. Linux source,
  patches and toolchain retain their own licenses; the MIT license of the upstream
  build recipe does not relicense them. The project kernel artifact retains the
  prepared corresponding source, configuration and build inputs in `source.tar.gz`;
  the Linux-environment bundle includes that kernel and its provenance, and the
  rootfs retains its COPYING. Preserve the full source kit durably with any release.
  The effective kernel config also embeds the audio FPGA bitstream
  `firmware/i2s_to_spi_v34.bin`; it is present in the retained source kit. Its
  firmware-specific license, preferred source and redistribution grant have not
  been established. The kernel's COPYING does not by itself settle those terms;
  resolve this before publishing the kernel, image or source kit.
- The local Dot base builder fetches signature-verified Alpine ARMv7 packages,
  including GPL BusyBox, musl and wpa_supplicant with their own licenses. The
  installer supplies Wi-Fi/Bluetooth radio firmware from the owner's unit or
  private files, separately from the kernel's embedded audio FPGA firmware.
  A complete corresponding-source/license inventory is required before distribution;
  the development builder is not a release-compliance process.
- The optional Dot Sendspin companion builds the official Apache-2.0
  `sendspin-cpp` SDK at `a4a9b65fb356657e3a39a2ab70b4aae957bf53e0` with
  ArduinoJson (MIT), IXWebSocket (BSD-3-Clause), micro-flac/micro-ogg-demuxer
  (Apache-2.0) and noise-c (MIT plus bundled crypto notices). Exact commits and
  compiler download checksums are in `platforms/linuxsendspin/dependencies.lock.json`.
  Its static ARMv7 musl/libc++ build uses Zig 0.14.1, not the kernel toolchain;
  Zig/musl and LLVM runtime sources retain their respective MIT/Apache-2.0 with
  LLVM-exception and per-file notices. Each build retains notices, provenance
  and a source kit including the integration and bundled runtime sources. Keep
  them with the exact binary. This is independent of the lab-only MA server and
  does not package Python, FFmpeg, the HTTP PCM sink or guest pairing policy.
- Home Assistant and its Python dependencies are downloaded separately for the
  development lab under their respective upstream licenses. The lab fixtures are
  Encore test instrumentation, not replacement implementations of HA protocols.
- The optional real-service labs download **Voice Incubator**, its HA integration
  and **OHF Voice Support / app-voice** separately at documented source pins.
  Their Apache-2.0 app licenses do not relicense their dependencies or models.
  In particular the tested `piper-tts 1.8.0` is GPL-3.0-or-later. Models, voices,
  soundfonts and system libraries have separate terms; see
  [Incubator lab notices](lab/incubator/NOTICES.md) and the
  [Assist wake lab](lab/assist-wake/README.md). These are local validation
  environments, not dependencies bundled into Encore's Go runtime or approved
  release packages.

No Panel Java code, trained wake model, TFLite interpreter, firmware blob,
sound or visual asset is bundled in the Encore Go runtime. This is not a claim
that the separate Dot kernel/image is firmware-free. The MixedNet executor
and DSP frontend inclusion is described above; regression-only model/audio
fixtures have their own provenance and are not packaged. The test tone is
generated mathematically. Research acknowledgement is distinct from inclusion.

Encore-owned code and documentation are licensed under the [MIT License](LICENSE).
This does not relicense the third-party material identified above, including
Apache-2.0 executor code and GPL/LGPL operating-system components. Preserve their
applicable licenses and notices. A complete dependency/model/asset and matching
source audit is still required before binary distribution.
