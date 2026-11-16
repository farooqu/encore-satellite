"""Create only fresh isolated lab state and a deterministic PCM fixture."""

import array
import json
import os
from pathlib import Path
import sys
import wave

os.umask(0o077)
state = Path.home() / ".local/state/encore-sendspin-lab"
data = state / "ma"
data.mkdir(parents=True, exist_ok=True)
(state / "cache").mkdir(exist_ok=True)
settings = data / "settings.json"
if not settings.exists():
    settings.write_text(json.dumps({
        "core": {
            "webserver": {"domain": "webserver", "values": {
                "bind_ip": "127.0.0.1", "bind_port": 8097,
                "base_url": "http://127.0.0.1:8097", "server_name": "Encore isolated Sendspin lab",
            }},
            "streams": {"domain": "streams", "values": {
                "bind_ip": "127.0.0.1", "bind_port": 8098, "publish_ip": "127.0.0.1",
            }},
            "discovery": {"domain": "discovery", "values": {"upnp_network_scan": False}},
        },
        "providers": {"sendspin": {
            "type": "player", "domain": "sendspin", "instance_id": "sendspin",
            "enabled": True, "values": {"allow_legacy_clients": False},
        }},
    }, indent=2))

fixture = state / "fixture.wav"
if not fixture.exists():
    # 120 seconds of identical stereo 1 kHz triangle wave, peak 4096 (-18 dBFS).
    # Integer arithmetic makes the PCM bytes deterministic across platforms.
    period = [((i if i < 24 else 48 - i) * 8192 // 24) - 4096 for i in range(48)]
    samples = array.array("h", [value for value in period for _ in range(2)] * 1000)
    if sys.byteorder != "little":
        samples.byteswap()
    with wave.open(str(fixture), "wb") as output:
        output.setparams((2, 2, 48000, 0, "NONE", "not compressed"))
        for _ in range(120):
            output.writeframesraw(samples.tobytes())
print(f"Private MA state: {data}; fixture: {fixture}")
