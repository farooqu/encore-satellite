"""Lab-only speech fixtures. Never included in the Encore engine."""

from homeassistant.const import Platform


async def async_setup_entry(hass, entry):
    await hass.config_entries.async_forward_entry_setups(entry, [Platform.STT, Platform.TTS])
    return True


async def async_unload_entry(hass, entry):
    return await hass.config_entries.async_unload_platforms(entry, [Platform.STT, Platform.TTS])


def scenario(hass):
    state = hass.states.get("input_select.encore_scenario")
    return state.state if state else "success"
