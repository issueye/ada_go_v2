# AGENTS.md — ada_go_v2 的宪法与工作手册

> 给在本仓库工作的 AI 代理与人类协作者。**动代码之前先读这一份**；它与设计文档冲突时，以本文件的红线为准，并立刻提 issue 修正文档。

**当前阶段:M1 内核、M2 执行层、M3 装配、M4 编排(`sched`/`quota`/`flow`/`bundle`,内核回收 + 装配层 on-demand 延迟激活(含 onService/onCron 触发 helper)+ jobs 跨进程锁)、M4.5 seam、M5 安全(`fs`/`fs-local`/`fs-sandbox`、`shell`/`shell-local`/`shell-guard`、`sandbox`/`sandbox-none`、`fs-redact` 脱敏、`fs-observe`/`shell-observe` 观察事件、`shell-redact` 输出脱敏,且 fs/shell 策略已调用 sandbox.Resolve、可升级拒绝走 approval(含 CLI 交互审批)、caps 静态授权 + `caps-gate` 调用点强制(fs/shell)、install 校验/原子落盘/本机索引/CLI 安装同意、`host/cli`/`host/server` 宿主适配器(共享 `host/builtins` 内置插件)、`net.fetch`(seam/net + provider/net-http + net-guard 授权)已落地**,M5 剩余 在线 registry/desktop 宿主/OS 级沙箱。本文件始终生效:先用它约束设计,再用它约束实现。

---

## 0. 五条红线（先看这个）

1. **内核不许认识业务**。`kernel/` 里出现 `Turn`、`Message`、`ToolCall`、`Model`、`Session`、`Agent` 这些词，就是设计被腐蚀的开始。
2. **一切功能都是插件，除非它符合「不是插件」的四类之一**（§2.2）。新增功能先问「它该是哪个插件」，而不是「加在哪个函数里」。
3. **强制逻辑不许走事件**。安全边界只允许用装饰器/服务方法；事件是广播，不是门禁。
4. **产品差异只来自 preset + host 适配器**。任何 `if product == "coder"`、任何按产品名硬编码的插件列表，一律拒绝。
5. **没有契约测试与文档更新的能力，不算完成**。新增 seam/provider/policy/core 插件，必须同时给：契约、实现、一致性测试、文档。

> 违反任何一条都需要 ADR（§12），不允许「先合进去再说」。

---

## 1. 这个项目是什么

**一句话**：Go 内核 + 可替换的插件组合，跑一个以插件为核心的 agent 平台；TS 插件用 esbuild（Go API）降级为 JS、用 moejs 在进程内执行，全程不需要 Node。

**它不是什么**：

- 不是一个「agent 框架 + 一堆可选扩展」。agent 本身就是插件组合（`core/agent-loop`、`core/session`、`core/system-prompt`）。
- 不是「先做成单体，以后再拆」。拆分的成本在第一天最低；之后再拆就是重写。
- 不是 DSH 的克隆。我们借它的**作用域语义与 seam 划分**，但不复制它的包结构、不引入 Node。

**当前产物**（全部在 `docs/`）：

| 文档 | 作用 |
|---|---|
| [plugin-kernel-design.md](docs/plugin-kernel-design.md) | 主设计：内核、执行层、编排、安全、宿主、里程碑 |
| [seam-api.md](docs/seam-api.md) | 首批 seam 契约：llm / tools / session / prompts |
| [seam-api-security.md](docs/seam-api-security.md) | 安全 seam 契约：sandbox / fs / shell |
| [kernel-api.md](docs/kernel-api.md) | M1 冻结的内核 API 与已知边界 |
| [spikes.md](docs/spikes.md) | M0 验证报告（5 个已通过，7 个待做） |
| [m2-execution.md](docs/m2-execution.md) | M2 执行层第一刀:compile / jsrt / bridge 的契约、测试矩阵与已知边界 |
| [m3-assembly.md](docs/m3-assembly.md) | M3 装配层:清单、依赖图、preset、事务化装配与 `ada plan`/`up`/`inspect` |
| [seam-session.md](docs/seam-session.md) | seam/session 与 session-memory:契约、契约测试与已知边界 |
| [m4-orchestration.md](docs/m4-orchestration.md) | M4 编排:sched(时钟/cron/周期与定时任务)与 quota(配额与回压) |
| [m4-flow.md](docs/m4-flow.md) | M4 流水线:声明式 flow、受限表达式、checkpoint/resume |
| [m4-composition.md](docs/m4-composition.md) | M4 组合层:bundle / profile(整体替换与补丁) |
| [version.md](docs/version.md) | 版本与引擎约束:semver 解析与 engine/engines 校验 |
| [host-cli.md](docs/host-cli.md) | M5 宿主适配器第一刀:host/cli 命令实现与测试 |
| [host-server.md](docs/host-server.md) | 第二宿主适配器:host/server 只读 HTTP API |
| [seam-net.md](docs/seam-net.md) | M5 网络能力:seam/net 与 provider/net-http |
| [seam-fs.md](docs/seam-fs.md) | M5 安全第一刀:seam/fs、fs-local 实现与 fs-sandbox 策略 |
| [seam-shell.md](docs/seam-shell.md) | M5 安全第二刀:seam/shell、shell-local 实现与 shell-guard 策略 |
| [seam-sandbox.md](docs/seam-sandbox.md) | M5 安全第三刀:seam/sandbox 与 sandbox-none(policy-only) |
| [seam-approval.md](docs/seam-approval.md) | M5 安全第四刀:seam/approval、approval-deny 与 fs 升级审批 |
| [caps.md](docs/caps.md) | M5 能力授权:caps 语法、信任等级与 Broker |
| [install.md](docs/install.md) | M5 安装与签名:清单/摘要/Ed25519/原子安装 |

---

## 2. 设计理念（10 条，每条都有可检验的判据）

### 2.1 十条原则

| # | 原则 | 为什么 | 判据（怎么检查） |
|---|---|---|---|
| P1 | **内核无知** | 内核是被所有产品共享的机制；一旦认识业务，产品就开始分叉 | `kernel/` 无 agent 词汇（CI grep）；删掉任何插件内核仍能装配 |
| P2 | **一切皆是插件** | 可替换、可禁用、可组合，才不会长成巨石 | 新功能能回答 §3 的插件准入测试 |
| P3 | **三段式 seam / provider / policy** | 接口、实现、策略的生命周期不同，混在一起就无法独立替换 | 接口包里没有实现；策略以装饰器注册，不写进 provider |
| P4 | **组合即产品** | 产品差异应当是数据差异，不是代码差异 | 换 preset 即换产品；没有 `if product` 分支 |
| P5 | **作用域即生命周期** | 注册必须有主，卸载必须干净 | 所有注册 API 返回 `Dispose`；fiber 卸载后无残留 |
| P6 | **依赖永不静默** | 「服务不在就先跳过」会让功能悄悄失效 | 依赖用 `inject` 声明；缺失 → `pending`，不是 nil 判断 |
| P7 | **广播用事件，门禁用装饰器** | 事件顺序不可靠、监听器可 panic；安全边界必须确定 | 安全/审批/沙箱只出现在装饰器或服务方法里 |
| P8 | **数据面留在 Go** | 跨语言边界只该承载控制面 | 大对象走句柄/URL；JSON 只用于调用与结果 |
| P9 | **可观测是功能的一部分** | 没有 inspect 的能力等于没有交付 | 新能力必须能在 `ada inspect` 里看到 fiber/服务/effect/装饰器链 |
| P10 | **契约先行** | 先写文档再写代码，才能并行、才能评审 | seam 变更先改 `docs/seam-api*.md`，再改实现 |

### 2.2 「不是插件」的四类（白名单，不得扩张）

只有这四类可以不是插件，新增第五类需要 ADR：

| 类别 | 内容 | 判据 |
|---|---|---|
| **内核** | Context / Fiber / Service / Effect / Event / Config / Registry / Provider 接口 | 只懂「作用域、服务、副作用、事件、配置」，不懂业务 |
| **基础设施** | assemble / compile(esbuild) / jsrt(moejs) / plugin(清单/安装/签名) / caps(授权) / observe | 是「装载与执行机制」，没有领域语义 |
| **组合数据** | preset / bundle / patch | 是数据不是代码 |
| **宿主适配器** | `host/desktop`（mygo）、`host/cli`、`host/server` | 只做平台映射（窗口、协议、主线程、进程入口），不做业务 |

其余一切——平台能力（llm/tools/fs/shell/sandbox/storage/settings/secrets/jobs/schedule/telemetry/ui）、agent core（agent/agent-loop/session/system-prompt/context/compaction/approval/subagent/goal/plan/team/workflow）、工具、UI 贡献——**都是插件**。

---

## 3. 新代码放哪：决策树

按顺序问，第一个「是」就是答案：

1. 它只影响「装哪些插件、装的顺序约束、参数」吗？→ **preset / bundle patch**（数据）
2. 它定义「agent 是什么、怎么跑一轮」吗？→ **`plugins/core/*`**（插件）
3. 它定义「某个宿主资源的接口」吗？→ **`plugins/seam/*`**（接口 + 错误码 + 事件名，**无实现**）
4. 它实现一个 seam 吗？→ **`plugins/provider/*`**
5. 它约束一次调用、必须**确定性地阻断**吗？→ **`plugins/policy/*`（装饰器）**
6. 它只观察、改写、通知、埋点、刷新 UI 吗？→ **事件监听**（仍写在某个插件里；改写用 waterfall，阻断用 guard/装饰器）
7. 它是「装载、编译、执行、授权、日志」这类所有产品都要的机制吗？→ **L2 基础设施**（不是插件）
8. 它是「作用域、服务、副作用、事件、配置」的机制吗？→ **L1 内核**（且必须无业务词汇）
9. 它是某个平台的窗口/菜单/协议/主线程吗？→ **`host/*` 适配器**
10. 以上都不是 → 停下来，说明你还没想清楚边界；写一页设计再动手。

> 经验法则：**如果一个功能需要「知道别的插件在干什么」，它就应该是服务或事件；如果它需要「阻止别人干什么」，它就是装饰器或 guard。**

### 3.1 插件准入测试（任何新功能必须五问全过）

1. **删掉它，另一个产品还能跑吗？** 不能 → 说明它属于内核或基础设施。
2. **它只做一件事吗？** 一个 seam、或一个 provider、或一个策略、或一组同类工具。混装 → 拆。
3. **它的对外面只有服务、事件、配置吗？** 还有「直接 import 别的插件」→ 不合格。
4. **它能被另一个实现替换吗？** 不能 → 说明它把接口和实现焊死了。
5. **它有契约测试吗？** 没有 → 不算完成。

---

## 4. 「逻辑堆回去」的 14 个入口，和对应的闸门

这一节是给未来的自己看的：**功能不是一次堆进去的，是一次一次「就加一行」加进去的**。每条都给识别信号与正确做法。

| # | 腐蚀形态（信号） | 为什么危险 | 闸门（正确做法） |
|---|---|---|---|
| 1 | kernel 里出现 `if cfg.EnableX`、`if plugin.ID == "cron"` | 内核开始认识产品 | 能力做成插件；配置进 preset |
| 2 | kernel 里加 `Session`/`Message` 类型 | 内核开始认识 agent | 放进 `plugins/core/session`；内核只留 `Event`/`Service` |
| 3 | `host/desktop` 里做会话管理、模型调用、工具编排 | 宿主变成第二个内核，headless 无法复用 | 业务进 core 插件；host 只做窗口/协议/主线程映射 |
| 4 | seam 包里写了实现（`func (s *seam) Read(...)`） | 接口与实现焊死，无法替换 | seam 只放类型/错误/事件名；实现进 `provider/*` |
| 5 | provider 里加重试、审批、截断、脱敏 | 策略无法被单独禁用 | 抽成 `policy/*`；provider 只做「一次调用」 |
| 6 | policy 绕过装饰器，直接 `os.Open` / `cmd.Run` | 出现第二条不受控路径 | policy 必须在装饰器链内，禁止直接 syscall |
| 7 | 插件 A `import` 插件 B 的 Go 包 | 装配顺序失效、无法隔离 | 经服务（`ctx.Service`）或事件；只 import `seam/*` 类型 |
| 8 | 全局可变单例（`var registry = ...`） | 多实例/测试/重载互相污染 | 状态挂在 kernel `App` 或 fiber scope 上 |
| 9 | preset 之外硬编码插件列表/顺序 | 产品分叉从代码开始 | 一律走 preset/patch；顺序由 `inject` 推导 |
| 10 | 用事件做「拒绝」（waterfall 不调 `next`） | 顺序依赖、可 panic、不可审计 | 用 `Guard`（同步拒绝）或装饰器 |
| 11 | 「临时」分支、TODO 后门、常驻 feature flag | 临时的最持久 | 要么进 preset 配置，要么删；ADR 记录例外 |
| 12 | UI 布局/文案/主题写进 Go，或插件自绘 UI | 破坏宿主统一与可替换 | 插件产数据；宿主渲染；文案走 i18n |
| 13 | secret/token 进 config 或日志 | 泄露面扩大 | `seam/secrets` 按名取；日志正则脱敏 |
| 14 | 模型可见状态存在 session 之外（内存 map/插件自存） | fork/resume/replay 失效 | 一切模型可见事实写 session 事件；派生状态用投影 |

**如何发现**（评审时问）：

- 「这段代码被删除后，哪个产品会坏？」如果答不出，通常是死代码或应该下沉。
- 「这个行为在两个产品里不同吗？」如果会不同，就不该在共享层。
- 「它需要知道谁在监听吗？」需要 → 应该是服务，不是事件。
- 「它能被第二个实现替换吗？」不能 → 接口与实现焊死了。

---

## 5. 硬规则（CI 与评审共同守护）

> M1 起接入 CI；现在先作为设计约束。命令是目标形态，路径以实际实现为准。

### 5.1 依赖禁令

```
kernel/        ✗→ jsrt, compile, assemble, flow, sched, host/*, plugins/*
               ✗→ 任何 agent 词汇（Turn/Message/ToolCall/Model/Session/Agent）
seam/*         ✗→ provider/*, policy/*, host/*     （只允许 kernel + 标准库）
provider/*     ✗→ policy/*, host/*                 （只允许 seam + kernel）
policy/*       ✓→ seam/*，✗→ provider 内部实现
plugins/*      ✗→ host/*
host/*         ✓→ 全部
```

### 5.2 必跑的门禁

| 门禁 | 检查 |
|---|---|
| `go build ./... && go vet ./...` | 基本正确性 |
| `go test -race ./...` | 并发安全 |
| `staticcheck ./...` | 静态问题 |
| `grep -rn "Turn\|Message\|ToolCall\|Model\|Session" kernel/` | 必须无输出 |
| `go list -deps ./kernel` | 不含 jsrt/compile/host |
| `grep -rn "os/exec\|net/http\|os.Open" kernel/` | 必须无输出 |
| `ada check ./plugins/...` | 清单 + 编译 + 链接 + 能力 |
| `ada test ./plugins/...` | 契约测试与 golden |
| `ada inspect --chain` 快照 | 装饰器链未意外变化 |
| `GOOS=linux/darwin/windows go build` | 交叉编译（CGO_ENABLED=0） |
| `pwsh scripts/guard.ps1` | 一键门禁:红词汇/依赖/gofmt/build/vet/staticcheck/race/交叉编译/行数预算(CI 三平台跑) |
| `pwsh scripts/bench.ps1` | 性能预算:单次调用 ≤ 20 μs、50 插件冷启动 ≤ 150 ms |

> CI:[.github/workflows/ci.yml](.github/workflows/ci.yml),`guard` 在 linux/windows/macos 三平台运行,`staticcheck` 与 `bench` 各自独立。

### 5.3 代码预算（超了要 ADR）

| 范围 | 预算 | 理由 |
|---|---|---|
| `kernel/` | ≤ 3000 行 | 内核越厚，插件越难替换 |
| `host/*` | ≤ 1500 行（每个适配器 ≤ 800） | 宿主只做映射 |
| 单个插件 | ≤ 800 行，单一职责 | 混装说明该拆 |
| 单个 seam 包 | ≤ 400 行（只有类型/错误/事件名） | 出现实现即违规 |

### 5.4 命名与结构约定

- 目录：`plugins/seam/<name>`、`plugins/provider/<name>`、`plugins/policy/<name>`、`plugins/core/<name>`、`plugins/tool/<name>`。
- 服务名：小写单数（`llm`、`tools`、`session`、`prompts`）。
- 事件名：`域/动词`，全小写，如 `turn/before`、`tools/pre-execute`、`fs/observe`；**新增事件必须写进对应契约文档**。
  - 两个命名空间不得混用：**总线事件**用服务复数（`tools/*`、`llm/*`），**会话事件类型**用单数（`tool/result`、`assistant/message`），见 `seam-api.md` §1.7。
- 插件 id：kebab-case，与目录同名。
- 错误：一律带错误码（见 seam 契约 §1.2），禁止裸 `errors.New` 跨边界。

### 5.5 注释与文案：一律中文

- **所有代码注释使用中文**：行内注释、块注释、Go 的 doc comment、TS 的 JSDoc、测试里的说明，全部中文。
- **标识符保持英文**：包名、类型名、函数名、变量名、文件名、目录名、错误码、事件名、JSON 字段名。
- **面向用户与开发者的文案用中文**：错误信息、日志、`ada` CLI 输出、文档。
- 注释写「为什么」与「约束」，不写「做了什么」；禁止把注释写成代码的翻译。
- 注释里出现「临时」「先这样」「以后再改」时，必须同时给 issue 编号与复议日期，否则评审拒绝。

### 5.6 禁止清单（绝对）

- 禁止 `panic` 跨插件/跨边界；禁止 `init()` 里做 I/O 或注册全局状态。
- 禁止 `context.Background()` 出现在插件实现里（必须用传入 ctx）。
- 禁止 sleep 轮询；用 fake clock 或事件。
- 禁止把 `[]byte` 大对象（> 256 KiB）塞进 JSON 边界。
- 禁止在 JS 插件里 `require`/`module.exports`/`.node`；禁止运行时 `eval`。
- 禁止把 secret 写进 config、日志、事件、工具结果。

---

## 6. 工程约定

### 6.1 Go

- 接口定义在**消费方 seam 包**；provider 实现它。
- 每个注册 API 返回 `kernel.Dispose`，且必须在 `ctx.Effect` 内注册（否则重载后会重复）。
- 所有导出 API 必须有 doc comment，说明「它属于哪一层、谁能调用」。
- 错误用 `%w` 包装，保留错误码；审计字段（plugin/cap/bytes）在边界层填一次。
- 测试用 `t.Cleanup` 回收 fiber；禁止依赖真实时间与真实网络。

### 6.2 TypeScript 插件

- ESM only；`apply(ctx, config)` 必须可重入、无模块级状态；状态进 `ctx.store`。
- 不 import Node 内置；只允许 `ada:*` 或 Artifact 内联模块。
- 不在高频回调里做重活（`llm.stream` 的 onDelta 应只做转发/累加小量）。
- 类型来自 `@ada/sdk`，不手写重复声明。

### 6.3 测试

| 类型 | 要求 |
|---|---|
| 契约测试 | 每个 seam 有 `seamtest`，provider 必须全绿 |
| Golden | Plan/Report、事件序列（`seam-api.md` 附录 C 的 C6 golden）、编译产物 |
| 拒绝矩阵 | 能力 × 目标 × 沙箱模式，全部拒绝且落审计 |
| 性能 | 调用开销 ≤ 20 µs、50 插件冷启动 ≤ 150 ms；回归 ±15% 报警 |
| 并发 | `-race` 全绿；重载 100 次内存回落 ±5% |

---

## 7. 文档即契约

| 改动 | 必须先更新 |
|---|---|
| 新增/修改 seam 方法、事件、错误码 | `docs/seam-api*.md` 对应章节 |
| 新增 kernel 导出 API | `docs/plugin-kernel-design.md` §4–§8 |
| 新增 preset 字段或分层规则 | 主设计 §12.4/§12.6 |
| 新增能力/权限点 | 主设计 §13 + 安全 seam 契约 |
| 改里程碑或验收口径 | 主设计 §17 |

规则：**先改文档，再改代码**；PR 里文档 diff 与代码 diff 同一个提交。文档里的示例代码必须在 CI 里能编译（M3 起）。

---

## 8. Agent 工作流（一次改动的标准步骤）

1. **读**：本文件 → 相关契约 → 目标包现有实现（≤ 10 分钟，别跳）。
2. **定位**：用 §3 决策树决定归属；写下「这属于哪一层、为什么」。
3. **计划**：列 3–7 步；涉及契约变化先提计划再动手。
4. **最小切片**：先让一个路径端到端跑通（例如一个 seam 方法 + 一个 provider + 一个测试），再铺开。
5. **验证**：§5.2 门禁全跑；契约测试必须新增/更新。
6. **文档**：按 §7 更新；有例外写 ADR。
7. **自审**：用 §9 的问题清单过一遍；答不上来就回退。
8. **提交**：一个提交一件事；信息写「为什么」而不是「改了什么」。

### 提交信息模板

```
<scope>: <一句话为什么>

- 归属：<层/插件>，因为 <判据>
- 契约变化：<无 / 文档路径>
- 验证：<命令与结果>
- 例外：<无 / ADR 编号>
```

---

## 9. 评审清单（12 问）

1. 这段代码属于哪一层？判据是什么？（§3）
2. 删掉它，另一个产品还能跑吗？
3. 它只做一件事吗？有没有混装 seam 与 policy？
4. 有没有新增 agent 词汇到 kernel？（grep 过了吗）
5. 注册都返回 `Dispose` 且在 effect 里吗？重载 100 次会漏吗？
6. 安全路径只有装饰器一条吗？有没有第二个 `os.Open`/`exec`？
7. 事件被当成门禁了吗？
8. 依赖用 `inject` 表达了吗？缺失时是 `pending` 而不是 nil 判断？
9. 产品差异是否只来自 preset / host？有没有 `if product`？
10. 契约测试与 golden 加了吗？拒绝矩阵覆盖了吗？
11. 文档与示例同步了吗？示例能在 CI 编译吗？
12. 有没有「临时」分支、TODO 后门、常驻 flag？

---

## 10. 方向与路线图（每阶段都带「不许做」）

| 阶段 | 要做的 | **本阶段不许做** |
|---|---|---|
| M0 | 12 个 spike（S1/S2/S6/S8/S9 已预验，gate 已关闭） | 不许写实现代码；不许定死 API |
| M1 内核 ✅ | Context/Fiber/Service/Effect/Event/Config（已落地，门禁通过） | 不许加 seam；不许认识 agent 词汇 |
| M2 执行 | compile(esbuild) + jsrt(moejs) + bridge | 不许在 kernel 里加 JS 相关类型；不许在 provider 里做业务 |
| M3 装配 | Plan/Graph/Assembler/Report/Profile | 不许硬编码插件列表；不许按产品分支 |
| M4 编排 | flow / sched / 配额 / cron | 不许把编排写进 kernel；流线必须是数据 |
| M4.5 Agent Core | session → prompts → llm → tools → loop → preset | 不许把循环逻辑放进 host；不许跳过契约测试 |
| M5 安全与宿主 | sandbox/fs/shell、签名、CLI、mygo | 不许用事件做拒绝；不许把审批写进 tools |
| M6 迁移生态 | 移植 v1 插件、SDK、示例 | 不许把 v1 的历史特例带进来（见 §11） |

长期方向（写下来防止跑偏）：

1. **产品 = preset**。新增产品只允许新增 preset + 可能的 host 适配器。
2. **能力 = seam**。新增宿主能力先写 seam 契约，再写 provider，再写 policy。
3. **内核只减不增**。除非某个机制被三个以上插件需要，否则不进 kernel。
4. **JS 是选项不是默认**。能 Go 实现的插件就 Go 实现；JS 用于第三方与快速迭代。
5. **单二进制、无 Node、可禁用一切**。任何「没有它产品起不来」的插件都是设计错误。

---

## 11. 与 ada_go v1 的关系（四处有意反转，别按旧文档改回去）

v1 的 `AGENTS.md` 记录了 2026-11-10 的「能力位不决定装什么」与「审批策略已移除」。v2 **有意反转**，理由见主设计 §13；不要把 v1 的结论带回来：

| 事项 | v1 现状 | v2 决定 | 理由 |
|---|---|---|---|
| 能力位 | 只做清单校验，不拦调用 | **恢复为授权**（capability broker 强制） | 安全边界不能只是词汇 |
| 审批 | 已移除（`tool.beforeCall` 删除） | **恢复为插件**（`policy/approval-gate`，fail-closed） | 高风险工具必须有闸门，且策略应可换 |
| TS 编译 | Node 的类型擦除（`build.mjs`） | **esbuild Go API 降级** | 无 Node 环境，且能补 moejs 的 TS 运行时短板 |
| 插件顺序 | 声明顺序 + id 排序 | **依赖图拓扑序** | 顺序由依赖决定，不由配置决定 |

> 读 v1 文档时记住：`docs/05-plugins.md`、`docs/06-plugin-runtime.md` 的部分口径已过期。

---

## 12. 例外流程：ADR

需要 ADR 的情形：

1. 新增第五类「不是插件」的东西；
2. 修改 seam 的公开方法/事件/错误码（破坏性）；
3. 在 kernel 或 host 里加入任何业务判断；
4. 突破代码预算（§5.3）；
5. 引入新的第三方依赖（尤其是会改变二进制体积/可移植性的）。

格式：`docs/adr/NNNN-<slug>.md`，必须含：背景、决策、**至少两个替代方案**、影响面、回滚方式、复议日期。没有 ADR 的例外一律视为未决，不得合并。

---

## 13. FAQ：常见诱惑与标准答复

| 诱惑 | 答复 |
|---|---|
| 「我就想在 kernel 里加一行特判」 | 不行。特判说明它该是插件或配置；加到 kernel = 所有产品共享这个特判 |
| 「这个插件比较特殊，先直接 import 它的包」 | 不行。用服务，或让它在启动时 `provide`；直接 import 会让装配顺序失效 |
| 「先写死，以后再抽」 | 可以写死，但必须留在同一层、留 issue + 复议日期；跨越 seam 边界的「以后」不存在 |
| 「UI 文案放 Go 里方便」 | 不行。文案走 i18n，布局走宿主渲染 |
| 「事件也能拒绝，为什么还要装饰器」 | 事件顺序不可靠、可 panic、不可审计；门禁必须确定 |
| 「这个工具需要一点模型知识」 | 那属于 `provider/llm-*` 或 `core/agent-loop`，不属于 `tool/*` |
| 「为了性能，把循环放到 host」 | 不行。循环是 `core/agent-loop`；性能问题用 Go 插件实现，不是搬到 host |
| 「先不写契约测试，功能先上」 | 不行。没有契约测试的 seam/provider 会在第一次升级时炸 |
| 「这个 preset 字段先不加，代码里判断一下」 | 不行。preset 是数据契约；加字段，别加判断 |

---

## 14. 维护

- 本文件与三份设计文档同步维护；任何红线变化必须同时改这里与对应章节。
- 每完成一个里程碑，回来更新 §10 的「不许做」清单（把已完成的移走，把新的诱惑加进去）。
- 如果发现本文件与现实脱节，**先修文档再改代码**。


