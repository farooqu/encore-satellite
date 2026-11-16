"""Use upstream's pinned manifests and checksum-verifying downloader, unpatched."""

import asyncio
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
APP = ROOT / ".amp/in/orb-backup/voice-incubator"
sys.path.insert(0, str(APP))
from voice_incubator import downloads, languages, piper_voices  # noqa: E402


async def main():
    models = APP / "models"
    data = Path.home() / ".local/state/encore-incubator/models"
    # `fetch` alone installs IMAGE, not speech models. `missing` intentionally
    # considers only symlinked model folders, as used by upstream's Dockerfile.
    if not (models / "parakeet-tdt-int8").is_symlink():
        downloads.link(data, models)
    downloads.fetch(models, "amd64")
    voice = piper_voices.everything()["en_US-kristin-medium"]
    todo = downloads.missing(models, "en", True)
    todo += voice.missing(models / "piper")
    await downloads.Downloader(todo, tries=3).run()
    assert not languages.load("en").missing(APP), "English models are not ready"
    print("English shared CTC/TDT readiness: PASS")


if __name__ == "__main__":
    asyncio.run(main())
