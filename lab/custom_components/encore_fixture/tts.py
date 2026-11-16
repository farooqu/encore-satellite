"""Deterministic PCM tone instead of speech synthesis; no audio asset needed."""

import io
import math
import struct
import wave

from homeassistant.components.tts import TextToSpeechEntity
from . import scenario


async def async_setup_entry(hass, entry, async_add_entities):
    async_add_entities([FixtureTTS()])


class FixtureTTS(TextToSpeechEntity):
    _attr_name = "Encore fixture TTS"
    _attr_unique_id = "encore-fixture-tts"
    _attr_default_language = "en"
    _attr_supported_languages = ["en"]

    async def async_get_tts_audio(self, message, language, options=None):
        if scenario(self.hass) == "tts_error":
            self.hass.bus.async_fire("encore_fixture_tts_error", {})
            return None, None
        buffer = io.BytesIO()
        with wave.open(buffer, "wb") as wav:
            wav.setnchannels(1)
            wav.setsampwidth(2)
            wav.setframerate(16000)
            count = 3200 if scenario(self.hass) == "short_tts" else 16000
            wav.writeframes(b"".join(struct.pack("<h", round(6000*math.sin(i*2*math.pi*440/16000))) for i in range(count)))
        return "wav", buffer.getvalue()
