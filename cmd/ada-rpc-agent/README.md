# ada-rpc-agent

把一份 agent preset 装配成工作区级的 RPC 宿主，供 ada_chat 这类桌面壳调用：同一个工作目录
共享一个进程；进程的工作目录就是用户选择的工作目录，`fs-local` 的 `/workspace` 根在装配前被改成该目录。

```bash
go build -o bin/ada-rpc-agent.exe ./cmd/ada-rpc-agent
```

## 环境契约

宿主（ada_chat）启动本进程时设置环境变量，并把 `cmd.Dir` 设成工作目录：

| 变量 | 必填 | 说明 |
|---|---|---|
| `ADA_BOT_ADDR` | 是 | `pipe://name` / `unix:///abs/path` / `tcp://host:port`；token 鉴权 |
| `ADA_BOT_TOKEN` | 是 | RPC token；只走环境，不进行程命令行 |
| `ADA_GO_AGENT_PRESET` | 是 | agent preset 的绝对路径（例：`examples/agent/agent.preset.json`） |
| `ADA_BOT_NAME` | 否 | 机器人名；写入 `system-prompt` 的 identity 与 `bot.info` |
| `ADA_BOT_MODEL` | 否 | 只用于 `bot.info` 展示；实际模型由 preset 决定 |
| `ADA_BOT_INSTRUCTION` | 否 | 追加到 `system-prompt` identity |
| `ADA_BOT_WORKSPACE` | 否 | 工作目录；缺省用进程 cwd |
| `ADA_BOT_CLIENT` | 否 | 允许的客户端名，默认 `ada-chat` |
| `ADA_GO_AGENT_BASE` | 否 | preset 相对路径的基目录；缺省从 preset 目录向上找 `go.mod` |
| `ADA_LLM_PROVIDER` | 否 | 覆盖 preset 的模型供应商：`openai` / `replay`；空表示跟随 preset |
| `ADA_LLM_ENDPOINT` | 否 | OpenAI 兼容的 `/v1` 地址（`openai` 时生效，空则用官方端点） |
| `ADA_LLM_MODEL` | 否 | 请求使用的模型 ID（`openai` 时生效，空则用 `gpt-4o-mini`） |
| `ADA_LLM_API_KEY` | 否 | OpenAI 兼容端点密钥（`llm-openai` 会优先从 secrets 取，缺省回落到该环境变量） |

### 供应商覆盖

`ADA_LLM_PROVIDER` 不为空时，宿主适配器会修改 preset 的已展开清单：

- `openai`：启用 `llm-openai`、禁用 `llm-replay`，把 endpoint/model/apiKeyEnv 写入 `llm-openai` 配置，
  并把 `agent-loop.model` 设为所选模型；
- `replay`：启用 `llm-replay`、禁用 `llm-openai`，`agent-loop.model` 取 replay preset 的默认模型。

因此桌面端可以在可视化设置里切换供应商，而不需要改 preset 文件。

## 协议方法

| 方法 | 参数 | 结果/事件 |
|---|---|---|
| `bot.info` | — | `{name, model, workspace, pid, preset, instruction}` |
| `chat.send` | `{turn, text, botId?, botName?}` | 运行期推送 `chat/delta` / `chat/reasoning` / `chat/tool` / `chat/done`，最后返回 `{turn, text, steps, toolCalls}` |

同一个工作区进程可服务多个机器人：`botId` 不同则各自建一个 agent session，共享同一个进程、
fs/workspace、preset、system prompt 和 LLM provider；`chat.send` 串行执行。`ctx` 被 RPC
取消时透传给 `agent.Run`。

### 事件

`chat.send` 运行期推送四类事件，宿主按 `turn`（工具卡片再按 `id`）合并：

| 事件 | 载荷 | 说明 |
|---|---|---|
| `chat/delta` | `{turn, text}` | 正文增量；非流式 provider 没有增量时，退回整段回放 |
| `chat/reasoning` | `{turn, text}` | 思考过程增量，宿主可做成可折叠区域 |
| `chat/tool` | `{turn, id, name, args, status}`，结果事件另有 `text`、`durationMs` | 工具开始为 `status="running"`，结果按 `ok`/`denied`/`error`/`timeout` 区分；`text` 截断到 8 KiB |
| `chat/done` | `{turn}` | 本轮结束 |

工具事件来自 `tools/pre-execute` 与 `tools/result`，只做转发，不参与策略判定（事件不是门禁）。

## 示例

```powershell
$ws = "$env:TEMP\ada-agent-workspace"
New-Item -ItemType Directory -Force $ws | Out-Null
Set-Content "$ws\a.txt" "hello"

$env:ADA_BOT_ADDR = "pipe://ada-agent-demo"
$env:ADA_BOT_TOKEN = "demo-token"
$env:ADA_BOT_NAME = "代码助手"
$env:ADA_BOT_WORKSPACE = $ws
$env:ADA_GO_AGENT_PRESET = (Resolve-Path "examples\agent\agent.preset.json").Path
$agent = (Resolve-Path "bin\ada-rpc-agent.exe").Path
Push-Location $ws
& $agent
```

ada_chat 侧只设置宿主变量即可（它不再内置 `--bot` 演示运行时）：

```powershell
$env:ADA_BOT_RUNTIME = "E:\codes\go_projects\ada_go_v2\bin\ada-rpc-agent.exe"
$env:ADA_GO_AGENT_PRESET = "E:\codes\go_projects\ada_go_v2\examples\agent\agent.preset.json"
# 在 ada_chat 仓库里：
go run ./cmd/ada_chat
```

## 已知边界

- **per-agent 的 prompt/model/provider 还没有隔离**：同一进程里的多个 bot 共享 preset 与 system prompt；
  项目群组式的多 agent 需要 ada_go_v2 引入显式 agent 实例，而不是只按 botId 切 session。
- replay provider 的游标是进程级的；多个 bot 共用 replay preset 时会耗尽脚本。真实 LLM 不受此限。
- 工具结果只回传文本块（截断 8 KiB），图片/附件等非文本内容不进事件；完整结果仍在 `chat.send` 的返回值里。
- `chat.send` 串行：同一进程里的其他 bot 要等当前轮结束，进度事件也因此只有一条轮次线。
- preset 里的相对文件路径按 ada_go_v2 模块根解析（可用 `ADA_GO_AGENT_BASE` 覆盖）。
- 仍由宿主负责重启/重连策略；本进程不自动重连。
