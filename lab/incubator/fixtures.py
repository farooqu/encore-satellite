"""Generate synthetic PCM fixtures; this is not microphone or speech-quality proof."""

import json
from pathlib import Path

import numpy as np
from piper import PiperVoice, SynthesisConfig
import soxr


def main():
    state = Path.home() / ".local/state/encore-incubator"
    out = state / "fixtures"
    out.mkdir(parents=True, exist_ok=True)
    voice = PiperVoice.load(str(state / "models/piper/en_US-kristin-medium.onnx"))
    for name, text in {
        "wake": "Okay Nabu.",
        "command": "Turn on the lamp.",
        "unknown": "Purple elephants juggle spaghetti.",
        "follow": "Yes.",
    }.items():
        chunks = list(voice.synthesize(text, syn_config=SynthesisConfig(
            length_scale=0.8, noise_scale=0, noise_w_scale=0,
        )))
        samples = np.concatenate([soxr.resample(c.audio_float_array, c.sample_rate, 16000) for c in chunks])
        # Trim Piper's padding, retaining 30 ms on each side.
        active = np.flatnonzero(np.abs(samples) > 0.01)
        samples = samples[max(0, active[0] - 480): active[-1] + 480]
        samples = (np.clip(samples, -1, 1) * 24000).astype("<i2")
        (out / f"{name}.pcm").write_bytes(samples.tobytes())
        print(json.dumps({"fixture": name, "text": text, "samples": len(samples)}))


if __name__ == "__main__":
    main()
