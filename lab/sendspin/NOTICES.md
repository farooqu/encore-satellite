# Sendspin lab dependency provenance

Encore's distribution license remains unselected. This folder contains local
integration tooling, not a redistributable MA/player image. Setup downloads
upstream source, tools and binaries separately; no compiled dependency, model,
credential or trust record belongs in git.

- [Sendspin C++](https://github.com/Sendspin/sendspin-cpp/tree/a4a9b65fb356657e3a39a2ab70b4aae957bf53e0)
  is Apache-2.0. `receiver/main.cpp` uses its API and builds its upstream
  `examples/common/file_persistence_provider.cpp` in place. Preserve the original
  copyright/license. Its host build fetches ArduinoJson 7.4.1, IXWebSocket 12.0.1,
  micro-flac 0.3.0 and ESPHome noise-c 0.1.13; each dependency's own license and
  notices apply. Opus, source, artwork, color and visualizer roles are disabled.
- [Music Assistant server](https://github.com/music-assistant/server/tree/07780231361dd45668f937fc3fe950b38527fde0)
  is Apache-2.0. The minimal installed Python environment, frontend and hashes
  are recorded in `server/uv.lock`; this is not an assertion that all dependencies
  have that license. Their installed metadata/notices remain in the private venv.
- Setup downloads BtbN's **GPL FFmpeg** build n8.1.3
  (`ffmpeg-n8.1.3-35-gf1acb0a266-linux64-gpl-8.1`), not a permissive-only build.
  The pinned release URL and verified archive SHA-256 are in `server/setup`.
  Its archive includes its own license/build material; redistribution requires
  separate compliance review, including applicable source obligations.
- Real voice validation also uses the existing HA/Incubator dependencies and
  speech assets. Their terms, including Piper's GPL license, are documented in
  [Incubator notices](../incubator/NOTICES.md). No blanket permissive-license or
  hardware-distribution claim follows from this lab.
- The MA test-music WAV is generated locally from integer arithmetic; the voice
  fixture speech is generated locally using the existing pinned speech assets.

Upstream repositories and downloaded archives retain their own license files.
This list is provenance for the lab, not a complete packaged-binary license audit.
