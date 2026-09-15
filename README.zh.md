# llm-vup-bridge

让 LLM-Vup 的 Live2D 形象对弹幕/礼物/SC 自动出声、动嘴、出表情。

```
弹幕事件 → distillery:9528 → LLM 分析 → TTS 播报
                              ├─ Mimo 直连（优先）
                              └─ POST :12393/inject → LLM-Vup TTS 引擎
```

## 连续对话工作台

默认风格是自然俏皮的 Mili：先回应内容，偶尔轻轻吐槽。取消强制长短句、逐句换情绪和 TTS 短句加台词，情绪标签不会进入朗读正文。

工作台分为「聊天与角色」「模型与语音」「事件与诊断」：

- 连续聊天：默认只测试文字，取消勾选后可生成语音；支持取消生成、停止本页音频、清空上下文和导出对话。
- 角色、上下文轮数和输出位置保存后立即对后续请求生效，存储在配置目录的 studio_config.json。
- 输出可选自动、本页 MiMo 试听、主项目 Live2D、仅文字。主项目模式会把 MiMo 音频直接注入 Live2D；没有 MiMo 时使用主项目 TTS。
- 检测主项目可查看接口是否挂载、角色、前端连接数量和播报状态。
- 连续聊天跳过直播回复概率和冷却；事件面板继续使用实际直播调度策略。

上下文按平台与房间隔离，无房间时按用户 ID（缺省为用户名）隔离。Web 聊天使用独立会话，不混入直播。默认最近 8 轮，最多 20 轮、128 个会话，空闲 30 分钟后过期；重启清空。只有已接受的回复进入上下文，直播中被丢弃的回复不进入历史。

## 一起启动（无需复制文件到主项目）

依赖：Go 1.26.4+；一起启动还需要 uv，以及已安装依赖、配置模型和语音引擎的 LLM-Vup。脚本自动识别相邻的 ../LLM-Vup，可用 LLM_VUP_ROOT 指定其它位置。

```bash
cd /home/demo/Agentropism/llm-vup-bridge
bash scripts/start.sh --debug  # 只启动 Web 调试台
# 或一起启动桥接与主项目（主项目依赖需事先准备好）
bash scripts/start.sh
```

脚本会构建 Go 二进制；首次运行从示例生成本仓库的 config.json，不覆盖已有配置。打开 http://localhost:9528/debug，在「模型与语音」配置接口，再回到「聊天与角色」测试。主项目模式需先保存地址并打开 Live2D 页面。自定义配置可设置 BRIDGE_CONFIG；Ctrl+C 会结束脚本启动的两个服务。

桥接只在运行时挂载接口，不需要修改 LLM-Vup 源文件。主项目自身仍使用原有配置、缓存和资源目录。

新增接口：

| 接口 | 用途 |
|---|---|
| GET/POST /debug/studio | 读取/保存角色、上下文与输出设置 |
| POST /debug/chat | 连续聊天，参数 session、text、user、mute；返回 result 与 audio_urls |
| POST /debug/chat/clear | 清空指定 session 的 Web 聊天上下文 |
| GET /debug/integration | 检查已保存地址的桥接状态 |
| GET /inject/health（主项目端） | 前端数量、角色、播报状态和音频注入能力 |

## 组成

| 部分 | 技术 | 说明 |
|---|---|---|
| `bridge/` | Python | `/inject` 注入服务，挂载到 LLM-Vup 的 FastAPI |
| `cmd/distillery` | Go | 事件管线：弹幕/礼物 → LLM 分析 → 调度 → TTS |
| `internal/tts/mimo.go` | Go | Mimo TTS 直连客户端，含完整情绪智能 |
| `internal/dispatch/` | Go | 发言调度、冷却、礼物/SC 模板 |
| `internal/emotion/` | Go | 情绪→语音参数映射 |
| `internal/llm/` | Go | LLM 分析客户端 |
| `prompts/` | 文本 | LLM 基础 prompt（编译时内嵌；角色可在工作台修改） |

