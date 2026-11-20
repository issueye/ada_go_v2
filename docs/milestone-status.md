# M4.5 / M5 验收矩阵

> 本文件把里程碑验收项映射到**实现位置、证据命令与已知边界**。口径来源:
> [plugin-kernel-design.md](plugin-kernel-design.md) §17、AGENTS §10。
> 最近更新:2026-11-20(437 个 Test + 4 个 Benchmark;不做 v1 插件迁移)。

## 0. 结论速览

| 里程碑 | 状态 | 说明 |
|---|---|---|
| M4 编排 | ✅ 完成(文档口径) | platform/sched/platform/quota/platform/flow/platform/bundle/回收/延迟激活/jobs 锁 |
| M4.5 Agent Core | ⚠️ 第一刀完成 | prompts/llm/tools/agent-loop + 最小 preset + memory/jsonl session + turn-stats 投影 + compaction-basic;真实端点与策略已落地,模型摘要可选/并行工具 |
| M5 安全与宿主 | ⚠️ 部分完成 | 安全 seam/caps/install/宿主/C13/inspect chain 已落地;仅剩 **OS 级沙箱强制** 与 **真实桌面窗口** |

## 1. M4.5 验收矩阵

| 任务 | 实现位置 | 证据 | 已知边界 |
|---|---|---|---|
| C1 session seam + memory/jsonl | `plugins/seam/session`、`plugins/provider/session-memory`/`session-jsonl`、`plugins/core/session-stats` | `session` 单测、`sessiontest`;jsonl 契约/重开/并发/半行恢复;`sessionstats` 投影 | 投影检查点未持久化;jsonl 无多进程租约 |
| C2 prompts seam + system-prompt | `plugins/seam/prompts`、`plugins/core/system-prompt` | `prompts` 3 个用例 | 引擎在 seam 内,未拆 provider;预算为字符估算 |
| C3 llm seam + provider | `plugins/seam/llm`、`plugins/provider/llm-replay`/`llm-openai` | `replay` 2 个用例、`openai` wire/真实端点(可选) | OpenAI 兼容端点已实测;stream 改写结果未回传;无 deepseek 专用适配 |
| C4 tools seam + tool/fs | `plugins/seam/tools`、`plugins/tool/fs` | `tools` 2 个用例 | `tools/execute` 不能包裹 handler;无 approval-gate |
| C5 agent-loop | `plugins/core/agent-loop` | `agentloop.TestTurnWithReplayAndTool`、`TestContextBudgetReturnsECONTEXT`、`TestCompactionFitsBudget`、`TestParallelTools` | 并行工具(maxParallelTools);contextBudget + compaction-basic;模型摘要可选;无 core/agent 拆分 |
| C6 最小 preset + golden | `examples/agent` | `ada plan/up --preset examples/agent/agent.preset.json`;事件序列 golden;`TestRealAgentTurn` 实测真实端点 tool call | 默认回放模型;真实端点需 env |
| C7 策略桩 | `plugins/policy/approval-gate`、`llm-retry`、`tool-timeout` | approval-gate 2、llm-retry 3、tool-timeout 1 | 全部落地;后续可加 tool-repeat-guard 等 |

## 2. M5 验收矩阵

### 2.1 沙箱与文件/命令安全

| 验收项 | 实现位置 | 证据 | 已知边界 |
|---|---|---|---|
| sandbox 判定 seam | `plugins/seam/sandbox` | `sandbox`(5) | 只做判定,不强制 |
| policy-only 后端 | `plugins/provider/sandbox-none` | `sandbox` 用例 | `Enforced=false`(**OS 级强制见 ADR 0001**) |
| fs seam/provider | `seam/fs`,`provider/fs-local` | `fs`(7) | TOCTOU 已知;父目录符号链接写逃逸已修(`TestWriteThroughSymlinkParent`) |
| fs 沙箱策略 | `policy/fs-sandbox` | `fs-sandbox`(5) | 可升级走 approval |
| fs 脱敏/观察 | `policy/fs-redact`,`fs-observe` | 各 5 | 观察不含内容 |
| shell seam/provider | `seam/shell`,`provider/shell-local` | `shell-local`(11) | 超时杀进程组(POSIX)/Windows Job + WaitDelay |
| shell 策略(platform/caps/审批) | `policy/shell-guard` | `shell-guard`(8) | `allowShell` 仍是配置开关 |
| shell 脱敏/观察 | `policy/shell-redact`,`shell-observe` | 各 5 | 分块边界可能漏脱敏 |

### 2.2 审批与能力授权

