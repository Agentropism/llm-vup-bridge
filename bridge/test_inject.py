"""Injection orchestration tests with upstream engines/transport replaced by fakes.

Run with: python -m unittest discover -s bridge -p 'test_*.py'
No model downloads or changes to the LLM-Vup checkout are needed.
"""

import asyncio
import base64
import importlib.util
import io
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch
import wave


class Router:
    def __init__(self):
        self.handlers = {}

    def get(self, path):
        return self.post(path)

    def post(self, path):
        def register(handler):
            self.handlers[path] = handler
            return handler
        return register


class Response:
    def __init__(self, body, status_code):
        self.body, self.status_code = body, status_code


def load_inject(prepare):
    modules = {}
    for name, attrs in {
        "fastapi": {"APIRouter": Router},
        "loguru": {"logger": types.SimpleNamespace(warning=lambda *a: None, exception=lambda *a: None)},
        "pydantic": {"BaseModel": object, "Field": lambda **kw: kw.get("default")},
        "starlette.responses": {"JSONResponse": Response},
        "src.open_llm_vtuber.agent.output_types": {"Actions": types.SimpleNamespace, "DisplayText": types.SimpleNamespace},
        "src.open_llm_vtuber.utils.stream_audio": {"prepare_audio_payload": prepare},
    }.items():
        modules[name] = types.ModuleType(name)
        modules[name].__dict__.update(attrs)
    spec = importlib.util.spec_from_file_location("inject_under_test", Path(__file__).with_name("vtuber_inject.py"))
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, modules):
        spec.loader.exec_module(module)
    return module


class InjectionTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.paths, self.messages = [], []
        wav_bytes = io.BytesIO()
        with wave.open(wav_bytes, "wb") as out:
            out.setnchannels(1)
            out.setsampwidth(2)
            out.setframerate(8000)
            out.writeframes(b"\0\0" * 80)
        self.audio = base64.b64encode(wav_bytes.getvalue()).decode()

        def prepare(path, **kwargs):
            self.paths.append(path)
            return {"type": "audio", "audio": self.audio}
        self.module = load_inject(prepare)

        async def send(text):
            import json
            self.messages.append(json.loads(text))
        async def unexpected_tts(**kwargs):
            self.fail("external audio must not be synthesized again")
        self.ws = types.SimpleNamespace(client_connections={"browser": types.SimpleNamespace(send_text=send)})
        self.context = types.SimpleNamespace(
            character_config=types.SimpleNamespace(character_name="Mili", avatar=""),
            live2d_model=types.SimpleNamespace(extract_emotion=lambda text: [1]),
            tts_engine=types.SimpleNamespace(async_generate_audio=unexpected_tts),
        )
        router = self.module.init_inject_route(self.ws, self.context)
        self.inject, self.health = router.handlers["/inject"], router.handlers["/inject/health"]

    def request(self, **kwargs):
        values = dict(text="谢谢！", emotion="joy", intensity=0.3, audio_base64=self.audio, audio_format="wav")
        values.update(kwargs)
        return types.SimpleNamespace(**values)

    async def test_external_audio_order_and_cleanup(self):
        result = await self.inject(self.request())
        self.assertEqual(result["clients"], 1)
        self.assertGreater(result["duration_seconds"], 0)
        self.assertEqual([m.get("text", m["type"]) for m in self.messages], [
            "conversation-chain-start", "audio", "backend-synth-complete", "force-new-message", "conversation-chain-end",
        ])
        self.assertTrue(self.paths)
        self.assertFalse(Path(self.paths[0]).exists())
        self.assertEqual((await self.health())["clients"], 1)

    async def test_requests_are_serialized(self):
        await asyncio.gather(self.inject(self.request()), self.inject(self.request()))
        events = [m.get("text") for m in self.messages if m["type"] == "control"]
        self.assertEqual(events, ["conversation-chain-start", "conversation-chain-end"] * 2)

    async def test_invalid_audio_and_disconnected_frontend(self):
        result = await self.inject(self.request(audio_base64="%%%"))
        self.assertEqual(result.status_code, 400)
        self.ws.client_connections.clear()
        result = await self.inject(self.request())
        self.assertEqual(result.status_code, 409)
        self.assertFalse(self.paths)

    async def test_failed_broadcast_is_not_success(self):
        async def fail(text):
            raise ConnectionError("closed")
        self.ws.client_connections["browser"].send_text = fail
        result = await self.inject(self.request())
        self.assertEqual(result.status_code, 500)
        self.assertFalse(Path(self.paths[0]).exists())


if __name__ == "__main__":
    unittest.main()
