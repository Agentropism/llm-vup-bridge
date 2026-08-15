# llm-vup-bridge

让 LLM-Vup 的 Live2D 形象对弹幕/礼物/SC 自动出声、动嘴、出表情。

```
弹幕事件 → distillery:9528 → LLM 分析 → TTS 播报
                              ├─ Mimo 直连（优先）
                              └─ POST :12393/inject → LLM-Vup TTS 引擎
```

## 组成

| 部分 | 技术 | 说明 |
|---|---|---|
| `bridge/` | Python | `/inject` 注入服务，挂载到 LLM-Vup 的 FastAPI |
| `cmd/distillery` | Go | 事件管线：弹幕/礼物 → LLM 分析 → 调度 → TTS |
| `internal/tts/mimo.go` | Go | Mimo TTS 直连客户端，含完整情绪智能 |
| `internal/dispatch/` | Go | 发言调度、冷却、礼物/SC 模板 |
| `internal/emotion/` | Go | 情绪→语音参数映射 |
| `internal/llm/` | Go | LLM 分析客户端 |
| `prompts/` | 文本 | LLM 分析 prompt（运行时加载） |

## 快速开始

### 准备 LLM-Vup

```bash
cd /path/to/LLM-Vup
# 确保 conf.yaml 已配置、avatars/ 目录存在、Live2D 模型就绪
```

### 启动桥接服务 (:12393)

```bash
cd /path/to/LLM-Vup
sudo LLM_VUP_ROOT=$(pwd) uv run python ./bridge/serve.py --verbose
# 看到「桥接已挂载: POST /inject」即成功
```

### 启动 distillery (:9528)

```bash
cd llm-vup-bridge
cp config.example.json config.json   # 编辑 config.json
CGO_ENABLED=0 go run ./cmd/distillery/
```

## 配置

### config.json

```jsonc
{
  "listen_addr": ":9528",
  "tts_addr": "http://localhost:9527",          // synapse-tts 回退地址
  "vtuber_addr": "http://localhost:12393",       // LLM-Vup /inject 地址
  "mimo": {                                      // Mimo 直连（非空优先，绕过 LLM-Vup）
    "api_key": "sk-xxxxx",
    "base_url": "https://api.xiaomimimo.com/v1",
    "model": "mimo-v2.5-tts",
    "voice": "冰糖",                              // 冰糖/茉莉/苏打/白桦/Mia/Chloe/Milo/Dean
    "format": "mp3"
  },
  "llm": {
    "endpoint": "https://api.openai.com/v1",
    "model": "gpt-4o-mini",
    "api_key": "sk-xxx"
  },
  "speech": {
    "cooldown_sec": 5,
    "gift_bypass_cooldown": true,
    "reply_chance": { "chat": 0.6, "greeting": 0.8, "question": 0.95, "command": 0.9, "gift_thanks": 1.0 }
  }
}
```

### 环境变量

所有配置项可被同名大写环境变量覆盖：

| 变量 | 对应配置 |
|------|----------|
| `MIMO_API_KEY` | Mimo API Key |
| `MIMO_VOICE` | Mimo 音色 |
| `VTUBER_ADDR` | LLM-Vup 地址 |
| `LLM_ENDPOINT` / `LLM_MODEL` / `LLM_API_KEY` | LLM 配置 |
| `TTS_ADDR` | synapse-tts 地址 |
| `LISTEN_ADDR` | distillery 监听地址 |

## 发言优先级调度（PRD 第四部分）

distillery 内置单消费者优先级调度器（`internal/dispatch/scheduler.go`），所有发言任务经 `POST /event` 入队后**串行执行，TTS 播放互不重叠**：

| 行为 | 规则 |
|------|------|
| 消费顺序 | 按优先级插队：舰长 > SC > 礼物 > 普通消息；同优先级先到先得 |
| 超时丢弃 | 排队超过 `ttl_text_sec`（普通消息）/ `ttl_gift_sec`（礼物/SC/舰长）直接丢弃 |
| 打断 | SC 感谢/礼物/舰长等系统事件**不可打断**；普通 LLM 互动可被更高优先级任务打断（synapse-tts 走 `/stop`，Mimo/注入路径取消在途请求） |
| 积压加速 | 排队任务数 ≥ `backlog_threshold` 时，普通消息语速提升 `backlog_speed_boost`（Mimo 为 speed 增量，synapse 为 rate 百分比增量）；系统事件保持原速 |
| 队列满 | 淘汰优先级最低的排队任务；新任务优先级不高于被淘汰者时丢弃新任务 |

