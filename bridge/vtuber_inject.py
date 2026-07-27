"""
POST /inject 路由：接收外部程序（如 distillery）产出的回复文本，
用 LLM-Vup 默认 TTS 引擎合成语音，并将 音频+字幕+表情 广播给所有已连接的前端。
不经过 agent 会话链，也没有播放完成确认（参照 upload 注入路径）。

本模块由 bridge/serve.py 在启动时挂载，不修改 LLM-Vup 仓库内的任何文件。
"""

import asyncio
import json

from fastapi import APIRouter
from loguru import logger
from pydantic import BaseModel
from starlette.responses import JSONResponse

from src.open_llm_vtuber.agent.output_types import Actions, DisplayText
from src.open_llm_vtuber.conversations.tts_manager import TTSTaskManager

# 请求里的 emotion 直接就是模型 emo_map 的英文标签（如 "joy"），
# 由 live2d_model.extract_emotion 按 emo_map 做唯一一处过滤。

class InjectRequest(BaseModel):
    """外部程序注入的回复请求"""

    text: str
    emotion: str = ""
    # 情绪强度（0.0~1.0），保留字段：当前前端协议无强度通道，暂不使用
    intensity: float = 1.0


def init_inject_route(ws_handler, default_context_cache) -> APIRouter:
    """
    创建 `/inject` 路由。

    Args:
        ws_handler: LLM-Vup 共享的 WebSocketHandler 实例（持有所有前端连接）。
        default_context_cache: 默认 ServiceContext（提供 TTS 引擎、Live2D 模型、角色配置）。
    """

    router = APIRouter()

    async def broadcast(msg: str) -> None:
        """向所有已连接的前端广播一条消息，单个连接失败仅记日志"""
        for uid, ws in list(ws_handler.client_connections.items()):
            try:
                await ws.send_text(msg)
            except Exception as e:
                logger.warning(f"inject 广播到客户端 {uid} 失败: {e}")

    @router.post("/inject")
    async def inject(req: InjectRequest):
        text = req.text.strip()
        if not text:
            return JSONResponse({"error": "empty text"}, status_code=400)

        if not ws_handler.client_connections:
            logger.warning("inject: 没有活跃的前端连接，已丢弃")
            return JSONResponse({"error": "no frontend connected"}, status_code=409)

        context = default_context_cache
        character_config = context.character_config

        # 情绪 → 表情：emotion 即 emo_map 英文标签，由 extract_emotion 统一过滤
        tag = req.emotion.strip().lower()
        expressions = context.live2d_model.extract_emotion(f"[{tag}]") if tag else []
        actions = Actions(expressions=expressions) if expressions else None

        display_text = DisplayText(
            text=text,
            name=character_config.character_name,
            avatar=character_config.avatar,
        )

        # 消息序列参照 process_single_conversation（upload 路径，不等前端播放确认）
        tts_manager = TTSTaskManager()
        try:
            await broadcast(
                json.dumps({"type": "control", "text": "conversation-chain-start"})
            )

            await tts_manager.speak(
                tts_text=text,
                display_text=display_text,
                actions=actions,
                live2d_model=context.live2d_model,
                tts_engine=context.tts_engine,
                websocket_send=broadcast,
            )

            # 等待 TTS 合成完成（TTS 失败时 speak 内部会降级为静音字幕帧）
            if tts_manager.task_list:
                await asyncio.gather(*tts_manager.task_list)
                await broadcast(json.dumps({"type": "backend-synth-complete"}))

            await broadcast(json.dumps({"type": "force-new-message"}))
            await broadcast(
                json.dumps({"type": "control", "text": "conversation-chain-end"})
            )
        except Exception as e:
            logger.error(f"inject 处理失败: {e}")
            return JSONResponse({"error": str(e)}, status_code=500)
        finally:
            tts_manager.clear()

        client_count = len(ws_handler.client_connections)
        logger.info(
            f"inject: 已向 {client_count} 个前端广播 (emotion={req.emotion or '无'}): {text[:40]}"
        )
        return {"status": "ok", "clients": client_count}

    return router