| 验收项 | 实现位置 | 证据 | 已知边界 |
|---|---|---|---|
| approval seam | `plugins/seam/approval` | `approval`(7) | fail-closed,无审批人即拒绝 |
| 审批实现 | `approval-deny`,`approval-cli` | `approval-cli`(5) | CLI y/yes 交互 |
| 静态能力授权 | `plugins/provider/caps-static`,`platform/caps/` | `caps`(6)、`caps-static`(1) | trust 来源仍取自清单自述(已知缺口) |
| 调用点强制 | `policy/caps-gate` | `caps-gate`(4) | fs/shell 调用点 |
| 网络升级审批 | `policy/net-guard` | `net-guard`(13) | 每次未授权都重新审批 |
| C13 拒绝矩阵 | `plugins/policy/securitymatrix` | `TestFSMatrix`、`TestFSNoCapDenied`、`TestNetMatrix`、`TestShellMatrix` | 覆盖 fs/net/shell;caps 拒绝审计仍待补 |

### 2.3 网络与 secret

| 验收项 | 实现位置 | 证据 | 已知边界 |
|---|---|---|---|
| net seam/provider | `seam/net`,`provider/net-http` | `net-http`(7) | 超时/8MiB/重定向≤5/http(s) |
| net 授权/私网拒绝 | `policy/net-guard` | `net-guard`(13) | 重定向逐跳校验;校验与请求解析仍有 TOCTOU |
| net 观察 | `policy/net-observe` | `net-observe`(4) | error 已脱敏 query;不含 body |
| secrets seam/provider | `seam/secrets`,`provider/secrets-env` | `secrets-env`(5) | 白名单空=拒绝;`ADA_SECRET_*` |
| secrets 授权 | `policy/secrets-guard` | `secrets-guard`(3) | 名称正则 + principal |

### 2.4 安装、审计与宿主

| 验收项 | 实现位置 | 证据 | 已知边界 |
|---|---|---|---|
| 安装校验/摘要/签名 | `platform/install/` | `install`(31) | Ed25519;签名文件可选 |
| 原子安装/索引/回滚 | `platform/install/` | `install` 用例 | 锁 TTL 10 分钟,TTL 误回收可能 |
| 在线索引/artifact | `platform/install/fetch.go`,`artifact.go` | `install` 用例 | zip 16MiB/1000 文件;artifact 不单独签名 |
| 在线仓库服务端 | `platform/host/registry` | `registry`(7) | 只读;无上传/发布 |
| CLI | `platform/host/cli` | `platform/host/cli`(30) | 参数手写解析;DevX 预算例外 |
| 只读 HTTP API | `platform/host/server` | `platform/host/server`(14) | Token/限流/TLS/CORS 可选 |
| 桌面映射骨架 | `platform/host/desktop` | `desktop`(4) | **无真实窗口(mygo),见 ADR 0001** |
| 审计落盘 | `policy/audit-log` | `audit-log`(6) | 无 fsync;大小轮转 |
| `ada inspect --chain` | `platform/host/cli` | `cli.TestInspectChain` | 只显示 fs/shell/net 优先级 |

## 3. 门禁与证据

```powershell
pwsh scripts/guard.ps1     # 词汇/依赖/gofmt/build/vet/staticcheck/race/交叉编译/kernel+host 预算
pwsh scripts/bench.ps1     # 单次调用 ≤ 20 μs、50 插件冷启动 ≤ 150 ms
go test -race -count=1 ./...
go run ./cmd/ada plan --preset examples/agent/agent.preset.json
go run ./cmd/ada inspect --chain --preset examples/agent/agent.preset.json
```

当前实测:

- 测试 **437 个 Test + 4 个 Benchmark**,`go test -race ./...` 全绿;
- kernel 生产 **2396 / 3000** 行;
- host 生产 **2287 / 2500**(cli 1405 / 1500,其它最大 404 / 800);
- bench:ServiceResolve 56ns、Emit 1.59μs、waterfall 5.26μs、50 插件 331μs;
- 本机 `staticcheck 2025.1.1` 与 Go 1.27 export data 不兼容,guard 会在该环境报工具错误(CI 用 `@latest`)。

## 4. 明确未完成项

1. **OS 级沙箱强制**(landlock/seatbelt/Job Object 级 fs/shell):当前只有 policy-only;
2. **真实桌面窗口(mygo)**:只有 Renderer/View/Subscribe/OpenPage 骨架;
3. **M4.5 基础已落地**;剩余摘要质量评估、检查点持久化与 `core/agent` 拆分;
4. **安全 P0 缺口**:安装 trust 未灌入 caps、审批/审计通用脱敏、连接级 IP 校验。

前两项记录在 [ADR 0001](adr/0001-deferred-m5-scope.md),复议日期 2027-01-15;在此之前 **不得宣称 M5 完成**。
