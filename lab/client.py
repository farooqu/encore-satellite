"""Small authenticated client for this isolated HA test environment."""

import json
import os
from pathlib import Path

import aiohttp

STATE = Path(os.environ.get("ENCORE_LAB_STATE", Path.home() / ".local/state/encore-lab"))
HA = "http://127.0.0.1:8123"
CLIENT = HA + "/"


class HAClient:
    def __init__(self, session, token):
        self.session, self.token, self.next_id = session, token, 0
        self.ws = None

    async def api(self, method, path, data=None):
        async with self.session.request(method, HA + path, json=data, headers={"Authorization": "Bearer " + self.token}) as response:
            result = await response.json()
            if response.status >= 400:
                raise RuntimeError(f"HA {method} {path}: HTTP {response.status}")
            return result

    async def connect(self):
        self.ws = await self.session.ws_connect(HA + "/api/websocket")
        await self.ws.receive_json()
        await self.ws.send_json({"type": "auth", "access_token": self.token})
        assert (await self.ws.receive_json())["type"] == "auth_ok"

    async def command(self, kind, **kwargs):
        self.next_id += 1
        message_id = self.next_id
        await self.ws.send_json({"id": message_id, "type": kind, **kwargs})
        while True:
            result = await self.ws.receive_json()
            if result.get("id") == message_id and result["type"] == "result":
                if not result["success"]:
                    raise RuntimeError(f"HA {kind}: {result.get('error')}")
                return result.get("result")


async def login(session):
    credentials = json.loads((STATE / "credentials.json").read_text())
    async with session.post(HA + "/auth/token", data={"grant_type": "refresh_token", "refresh_token": credentials["refresh_token"], "client_id": CLIENT}) as response:
        result = await response.json()
        assert response.status == 200, "Lab refresh token no longer valid"
    client = HAClient(session, result["access_token"])
    await client.connect()
    return client
