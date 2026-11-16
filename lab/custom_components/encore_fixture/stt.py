"""Consume real API audio; substitute recognition, not the Assist pipeline."""

import asyncio
import hashlib
import logging

from homeassistant.components.stt import (
    AudioBitRates, AudioChannels, AudioCodecs, AudioFormats, AudioSampleRates,
    SpeechAudioProcessing, SpeechResult, SpeechResultState, SpeechToTextEntity,
)
from . import scenario

_LOGGER = logging.getLogger(__name__)


async def async_setup_entry(hass, entry, async_add_entities):
    async_add_entities([FixtureSTT()])


class FixtureSTT(SpeechToTextEntity):
    _attr_name = "Encore fixture STT"
    _attr_unique_id = "encore-fixture-stt"
    supported_languages = ["en"]
    supported_formats = [AudioFormats.WAV]
    supported_codecs = [AudioCodecs.PCM]
    supported_bit_rates = [AudioBitRates.BITRATE_16]
    supported_sample_rates = [AudioSampleRates.SAMPLERATE_16000]
    supported_channels = [AudioChannels.CHANNEL_MONO]
    # Deterministic PCM is not speech. Exercise the real pipeline without VAD
    # classifying a generated test waveform as silence.
    audio_processing = SpeechAudioProcessing(False, False, False)

    async def async_process_audio_stream(self, metadata, stream):
        count, digest = 0, hashlib.sha256()
        mode = scenario(self.hass)
        try:
            async for block in stream:
                digest.update(block)
                count += len(block)
            _LOGGER.info("STT fixture received bytes=%s sha256=%s mode=%s", count, digest.hexdigest(), mode)
            self.hass.bus.async_fire("encore_fixture_audio", {"bytes": count, "sha256": digest.hexdigest(), "scenario": mode})
            if mode == "slow_stt":
                await asyncio.sleep(10)
            if mode == "stt_error" or count == 0:
                return SpeechResult("", SpeechResultState.ERROR)
            return SpeechResult("Turn on the Encore lamp", SpeechResultState.SUCCESS)
        except asyncio.CancelledError:
            _LOGGER.info("STT fixture cancelled")
            self.hass.bus.async_fire("encore_fixture_cancelled", {"scenario": mode})
            raise