## 快速开始

### 准备 LLM-Vup

```bash
cd /path/to/LLM-Vup
# 确保 conf.yaml 已配置、avatars/ 目录存在、Live2D 模型就绪
```

### 启动桥接服务 (:12393)

```bash
cd /path/to/LLM-Vup
LLM_VUP_ROOT=/path/to/LLM-Vup uv run --project /path/to/LLM-Vup python /path/to/llm-vup-bridge/bridge/serve.py --verbose
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

distillery 内置单消费者优先级调度器（`internal/dispatch/scheduler.go`），所有发言任务经 `POST /event` 入队后**串行执行处理任务**。主项目注入会按音频时长等待再处理下一段；这是时长同步，不是前端播放确认：

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

## TTS 输出与情绪

工作台的输出设置优先。自动模式维持 MiMo → VTuber → synapse-tts 的选择顺序；这是配置选择，不是请求失败后的自动切换。

- 本页试听：MiMo 生成音频，浏览器试听，不发送给主项目。
- 主项目：有 MiMo 时生成音频再通过 /inject 发送音频、字幕和表情；没有 MiMo 时由主项目合成。
- 仅文字：不调用 TTS。

MiMo 根据情绪和强度轻微调整语速，最终限制在 0.8–1.2。短句原样朗读，不追加尾缀；单次试听可以覆盖音色、格式与语速。synapse 的情绪调节幅度也已减小。

礼物与舰长使用简短感谢模板。SC 在模型已配置时由 LLM 连贯地感谢并回应内容；未配置模型时使用感谢与留言模板，明确不能生成问题答案。整个 SC 回复一次合成，不再插入固定 1.5 秒间隔。

## POST /inject 协议

```json
{"text": "要说的话", "emotion": "joy", "intensity": 0.9}
```

- `text`（必填）
- `emotion`（可选）：emo_map 英文标签 `joy|sadness|anger|fear|surprise|smirk|neutral|disgust`
- `intensity`（可选）：0.0~1.0；主项目现有表情协议暂无强度通道。
- `audio_base64`（可选）：已合成音频的 Base64，不含 data URL 前缀。
- `audio_format`（可选）：mp3 或 wav。提供音频时主项目直接播放，跳过自身 TTS。

响应：
- `200 {"status":"ok","clients":N}` — 已广播
- `400` — 空文本
- `409` — 无前端连接

## distillery HTTP 接口

| 接口 | 说明 |
|---|---|
| `POST /event` | 统一事件入口，body 为 UnifiedEvent |
| `POST /test` | 同步测试：`{"text","user","type","emotion"}` |
| `POST /tts/mimo` | Mimo 直连测试：`{"text","emotion","speed","voice","format"}`，音色/格式/语速可按次覆盖 |
| `GET /health` | 健康检查 |
| `GET /debug` | 调试台页面（见下节） |
| `GET /debug/config` | 当前生效配置（密钥只回是否已配置） |
| `GET /debug/log?n=50` | 最近事件处理记录（内存环形缓冲，200 条） |
| `POST /debug/queue` | 等价 `POST /event`，额外返回 `seq`/`accepted`/`priority` |
| `GET /debug/audio/{name}` | **试听**：播放 `cache/` 下的音频文件，支持 Range（可拖动进度条） |
| `GET /debug/providers` | 内置模型服务商预设 + 当前生效的模型配置（Key 只回掩码；含接口类型错配提示） |
| `POST /debug/model` | **切换模型**：`{"provider","endpoint","model","api_key"}`，立即生效并落盘 |
| `POST /debug/model/test` | 用当前配置发一次最小请求，验证 endpoint / Key / 模型 |
| `GET /debug/mimo` | 试听（Mimo TTS）配置状态：是否已启用、是否待重启、Key 掩码 |
| `POST /debug/mimo` | **配置试听**：`{"api_key","base_url","model","voice","format"}`，写入 `mimo_config.json` |

## 配置文件

`distillery` 的配置来自 `--config`。**未显式指定时会自动查找**：当前目录 → 可执行文件所在目录 → 其上级目录；都没找到才用内置默认值。查找结果与解析后的绝对路径都会打印在启动日志里。

**配置读取失败不会被静默忽略**：

| 情况 | 行为 |
|---|---|
| 文件正常 | 正常启动 |
| `--config` 指向的文件不存在 | ✖ 拒绝启动，退出码 1（路径写错立刻暴露） |
| 文件存在但 JSON 非法 | ✖ 拒绝启动，并打印解析错误；提示里附 `python3 -m json.tool` 校验命令 |
| 自动查找都没找到 | ⚠ 警告后以默认值启动，并说明后果（Mimo 未配置 → 无音频缓存 → 试听不可用） |

环境变量（`LLM_*` / `MIMO_*` / `TTS_ADDR` / `VTUBER_ADDR` / `LISTEN_ADDR`）会覆盖文件值，**启动日志会列出实际生效的变量名**，避免"改了文件却没生效"的错觉。

调试台顶部也会显示同样信息：配置缺失/损坏时红色横幅，环境变量覆盖时蓝色提示。所以不必翻日志就能判断"是不是配置根本没读进去"。

### 接口与用途不匹配检测

模型配置和 TTS 配置的形状完全一样（都是 OpenAI 兼容的 `/v1`），把 **TTS 接口填进模型配置**是最容易犯的错——能保存成功，但意图分析永远失败。调试台会识别已知的语音服务域名并给出明确纠正：

> ⚠ 接口类型不匹配
> 这个地址（小米 MiMo TTS）是语音合成接口，不是对话模型接口。用它做意图分析会一直失败（模型不会返回要求的 JSON）。如果这是给试听用的 TTS Key，请填到「Mimo TTS 配置」里；模型配置请改选 DeepSeek / OpenCode Go / OpenAI。

识别规则在 `internal/provider/classify.go`（已知域名 + 路径关键词 + 预设反查），未识别的自定义地址（如本地 Ollama）不会误报。

## Mimo TTS 配置（试听用）

**不需要手写 config.json**：调试台的「Mimo TTS 配置」面板填 Key / 音色 / 格式 / 地址 / 模型，保存到 `mimo_config.json`（0600，与 config.json 同目录）。

- 该文件**优先于** `config.json` 的 `mimo` 段。
- Mimo 客户端在启动时构造，所以保存后**需要重启 distillery** 才会用于生成音频；接口返回 `restart_required` 与 `restart_hint`，界面会明确显示"已保存，重启后生效"，不会假装已经能用。
- 面板顶部实时显示三态：✅ 当前进程已启用 / ⚠ 已保存待重启 / ✖ 未配置（试听不可用）。


## 模型配置

调试台的「模型配置」区块支持一键切换服务商，**保存后立即生效，不需要重启**（下一条消息就用新模型）。内置预设（都走 OpenAI 兼容的 `/chat/completions`，因此无需按服务商分支）：

| 服务商 | 接口地址 | 内置模型 |
|---|---|---|
| DeepSeek | `https://api.deepseek.com/v1` | `deepseek-chat`、`deepseek-reasoner` |
| OpenCode Go | `https://opencode.ai/zen/go/v1` | `deepseek-v4-flash`、`deepseek-v4-pro`、`kimi-k2.7-code`、`glm-5.2`、`mimo-v2.5` 等 13 个 |
| OpenAI | `https://api.openai.com/v1` | `gpt-4o-mini`、`gpt-4o`、`gpt-4.1-mini`、`gpt-4.1`、`o4-mini` |

