# Incubator lab: provenance, not an Encore distribution license

Design acknowledgements remain in [ACKNOWLEDGEMENTS.md](../../ACKNOWLEDGEMENTS.md).
The license for new Encore code remains **unselected**. This lab does not change
that or confer a license on upstream models. No model, sound asset, vendored
upstream runtime source, compiled extension or secret is included in these added
tracked lab files. Setup downloads/builds them separately for local validation.
Do not infer that this entire environment is permissively licensed or suitable
for redistribution from the two app repositories' Apache licenses.

## Source/runtime dependencies

- Voice Incubator `38b573b13a92f0654b24611a3a6355c8e8c4d5c3` and its HA
  integration `e02de9447868b7cdc90dc6437618662b3ebef6f6` are Apache-2.0.
  Their checkout `LICENSE` files and upstream vendored notices remain intact.
  The setup uses their source; it does not copy it into Encore's runtime.
- The exact Python environment is in `requirements.lock`; each installed
  distribution retains its own license metadata/notices. In particular,
  **piper-tts 1.8.0 is GPL-3.0-or-later**, not Apache/MIT. The upstream app
  source license is not a blanket license for its dependencies.
- Kaldi model/tools, downloaded microWakeWord assets and third-party components
  retain the license files/cards supplied upstream. Debian's FluidSynth and
  General MIDI SoundFont packages also have their own copyright files. This is
  not a binary packaging/license-compliance audit.

## Model provenance actually used by this pin

`voice_incubator/downloads.py` at the app revision pins file URLs, sizes and
SHA-256 hashes. Downloads are checked before installation (Piper's own pinned
catalog uses MD5; independent SHA-256 values for the tested voice follow).

- Main asset repository: `rhasspy/voice-incubator-models`
  `e359089e2dfe430415b24410a089fdcf36fd5bf9`.
  This includes `parakeet-tdt_ctc-110m-en-int8` (English shared encoder,
  transducer and CTC head/tokens), English Kaldi wake verifier, GLiNER fallback,
  and microWakeWord assets. Parakeet originates with NVIDIA (CC-BY-4.0 model
  terms); model-specific terms apply, not just the container repository's label.
  The Kaldi model is MIT and tools Apache-2.0; microWakeWord and GLiNER assets
  identify Apache-2.0 provenance. Preserve individual upstream cards/notices and
  attribution if redistributing; they are not newly relicensed here.
- Additional Kaldi G2P/tool archives: `datasets/rhasspy/rhasspy-speech`
  `9e6bcf3af07909f39f71674f62372508d00dd4a9`, as pinned by the downloader.
  These are downloaded by standard setup, not exercised as Kaldi command-mode
  recognition in this proof.
- Piper catalog/voice files: `rhasspy/piper-voices`
  `5041a7a49ae5b3a0f30e6837769eb03fdbb231bd`, `en_US-kristin-medium`.
  Its downloaded `MODEL_CARD` credits the LibriVox public-domain dataset. Do not
  confuse the dataset statement with the Piper engine's GPL terms or a blanket
  license for every voice in the catalog.
  - Model SHA-256:
    `5849957f929cbf720c258f8458692d6103fff2f0e3d3b19c8259474bb06a18d4`.
  - Config SHA-256:
    `5681426d4aead22195de70531eeeeddb46493cfaffc5764b2ea3db73428b651c`.
- Standard setup also fetches **unused** `onnx-community/embeddinggemma-2-ONNX`
  `daa72c51243991dfcaf9f9137d2c573d8f7790c0` and
  `OpenVoiceOS/redimnet-b2-vox2-onnx`
  `5127cc45350579cafea037f82634c2be04ca712d`. Neither embedding matching nor
  speaker identification is enabled by the launcher. The former's repository
  card labels Apache-2.0, but underlying Gemma terms/provenance still require
  review before distribution; no blanket permissive claim is made for either.

This inventory records the pinned local test environment, not authorization to
bundle it in an Encore release. A release still requires a complete source,
dependency, model and asset notice/license audit, especially for GPL dependencies
and model-specific attribution/terms. No binaries are committed by this work.
