# llm-vup-bridge

让 **LLM-Vup**（Open-LLM-VTuber）的 Live2D 形象
对**弹幕/礼物/SC 等事件**自动出声、动嘴、出表情

```
弹幕事件 → distillery:9528 → LLM 分析 → POST :12393/inject {text, emotion, intensity}
                                          ↓ LLM-Vup TTS 引擎合成
                              广播到所有 /client-ws 前端：
                                control: conversation-chain-start
                                audio 帧(base64 wav + volumes + display_text + actions.expressions)
                                backend-synth-complete / force-new-message / conversation-chain-end
```

## 组成

| 部分 | 技术 | 说明 |
|---|---|---|
| `bridge/` | Python | `/inject` 注入服务。复用 LLM-Vup 进程启动，把路由挂进同一个 FastAPI app |
| `cmd/distillery` + `internal/` | Go | 事件管线：弹幕/礼物 → LLM 意图分析 → 冷却调度 → 调 `/inject` |
| `prompts/` | 文本 | LLM 意图分析 prompt（运行时加载） |

## 快速开始

### 1. 准备 LLM-Vup / Open-LLM-VTuber

```bash
# 克隆到本模块根目录下（或用 LLM_VUP_ROOT 指向任意位置）
git clone <你的 LLM-Vup 或 Open-LLM-VTuber 仓库> LLM-Vup
cd LLM-Vup
```

仓库需要（均为 gitignored，不影响仓库干净）：

- `conf.yaml`：参照 `config_templates/` 配置好角色、Live2D 模型、ASR/TTS 引擎
- `avatars/` 目录：`mkdir avatars`（缺失会启动报错）
- ASR 模型（如用 sherpa-onnx 系列，下载到 `models/`）
- 前端：`frontend/`（按上游说明准备）

### 2. 启动桥接服务（:12393）

```bash
cd LLM-Vup && uv run python ../bridge/serve.py --verbose
# 启动日志出现「桥接已挂载: POST /inject」即成功
```

### 3. 启动 distillery（:9528）

```bash
cp config.example.json config.json   # 填入 LLM API key 等
CGO_ENABLED=0 go build -o distillery ./cmd/distillery
./distillery
```

无 config.json 时用内置默认值；所有配置项均可被同名大写环境变量覆盖
（`VTUBER_ADDR` / `LLM_ENDPOINT` / `LLM_MODEL` / `LLM_API_KEY` / `TTS_ADDR` / `LISTEN_ADDR`）。

**`vtuber_addr` 置空字符串**则不走桥接，回退到 synapse-tts 本地放音（`tts_addr`）。

### 4. 验证

```bash
# 无前端连接时 → 409
curl -X POST localhost:12393/inject -H 'Content-Type: application/json' -d '{"text":"测试"}'

# 礼物事件（不走 LLM，无需 API key）→ 打开 LLM-Vup 前端即可看到字幕+表情
curl -X POST localhost:9528/test -H 'Content-Type: application/json' \
  -d '{"text":"火箭","user":"老板","type":"gift"}'
```

## POST /inject 协议

请求：

```json
{"text": "要说的话", "emotion": "joy", "intensity": 0.9}
```

- `text`（必填）：非空，否则 400
- `emotion`（可选）：**Live2D 模型 emo_map 的英文标签**，不在 emo_map 内的标签会被过滤（无表情）。
  mao_pro 模型可用：`neutral` `anger` `disgust` `fear` `joy` `smirk` `sadness` `surprise`
- `intensity`（可选，保留字段）：当前协议无强度通道，暂不使用

响应：

- `200 {"status":"ok","clients":N}` — 已广播（请求会等 TTS 合成完才返回）
- `400 {"error":"empty text"}`
- `409 {"error":"no frontend connected"}` — 没有前端连 `/client-ws`
- `500 {"error":"..."}` — 合成/广播异常；TTS 失败时自动降级为静音字幕帧（audio=null）

## distillery HTTP 接口

| 接口 | 说明 |
|---|---|
| `POST /event` | 统一事件入口（异步处理），body 为 UnifiedEvent |
| `POST /test` | 同步测试入口：`{"text","user","type"}`，type ∈ `text/gift/super_chat/captain` |
| `POST /tts/stop` · `POST /tts/skip` | 停止 / 跳过当前播报（仅 synapse-tts 回退路径） |
| `GET /tts/status` · `GET /health` | 状态与健康检查 |

礼物/SC/舰长直接生成感谢词（`emotion=joy`），不消耗 LLM 调用；普通消息经 LLM
分析 `(intent_type, emotion, intensity, reply_text, ...)` 后按权重与冷却调度播报。

## 已知限制

- 前端 interrupt 不打断注入播放（注入未注册进 `current_conversation_tasks`）
- `intensity`、语速/音调不传给 LLM-Vup TTS（按其角色配置走）
- 并发注入音频帧可能交叠；distillery 冷却调度实际会串行化
- 无前端连接时 distillery 回复只记日志（409），不回退本地放音
- edge_tts 需能访问外网；否则请在 LLM-Vup `conf.yaml` 改用本地 TTS 引擎

## 许可证

MIT（见 `LICENSE`）。LLM-Vup / Open-LLM-VTuber 是独立的上游项目，
本仓库不包含、也不修改其代码，运行时通过 `LLM_VUP_ROOT` 引用你自己的检出。