模型名支持直接输入（datalist 可编辑），也可选「自定义」填任意 OpenAI 兼容 base URL。

### 行为

- **保存并启用**：写入内存后立即对后续消息生效，同时落盘到 `--model-config`（默认 `model_config.json`，**0600，含密钥**）。文件长这样，也可手工编辑后重启：
  ```json
  { "llm": { "endpoint": "https://opencode.ai/zen/go/v1",
             "model": "deepseek-v4-flash", "api_key": "sk-xxx" } }
  ```
  该文件优先于 `config.json` 与环境变量，因此调试台的改动不会被启动配置覆盖。
- **文件写在哪**：所有相对路径都以 **`config.json` 所在目录**为基准（不是进程工作目录），所以从任何目录启动都会读写同一份配置，不会出现「保存了但找不到文件」。启动日志会打印解析后的绝对路径：
  ```
  [distillery] 配置文件: /srv/vup/config.json | 模型配置保存到: /srv/vup/model_config.json | 音频缓存: /srv/vup/cache
  ```
  音频缓存同理，可用 `mimo.cache_dir` 覆盖（默认为 `config.json` 同级的 `cache/`）。
- **路径不可写时**：启动阶段就会预检并打印 `⚠ 模型配置路径不可写…`；此时保存会**立即生效但无法持久化**，接口返回 `warning` 字段，界面显示黄色告警（而不是假装成功）。
- **测试连接**：用当前已保存的配置发一次最小请求，回显耗时与模型回复；失败时透传服务商原始报错（例如 `HTTP 401 authentication_error: Invalid API key`），便于区分「Key 错」和「地址/模型错」。
- **API Key 处理**：只回显掩码（`********1234`），不回传明文。已保存过 Key 时界面出现「沿用已保存的 Key」勾选；**勾选才复用**，否则空 Key 直接报错——避免切换服务商时误用上一家的 Key。切换服务商时该勾选会自动取消。
- **未配置模型时**：普通消息直接记为「未配置 LLM 模型」，不再傻等 30s 超时；礼物/SC/舰长仍走纯模板，调试台与试听照常可用。

