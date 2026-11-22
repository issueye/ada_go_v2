# 终端交互宿主:platform/host/tui

> 状态:**已实现(2026-11-21)**。命令 `ada-tui` 装配 preset、开一个 agent 会话,
> 用行式输入连续对话;窗口/输入映射在宿主,agent 语义仍由插件承担。
>
> 配套 [AGENTS.md](../AGENTS.md) §2.2、[host-cli.md](host-cli.md)、[workspace-plugin.md](workspace-plugin.md)、[ADR 0011](adr/0011-tui-host-budget.md)。

## 1. 为什么不是插件

TUI 是终端的**窗口/输入/输出映射**,不是 agent 能力:

- 它不定义「agent 是什么、怎么跑一轮」;
- 它不自绘业务 UI,也不在插件里写布局/文案(AGENTS §4 #12);
- 它只装配 preset,把 stdin 的一行交给 `core/agent-loop`,把结果写回终端。

所以按 AGENTS §2.2,它属于**宿主适配器**,放在 `platform/host/tui`;入口
`cmd/ada-tui` 只做 `os.Exit(tui.Run(...))`。如果坚持把它做成插件,需要先改
ADR 的反转口径,不能直接违反 §4 #12。

## 2. 范围

```
cmd/ada-tui/main.go          入口:os.Exit(tui.Run(os.Args[1:], stdin, stdout, stderr))
platform/host/tui/tui.go     参数解析、装配、会话循环、终端样式
```

装配复用与 CLI 相同的路径:

```text
preset/目录 ─▶ assemble.Plan ─▶ loader.Lookup(builtins) ─▶ assemble.Up
      ─▶ kernel.Service("agent") ─▶ agent.Open ─▶ 行式输入循环
```

## 3. 用法

```powershell
go run ./cmd/ada-tui --preset examples/agent/agent.preset.json
go run ./cmd/ada-tui --preset examples/agent/real.preset.json       # 真实端点,需 ADA_LLM_API_KEY
go run ./cmd/ada-tui --preset examples/workspace/workspace.preset.json --workspace docs
```

更方便的封装脚本 [scripts/tui.ps1](../scripts/tui.ps1):

```powershell
pwsh scripts/tui.ps1                                   # 默认 workspace replay
pwsh scripts/tui.ps1 -Workspace docs
pwsh scripts/tui.ps1 -Real -Workspace docs             # 真实端点;先设置 ADA_LLM_API_KEY
pwsh scripts/tui.ps1 -Preset examples/agent/agent.preset.json
pwsh scripts/tui.ps1 -Plain                            # 无 ANSI
pwsh scripts/tui.ps1 -Build                            # 强制重编 bin/ada-tui
```

脚本行为:

- 默认 preset 是 `examples/workspace/workspace.preset.json`(replay);
- `-Real` 切到 `examples/workspace/real.preset.json`,并在缺少 `ADA_LLM_API_KEY` 时直接报错,不落盘 key;
- 优先用 `bin/ada-tui(.exe)`;不存在或 `-Build` 时现场 `go build -trimpath -o` 一个入口;
- 参数原样转发,退出码沿用 `ada-tui`(0/1/2)。

| 参数 | 语义 |
|---|---|
| `<目录>` / `--preset <文件>` | 与 `ada plan/up` 相同;两者只能给一个 |
| `--workspace <id>` | 启动会话绑定到指定工作区;preset 必须提供 workspace 服务 |
| `--plain` | 不输出 ANSI 清屏/颜色;测试、管道、无 VT 终端使用 |

退出码:0 正常退出 / 1 装配或运行失败 / 2 用法错误。

## 4. 会话内命令

| 命令 | 行为 |
|---|---|
| 任意文本 | 交给 agent 跑一轮,打印最终文本 |
| `/workspace` | 显示当前会话绑定的工作区(没有绑定则显示 `Current`) |
| `/workspace <id>` | 校验 ID 后**开一个新会话**并绑定该工作区;旧会话不迁移 |
| `/session` | 打印会话 ID 与工作区 |
| `/clear` | ANSI 清屏(plain 模式无操作) |
| `/help` | 命令帮助 |
| `/quit` / `/exit` | 退出 |

工作区切换刻意开新会话,而不是改当前会话的 `Meta.Workspace`:会话事件是
append-only,中途改绑定会破坏 replay/fork 的因果。

## 5. 终端行为

- 行式输入:没有 raw mode、没有方向键编辑、没有多行输入;每行一次 turn。
- 输出不流式:agent-loop 目前只返回整轮文本;工具调用/增量 token 未展示。
- ANSI 只用于清屏、标题和提示符;`--plain` 时全部关闭。
- 装配日志与 JS `ctx.log` 写到 `io.Discard`,避免和对话输出混在一起。
- 一个进程一次只跑一个会话;多会话同进程仍由 library 层支持(见 workspace-plugin §8.1 的 ctx 传播)。

## 6. 测试

| 场景 | 用例 |
|---|---|
| 回放 preset 连续对话 + `/quit` | `tui.TestChatReplay` |
| `--workspace docs` + `/workspace docs` 切换 | `tui.TestWorkspaceAndSwitch` |
| 参数缺失 → 2 | `tui.TestUsage` |
| preset 无 workspace 服务 → 1 | `tui.TestWorkspaceMissing` |
| `--plain` 不输出 ANSI | `tui.TestChatReplay` |

## 7. 已知边界

1. **不是全屏 TUI**:没有 raw mode/alternate screen;长输出直接滚动。
2. **没有流式输出**:看不到 token 增量与工具调用过程;agent-loop 的 streaming 落地后再接。
3. **没有审批 UI**:`approval-gate` 的 CLI 交互仍由 `approval-cli` 负责,与 TUI 输入循环没有集成。
4. **工作区切换开新会话**:旧会话上下文不迁移;需要保留历史时用 session-jsonl + 后续 resume。
5. **预算例外**:TUI 271 行使 host 总行数 2638,超过原 2500;见 [ADR 0011](adr/0011-tui-host-budget.md),复议 2027-06-30。
6. **Windows VT**:依赖终端自身支持 ANSI;老式控制台请加 `--plain`。
7. **同一步的并行工具**:模型可能在一次响应里同时发 `write_file` 和 `read_file`;
   agent-loop 会并行执行,读可能先于写完成而报 `ENOENT`(文件最终存在)。
   这是并行工具的设计语义,不是 TUI 读错;有依赖的调用应分步,或把 `maxParallelTools` 设为 1。

## 7.1 正式端点验证(2026-11-21)

使用 `examples/workspace/real.preset.json`(OpenAI 兼容端点 + `ADA_LLM_API_KEY`),`ada-tui --plain`:

| 场景 | 结果 |
|---|---|
| `/workspace docs` + 两轮对话(记住数字 7 → 回答 7) | 同一会话保持上下文 |
| `/workspace docs` + `write_file` | `EDENIED [source=workspace] docs 只读` |
| `/workspace repo` + `.git/tui-blocked.txt` | `EDENIED [source=policy]` |
| `/workspace repo` + 分步 write → read | 读回 `tui-real-ok`,文件落在 repo 根 |
| 同一步 write + read | read 报 `ENOENT`,文件最终存在(见边界 7) |
| 默认 ANSI / `--plain` | 前者输出 ESC,后者无 ANSI |

## 8. 下一步

1. 用 `golang.org/x/term` 加 raw mode(需先提依赖 ADR),支持方向键/历史/Ctrl+C;
2. 接 agent-loop 的流式输出与工具事件,显示 tool call 进度;
3. 抽取 host/cli 与 host/tui 的公共装配 helper,把 host 预算回落到 2500 后撤销 ADR 0011。
