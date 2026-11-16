"""Generate English resources using pinned HA's own development translation tool.

Source archives omit compiled translations; the protocol implementation is never
patched. Keep downloaded sources in ignored orb scratch, not in Encore packages.
"""

import hashlib
import importlib.util
from pathlib import Path
import shutil
import subprocess
import tarfile
import tomllib
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
lock = tomllib.loads((ROOT / "lab/uv.lock").read_text())
ha = next(package for package in lock["package"] if package["name"] == "homeassistant")
url = ha["source"]["url"]
commit = url.rsplit("/", 1)[1].removesuffix(".tar.gz")
installed = Path(importlib.util.find_spec("homeassistant").origin).parent
marker = installed / ".encore-english-resources"
if marker.exists() and marker.read_text() == commit:
    print("HA English development resources already generated")
    raise SystemExit(0)

scratch = ROOT / ".amp/in/orb-backup"
scratch.mkdir(parents=True, exist_ok=True)
archive = scratch / f"ha-{commit}.tar.gz"
if not archive.exists():
    urllib.request.urlretrieve(url, archive)
expected = ha["sdist"]["hash"].removeprefix("sha256:")
assert hashlib.file_digest(archive.open("rb"), "sha256").hexdigest() == expected, "HA archive checksum mismatch"
source = scratch / f"ha-{commit}"
source.mkdir(exist_ok=True)
with tarfile.open(archive) as bundle:
    bundle.extractall(source, filter="data")
checkout = next(source.glob("core-*"))
subprocess.run([str(ROOT / "lab/.venv/bin/python"), "-m", "script.translations", "develop", "--all"], cwd=checkout, check=True, stdout=subprocess.DEVNULL)
count = 0
for english in (checkout / "homeassistant/components").glob("*/translations/en.json"):
    destination = installed / english.relative_to(checkout / "homeassistant")
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(english, destination)
    count += 1
marker.write_text(commit)
print(f"Generated/copied {count} English integration resources using HA's upstream script")
