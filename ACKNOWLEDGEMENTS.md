# Acknowledgements and included material

We learned a great deal from **Techo5**, **Kiosk Satellite**, **LibreEcho**, and
the **Voice Preview Edition Incubator** work. We couldn't have built Encore
without the practical examples those projects shared, even where we didn't use
their code. Comparing how they solved the same problems helped us choose an
approach, combine ideas, or build and test something different for Encore.

Encore is an independent implementation, not a distribution of Techo5. Its early
proof of concept drew on Techo5 and Techo5 Panel's work on Assist, native
Incubator, Echo audio, wake words and Android. References include:

- [Techo5](https://github.com/farooqu/techo5), inspected at
  `2b091439765d5f881de4b89255b8cf0637d80cba`.
- [Techo5 Panel](https://github.com/farooqu/techo5-panel), inspected at
  `14c16fea2fbc64ce9b706009a3fed16766523e6d`.
- [Techo5 Dot](https://github.com/farooqu/techo5-dot), inspected at
  `b2a50c74bbe8b34785748574aa39fbd045fc4e46`.
- [Kiosk Satellite](https://github.com/jxlarrea/lineageos-echo-kiosk-satellite),
  including its Biscuit HAL as a hardware reference.
- [LibreEcho](https://github.com/aslater3/LibreEcho-Platform), including its
  MediaTek board profiles and the kernel source identified below.
- [Voice Incubator](https://github.com/OHF-Voice/voice-incubator) and the
  [Voice Preview Edition](https://www.home-assistant.io/voice-pe/) as references
  for voice behavior and protocol integration.

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
- The current Biscuit AArch64 kernel uses [LibreEcho-Linux](https://github.com/aslater3/LibreEcho-Linux)
  at `5bc811d3cd7091e8d70f9faa0bc12c5cf4e77ae0` plus Encore patches. Its kernel
  source, configuration, patches and toolchain retain their applicable licenses;
  preserve the matching source kit with each distributed binary. The old Techo5
  Dot-derived Linux 3.18 build is retired; its provenance remains in Git history.
- The Biscuit image uses Alpine AArch64 userspace; system packages retain their
  own licenses and corresponding-source obligations. The installer obtains
  compatible stock firmware from the device rather than bundling those radio
  binaries. The documented good-faith exception for the exact
  `i2s_to_spi_v34.bin` blob is not legal clearance, publication approval, or an
  exception for a separately recovered FPGA tail. See
  [firmware provenance](devices/biscuit/packaging/FPGA-PROVENANCE.txt) and the
  [current package guide](devices/biscuit/packaging/kernel61/README.md).
- The experimental Dot Sendspin companion (not bundled in the ARM64 starter)
  builds the official Apache-2.0
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