模型与端点的对应关系锁在 `internal/provider/provider.go`，并有单测固定（写错端点会导致全部请求 404）。列表是手工维护的常用集，若服务商上线新模型，直接在界面输入模型名即可，无需改代码。

## 调试台

浏览器打开 <http://localhost:9528/debug> 即可。单页 HTML 由 `go:embed` 编译进二进制（`internal/debugui/index.html`），**无前端构建步骤、无外部依赖、无 CDN**。

| 区块 | 用途 |
|---|---|
| 运行状态 | 实际生效的 TTS 路径（mimo-direct / vtuber-inject / synapse-tts）、音频缓存目录、LLM 服务商与模型、冷却、队列容量与时限、积压加速、各 intent 回复概率 |
| 模型配置 | 切换 DeepSeek / OpenCode Go / OpenAI，保存即生效并落盘；一键测试连接；接口类型错配提示 |
| Mimo TTS 配置 | 填 Key/音色/格式即可试听，落 `mimo_config.json`，明确提示"重启后生效" |
| 优先级 / 情绪参考 | 类型→优先级→可否打断→排队时限；八种情绪→rate/pitch/volume |
| 队列提交 | 走 `POST /event` 完整链路（异步），可一键连发 5 条，用于验证串行播报、插队、超时丢弃、积压加速 |
| 同步测试 | 走 `POST /test`，直接返回 `ProcessResult`（含 `audio_paths`） |
| Mimo TTS 试听 | 走 `POST /tts/mimo`，可选情绪/音色/格式/语速，生成后**页面内直接播放**，并给下载链接 |
| 最近事件 | 每条事件的优先级、结果（已发声/跳过/TTS失败/超时丢弃/被淘汰/被打断）、排队耗时、处理耗时、回复文本、情绪，以及**每段音频的试听按钮** |

### 试听

