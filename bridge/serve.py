"""
LLM-Vup 桥接启动器 —— 不修改 LLM-Vup 原仓库的任何文件。

用法:
    # 方式一：把 Open-LLM-VTuber/LLM-Vup 克隆到本模块根目录下的 ./LLM-Vup
    cd LLM-Vup && uv run python ../bridge/serve.py [--verbose] [--hf_mirror]

    # 方式二：用环境变量指向任意位置的仓库检出
    LLM_VUP_ROOT=/path/to/LLM-Vup uv run --project /path/to/LLM-Vup \
        python bridge/serve.py [--verbose] [--hf_mirror]

作用:
    1. 复刻 LLM-Vup run_server.py 的启动流程（环境变量、配置同步、初始化、uvicorn），
       直接复用其模块级逻辑，行为与原启动器一致。
    2. 启动时通过包装 init_client_ws_route 拿到共享的 WebSocketHandler，
       把 POST /inject（见 vtuber_inject.py）挂进同一个 FastAPI app。
       distillery 等外部程序 POST {"text","emotion","intensity"} 即可让
       Live2D 形象开口说话（TTS + 口型 + 表情 + 字幕广播到所有前端）。

环境变量:
    LLM_VUP_ROOT  LLM-Vup/Open-LLM-VTuber 仓库根目录，
                  默认为本模块根目录下的 ./LLM-Vup
"""

import argparse
import asyncio
import atexit
import os
import sys
import tempfile
from pathlib import Path

# 定位 LLM-Vup 仓库根并切入（LLM-Vup 大量使用相对路径：conf.yaml、cache、models 等）
BRIDGE_ROOT = Path(__file__).resolve().parent.parent
_candidates = [BRIDGE_ROOT.parent / "LLM-Vup", BRIDGE_ROOT / "LLM-Vup"]
_default_root = next((p for p in _candidates if (p / "run_server.py").is_file()), _candidates[0])
LLM_VUP_ROOT = Path(os.environ.get("LLM_VUP_ROOT", _default_root)).resolve()
if not (LLM_VUP_ROOT / "run_server.py").is_file():
    raise SystemExit(f"未找到 LLM-Vup 仓库: {LLM_VUP_ROOT}（用 LLM_VUP_ROOT 环境变量指定）")
os.chdir(LLM_VUP_ROOT)
sys.path.insert(0, str(LLM_VUP_ROOT))

import uvicorn  # noqa: E402
from loguru import logger  # noqa: E402

# 复用原启动器：模块级会设置 HF_HOME/MODELSCOPE_CACHE、创建 upgrade_manager
import run_server  # noqa: E402
from src.open_llm_vtuber import server as server_module  # noqa: E402
from src.open_llm_vtuber.config_manager import read_yaml, validate_config  # noqa: E402

from vtuber_inject import init_inject_route  # noqa: E402

# ── 桥接：包装 init_client_ws_route，捕获共享 WebSocketHandler 实例 ──
_captured: dict = {}
_orig_init_client_ws_route = server_module.init_client_ws_route


def _patched_init_client_ws_route(default_context_cache, ws_handler):
    _captured["ws_handler"] = ws_handler
    return _orig_init_client_ws_route(default_context_cache, ws_handler)


server_module.init_client_ws_route = _patched_init_client_ws_route


def parse_args():
    parser = argparse.ArgumentParser(description="LLM-Vup bridge server (with /inject)")
    parser.add_argument("--verbose", action="store_true", help="Enable verbose logging")
    parser.add_argument("--hf_mirror", action="store_true", help="Use Hugging Face mirror")
    return parser.parse_args()


@logger.catch
def run(console_log_level: str):
    # 以下流程与 run_server.run 保持一致
    run_server.init_logger(console_log_level)
    logger.info(f"Open-LLM-VTuber, version v{run_server.get_version()} (bridge: /inject)")

    lang = run_server.upgrade_manager.lang
    run_server.check_frontend_submodule(lang)

    try:
        run_server.upgrade_manager.sync_user_config()
    except Exception as e:
        logger.error(f"Error syncing user config: {e}")

    atexit.register(server_module.WebSocketServer.clean_cache)

    config = validate_config(read_yaml("conf.yaml"))
    server_config = config.system_config

    if getattr(server_config, "enable_proxy", False):
        logger.info("Proxy mode enabled - /proxy-ws endpoint will be available")

    # Missing optional avatar assets use a temporary empty directory; the upstream
    # checkout is never patched or populated by this bridge.
    if not (LLM_VUP_ROOT / "avatars").is_dir():
        empty_avatars = tempfile.TemporaryDirectory(prefix="llm-vup-avatars-")
        atexit.register(empty_avatars.cleanup)
        original_avatar_files = server_module.AvatarStaticFiles

        def avatar_files(*args, **kwargs):
            kwargs["directory"] = empty_avatars.name
            return original_avatar_files(*args, **kwargs)

        server_module.AvatarStaticFiles = avatar_files

    # 构造过程中会调用被包装的 init_client_ws_route，从而捕获 ws_handler
    server = server_module.WebSocketServer(config=config)

    ws_handler = _captured.get("ws_handler")
    if ws_handler is None:
        logger.error("桥接失败：未捕获到 WebSocketHandler，/inject 未挂载")
    else:
        server.app.include_router(
            init_inject_route(ws_handler, server.default_context_cache)
        )
        # include_router 追加在 catch-all 前端静态挂载(Mount "/", name="frontend")之后，
        # 请求会先被静态挂载拦截(405)，需把 /inject 路由挪到该挂载之前
        routes = server.app.router.routes
        inject_routes = [r for r in routes if getattr(r, "path", "").startswith("/inject")]
        mount_idx = next(
            (i for i, r in enumerate(routes) if getattr(r, "name", None) == "frontend"),
            len(routes),
        )
        for r in inject_routes:
            routes.remove(r)
        for r in reversed(inject_routes):
            routes.insert(mount_idx, r)
        logger.info("桥接已挂载: POST /inject")

    logger.info("Initializing server context...")
    try:
        asyncio.run(server.initialize())
        logger.info("Server context initialized successfully.")
    except Exception as e:
        logger.error(f"Failed to initialize server context: {e}")
        sys.exit(1)

    logger.info(f"Starting server on {server_config.host}:{server_config.port}")
    uvicorn.run(
        app=server.app,
        host=server_config.host,
        port=server_config.port,
        log_level=console_log_level.lower(),
    )


if __name__ == "__main__":
    args = parse_args()
    console_log_level = "DEBUG" if args.verbose else "INFO"
    if args.verbose:
        logger.info("Running in verbose mode")
    if args.hf_mirror:
        os.environ["HF_ENDPOINT"] = "https://hf-mirror.com"
    run(console_log_level=console_log_level)
