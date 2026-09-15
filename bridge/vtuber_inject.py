"""Bridge routes for text or pre-generated audio, with serialized injection."""

import asyncio
import base64
import binascii
import io
import json
import tempfile
import wave
from pathlib import Path
from typing import Literal

from fastapi import APIRouter
from loguru import logger
from pydantic import BaseModel, Field
from starlette.responses import JSONResponse

from src.open_llm_vtuber.agent.output_types import Actions, DisplayText
from src.open_llm_vtuber.utils.stream_audio import prepare_audio_payload


class InjectRequest(BaseModel):
    text: str = Field(min_length=1, max_length=8000)
    emotion: str = "neutral"
    intensity: float = Field(default=0.35, ge=0, le=1)
    audio_base64: str = Field(default="", max_length=32 * 1024 * 1024)
    audio_format: Literal["mp3", "wav"] = "mp3"


def init_inject_route(ws_handler, default_context_cache) -> APIRouter:
    router = APIRouter()
    playback_lock = asyncio.Lock()

    async def broadcast(msg: dict) -> int:
        sent = 0
        for uid, ws in list(ws_handler.client_connections.items()):
            try:
                await ws.send_text(json.dumps(msg))
                sent += 1
            except Exception as exc:
                logger.warning(f"inject 广播到 {uid} 失败: {exc}")
        return sent

    @router.get("/inject/health")
    async def health():
        context = default_context_cache
        return {
            "bridge": "llm-vup-bridge",
            "clients": len(ws_handler.client_connections),
            "character": getattr(context.character_config, "character_name", ""),
            "tts": type(context.tts_engine).__name__,
            "audio_injection": True,
            "busy": playback_lock.locked(),
            "playback_sync": "audio_duration",
        }

    @router.post("/inject")
    async def inject(req: InjectRequest):
        text = req.text.strip()
        if not text:
            return JSONResponse({"error": "empty text"}, status_code=400)
        audio_bytes = None
        if req.audio_base64:
            try:
                audio_bytes = base64.b64decode(req.audio_base64, validate=True)
                if not audio_bytes:
                    raise ValueError("empty audio")
            except (ValueError, binascii.Error):
                return JSONResponse({"error": "invalid base64 audio"}, status_code=400)

        async with playback_lock:
            if not ws_handler.client_connections:
                return JSONResponse({"error": "没有前端连接，请先打开主项目的 Live2D 页面"}, status_code=409)
            context = default_context_cache
            character = context.character_config
            expressions = context.live2d_model.extract_emotion(f"[{req.emotion.strip().lower()}]")
            actions = Actions(expressions=expressions) if expressions else None
            display = DisplayText(text=text, name=character.character_name, avatar=character.avatar)
            path = None
            own_file = audio_bytes is not None
            started = False
            try:
                if own_file:
                    with tempfile.NamedTemporaryFile(suffix=f".{req.audio_format}", delete=False) as f:
                        f.write(audio_bytes)
                        path = f.name
                else:
                    path = await context.tts_engine.async_generate_audio(text=text)
                    if not path:
                        raise ValueError("TTS 没有返回音频文件")
                payload = await asyncio.to_thread(
                    prepare_audio_payload, path, display_text=display, actions=actions
                )
                # The existing frontend has no injection-specific playback ACK.
                # Pace injected clips by decoded WAV duration, not synthesis time.
                with wave.open(io.BytesIO(base64.b64decode(payload["audio"])), "rb") as wav:
                    duration = wav.getnframes() / wav.getframerate()
                await broadcast({"type": "control", "text": "conversation-chain-start"})
                started = True
                clients = await broadcast(payload)
                if clients == 0:
                    raise ValueError("音频发送失败，前端连接已断开")
                await broadcast({"type": "backend-synth-complete"})
                await asyncio.sleep(duration)
                await broadcast({"type": "force-new-message"})
                return {"status": "ok", "clients": clients, "duration_seconds": duration}
            except Exception as exc:
                logger.exception("inject 处理失败")
                return JSONResponse({"error": str(exc)}, status_code=500)
            finally:
                if started:
                    await broadcast({"type": "control", "text": "conversation-chain-end"})
                if path:
                    if own_file:
                        Path(path).unlink(missing_ok=True)
                    else:
                        context.tts_engine.remove_file(path)

    return router
