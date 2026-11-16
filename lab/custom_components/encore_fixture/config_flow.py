"""Local-only opt-in for the deterministic speech providers."""

from homeassistant.config_entries import ConfigFlow


class FixtureFlow(ConfigFlow, domain="encore_fixture"):
    VERSION = 1

    async def async_step_user(self, user_input=None):
        await self.async_set_unique_id("encore-lab-speech")
        self._abort_if_unique_id_configured()
        return self.async_create_entry(title="Encore speech fixtures", data={})