- 每个事件生成的所有音频片段都会记录在调试台里，**每段一个原生 `<audio controls>` 播放器**（自带进度条、音量、暂停），可逐段播放；SC 的感谢与内容回应使用同一段音频。
- 播放状态完全由浏览器维护，不由 JS 变量记账——因此自动刷新重建表格不会造成"显示 ▶ 但还在响"或"多段同时播放"。正在播放时自动刷新会自动跳过，避免打断。
- 同一时刻只播放一段：开始播新的会自动暂停其它（表内事件委托保证）。
- `GET /debug/audio/{name}` 用 `http.ServeContent` 提供，支持 Range 请求，可直接拖动进度条（`206 Partial Content`）。
- 只接受 `cache/` 下的单层文件名（`mimo_` 前缀），拒绝任何含 `/`、`\`、`..` 的路径，避免目录穿越。
- 未配置 `mimo.api_key` 时试听端点返回 404 并提示原因，调试台会把音色下拉与生成按钮置灰。
- 音频目录为空、文件被清理时，播放器会自己显示加载失败，不影响页面其它功能。

### 脱离主项目独立调试

调试台与试听**不依赖 LLM-Vup、synapse-tts 或任何 LLM**：

```bash
cd llm-vup-bridge
go build -o distillery ./cmd/distillery
mkdir -p /tmp/dbg && cd /tmp/dbg
cat > config.json <<'JSON'
{ "listen_addr": ":9528",
  "mimo": { "api_key": "sk-xxx", "voice": "冰糖", "format": "mp3" } }
JSON
/path/to/distillery --config config.json   # 打开 http://localhost:9528/debug
```

- 只填 `mimo.api_key` 即可试听全部情绪与音色；不需要 LLM key，也不需要 LLM-Vup 在跑。
- 没有 LLM key 时，礼物/SC/舰长走纯模板回复，仍可完整验证调度、情绪与试听链路；普通消息会记为 `llm_error`。
- 一个发声后端都没配时，结果会明确记为「未配置任何 TTS 后端（仅生成文本回复）」，而不是误报 TTS 失败。
- 音频文件按 `<cache目录>/mimo_<来源>_<纳秒>.<格式>` 命名，来源前缀区分 `gift_thanks` / `sc_thanks` / `sc_followup` / `llm_reply` / `test`。

事件结果由 `internal/debugui` 的环形缓冲记录：入队即写一条，`Speak` 完成后回填结果，被丢弃的任务由 `SpeakTask.OnDrop` 标注原因。记录只在内存中，重启即清空。

基础提示词编译进二进制，更新 prompts/intent_analysis.txt 后需要重新构建。角色与语气设定可在工作台保存后立即应用。

## 验证

```bash
# Mimo TTS 试听（返回 url 字段，浏览器可直接打开播放）
curl -X POST localhost:9528/tts/mimo -H 'Content-Type: application/json' \
  -d '{"text":"喂你这个笨蛋！","emotion":"anger","voice":"茉莉"}'

# 礼物测试
curl -X POST localhost:9528/test -H 'Content-Type: application/json' \
  -d '{"text":"火箭","user":"老板","type":"gift"}'
```

## 已知限制

- 主项目注入按音频时长串行等待，尚无前端播放完成确认；主项目自身对话与主动发言未纳入桥接队列。
- 取消生成会取消在途请求；已发给 Live2D 的音频不能由本页停止按钮打断。本页播放器全局互斥。
- MiMo 配置沿用原来的保存后重启机制；模型与角色/输出设置支持立即生效。
- 上下文保存在内存中，不是长期记忆；礼物感谢仍为模板。

## 验证

```bash
CGO_ENABLED=0 go test ./...
node --check internal/debugui/studio.js
bash -n scripts/start.sh
```

自动测试使用本机模拟 HTTP 服务验证连续对话、上下文隔离/清空、配置持久化、语音正文和音频注入。实际听感与 Live2D 播放需配置真实服务后试听。

## 许可证

MIT
