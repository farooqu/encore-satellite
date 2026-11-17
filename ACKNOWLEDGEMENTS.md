# Acknowledgements and included material

Encore's design and proof of concept were informed by **Techo5** and **Techo5
Panel**. Their practical work on Assist, native Incubator, Echo audio, wake-word
frontends and a native Android host made this investigation possible:

- [Techo5](https://github.com/farooqu/techo5), inspected at
  `2b091439765d5f881de4b89255b8cf0637d80cba`.
- [Techo5 Panel](https://github.com/farooqu/techo5-panel), inspected at
  `14c16fea2fbc64ce9b706009a3fed16766523e6d`.

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
  exact versions. No complete Techo5 feature registry or concrete device code is
  carried into Encore.
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

No Panel Java code, DSP frontend, wake model, TensorFlow implementation, FFT,
native Incubator model runtime, firmware blob, sound or visual asset has been
copied into the Encore runtime in this increment. The test tone is generated
mathematically. Research acknowledgement is distinct from code inclusion.

The license for **new Encore code remains unselected**, as stated in the README.
A permissive license is intended; Apache-2.0 is a recommendation, not an applied
license. Required third-party notices above already apply to their material. A
complete dependency/model/asset audit is required before binary distribution.