`speech` 配置新增字段：

```jsonc
"speech": {
  "cooldown_sec": 5,
  "gift_bypass_cooldown": true,
  "reply_chance": { "chat": 0.6, "greeting": 0.8, "question": 0.95, "command": 0.9, "gift_thanks": 1.0 },
  "ttl_text_sec": 10,          // 普通消息排队时限（秒）
  "ttl_gift_sec": 60,          // 礼物/SC/舰长排队时限（秒）
  "queue_max_size": 64,        // 队列容量，0=不限
  "backlog_threshold": 3,      // 积压加速阈值，0=禁用
  "backlog_speed_boost": 0.15  // 积压加速量
}
```

`POST /test` 为同步测试端点，不经过优先级队列。待机动画打断属前端行为，不在本仓库实现。

## TTS 路径优先级

1. **Mimo 直连**（`mimo.api_key` 非空）→ 直接调用 Mimo API，绕过 LLM-Vup
2. **VTuber 注入**（`mimo.api_key` 为空 + `vtuber_addr` 非空）→ `POST /inject`
3. **synapse-tts 回退**（两者均为空）→ `POST /speak`

## Mimo TTS 情绪控制

Mimo 引擎内置完整的情绪—语音参数映射：

| 情绪 | 表演指令 | speed | Mimo emotion |
|------|----------|-------|-------------|
| anger | 超愤怒暴躁、大声吼出来 | 1.35 | angry |
| joy | 超级开心兴奋、笑出声 | 1.25 | happy |
| sadness | 委屈带哭腔、声音颤抖 | 0.75 | sad |
| fear | 害怕慌张、倒吸凉气 | 1.4 | fearful |
| surprise | 惊讶不可置信、声音上扬 | 1.3 | surprised |
| smirk | 嘲讽冷笑、阴阳怪气 | 1.08 | happy |
| disgust | 嫌弃厌恶、皱着眉头 | 1.15 | angry |
| neutral | 自然放松、随意轻快 | 1.0 | default |

**短句增强（≤6 字）**：自动追加戏剧化尾缀并提升 speed，让短句不干瘪。

## 礼物/SC 处理

- **礼物**：按价值分级（小/中/大），Mili 傲娇风格感谢
- **SC**：两段式——先感谢（joy），1.5 秒后念内容并回应（smirk）
  - 自动分类 SC 内容（夸赞/提问/其他）生成不同风格的跟进回复
- **舰长**：惊喜欢迎（surprise）

内置礼物关键词：火箭、城堡、星球、嘉年华、总督、提督 → 大；飞船、摩天轮、烟花、告白 → 中

## POST /inject 协议

```json
{"text": "要说的话", "emotion": "joy", "intensity": 0.9}
```

- `text`（必填）
- `emotion`（可选）：emo_map 英文标签 `joy|sadness|anger|fear|surprise|smirk|neutral|disgust`
- `intensity`（可选，保留）：0.0~1.0

响应：
- `200 {"status":"ok","clients":N}` — 已广播
- `400` — 空文本
- `409` — 无前端连接

## distillery HTTP 接口

| 接口 | 说明 |
|---|---|
| `POST /event` | 统一事件入口，body 为 UnifiedEvent |
| `POST /test` | 同步测试：`{"text","user","type","emotion"}` |
| `POST /tts/mimo` | Mimo 直连测试：`{"text","emotion"}` |
| `GET /health` | 健康检查 |

## 验证

```bash
# Mimo TTS 测试
curl -X POST localhost:9528/tts/mimo -H 'Content-Type: application/json' \
  -d '{"text":"喂你这个笨蛋！","emotion":"anger"}'

# 礼物测试
curl -X POST localhost:9528/test -H 'Content-Type: application/json' \
  -d '{"text":"火箭","user":"老板","type":"gift"}'
```

## 已知限制

- 前端 interrupt 不打断注入播放
- 无需 LLM-Key 时礼物/SC 仍可工作（纯模板），文本消息需要 LLM API Key
- Mimo 直连时音频缓存到本地 `cache/` 目录

## 许可证

MIT
