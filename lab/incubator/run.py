"""Launch the unmodified upstream app against the isolated HA/Encore lab."""

import asyncio
import os
from pathlib import Path
import sys

import aiohttp

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "lab"))
from client import HA, STATE, login  # noqa: E402


async def token():
    path = STATE / "incubator-token"
    async with aiohttp.ClientSession() as http:
        if path.is_file():
            value = path.read_text().strip()
            async with http.get(HA + "/api/", headers={"Authorization": "Bearer " + value}) as response:
                if response.status == 200:
                    return value
        ha = await login(http)
        # Unlike the refresh exchange's 30-minute token, this survives a
        # supervised service reconnect. Only the isolated lab user's token.
        value = await ha.command("auth/long_lived_access_token", client_name="Encore Incubator lab", lifespan=30)
        path.touch(mode=0o600, exist_ok=True)
        path.chmod(0o600)
        path.write_text(value)
        await ha.ws.close()
        return value


if __name__ == "__main__":
    upstream = ROOT / ".amp/in/orb-backup/voice-incubator"
    env = dict(os.environ, HASS_TOKEN=asyncio.run(token()))
    os.chdir(upstream)
    os.execve(upstream / ".venv/bin/python", [
        str(upstream / ".venv/bin/python"), "-m", "voice_incubator",
        "--http-port", "8100", "--wyoming-port", "0",
        "--hass-url", "http://127.0.0.1:8123",
        "--data-dir", str(Path.home() / ".local/state/encore-incubator/app"),
        "--no-speaker-id", "--embedding-model", "none",
    ], env)
