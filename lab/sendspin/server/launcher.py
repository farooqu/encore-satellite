"""Run unmodified MA core/providers with one loopback-only local fixture route."""

import asyncio
from concurrent.futures import ThreadPoolExecutor
import os
from pathlib import Path
import signal

from aiohttp import web
from music_assistant.__main__ import setup_logger
from music_assistant.helpers.util import cap_native_thread_pools, set_model_cache_dirs
from music_assistant.mass import MusicAssistant


async def main():
    state = Path.home() / ".local/state/encore-sendspin-lab"
    os.umask(0o077)
    setup_logger(str(state / "ma"), "DEBUG")
    cap_native_thread_pools()
    set_model_cache_dirs(str(state / "cache"))
    loop = asyncio.get_running_loop()
    loop.set_default_executor(ThreadPoolExecutor(max_workers=32))
    stopped = asyncio.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, stopped.set)
    # Safe mode excludes optional automatically installed LAN/ML providers, not
    # MA's builtin Sendspin, queue, streaming, metadata, or loudness controllers.
    mass = MusicAssistant(str(state / "ma"), str(state / "cache"), safe_mode=True)

    async def fixture(request):
        return web.FileResponse(state / "fixture.wav")

    try:
        await mass.start()
        mass.webserver.register_dynamic_route("/lab/fixture.wav", fixture)
        await stopped.wait()
    finally:
        await mass.stop()


if __name__ == "__main__":
    asyncio.run(main())
