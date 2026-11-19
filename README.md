# ada_go_v2

**一个以插件为核心的程序基座**：Go 内核 + TypeScript 插件（纯 Go 编译与执行，无需 Node 环境）+ Cordis 式的作用域与编排。

> 当前处于 **M1**：内核骨架已落地（`kernel/` + demo + 门禁脚本），其余仍为设计。
> **工作约定/红线**：[AGENTS.md](AGENTS.md)（动代码或改设计前先读）
> 设计文档：[docs/plugin-kernel-design.md](docs/plugin-kernel-design.md)
> seam 契约：[docs/seam-api.md](docs/seam-api.md)（llm / tools / session / prompts）

## 这套方案要解决什么

ada_go v1 已经证明了「清单 + 每插件一个 moejs 运行时 + 能力桥 + 装配事务」是可行的，但它是**功能化**的：
装配顺序靠声明顺序、没有服务依赖、能力声明退化成词汇、钩子零使用、改一个插件要重建整代装配。

v2 把「插件」从**被调用的函数袋**升级成**可编排的作用域节点**：

| 抽象 | 解决 | 参照 |
|---|---|---|
| Context / Fiber | 作用域隔离、可嵌套派生、可等待的插件句柄 | Cordis `ctx` / `fiber` |
| Service | 插件间协作与依赖注入，依赖变化自动重载 | Cordis `provide/inject/epoch` |
| Effect | 一切注册可撤销，卸载逆序、依赖方先走、失败回滚 | Cordis `ctx.effect` |
| Event | 五种分发；`waterfall` 作为策略拦截的通用形态 | Cordis `emit/serial/bail/waterfall` |
| Config | JSON Schema 子集 + 分层 patch + 热更新 | Cordis Schema + DSH loader |
| **seam / provider / policy** | 平台能力三段式：接口、实现、策略各自可换 | DSH 的 `dsh-llm` / `dsh-fs-local` / `dsh-llm-retry` |
| **Agent Core 即插件** | agent、agent-loop、session、system-prompt、compaction 都是插件，由 preset 组合；产品差异不改代码 | `dsh-agent-loop`、`dsh-session`、`dsh-agent-preset` |
| 编排 | 装配（DAG + 事务）、交互（服务/事件/流水线）、调度（激活/配额/回收） | 本方案新增 |

## 核心决策（8 条）

1. **内核是 Go 的，且只有内核不是插件**：插件树、服务表、副作用栈、事件、配置在内核；平台能力（llm/tools/fs/shell/storage…）与 **agent core**（agent / agent-loop / session / system-prompt / compaction…）都是插件，seam + provider + policy 三段式；产品差异只由 **preset** 声明决定。
2. **esbuild 只做 TS → JS 降级**：`api.Transform` 逐文件、内存态、纯 Go、无 Node；补上 moejs `CompileTS` 不支持的语法（enum/namespace/参数属性/装饰器/TSX/CJS 形态）。
3. **moejs 执行，边界写死**：每插件一 Runtime 一 goroutine；`*Module` 进程内共享；字节码不可落盘；跨边界只走 JSON；环境能力默认全关。
4. **能力 ≠ 服务**：能力是授权（调用点强制 + 审计），服务是协作接口（`inject` 决定可见性）。
5. **发布物不带 esbuild**：编译发生在安装期/开发期，产物是 `plugin.artifact.json`（JS + sourcemap + 摘要 + 签名）。
6. **编排分三层**：装配编排产出依赖图与事务报告；交互编排产出服务调用、事件链与可声明流水线；调度编排产出激活事件、配额与闲置回收。
7. **mygo 是宿主之一**：内核不 import mygo；桌面适配器负责窗口、协议、类型化事件与主线程切换。
8. **迁移而非重写**：v1 的虚拟模块表、贡献事务、预算/熔断、十类沙箱、签名逻辑可搬；重写的是「一次性装配 + 冻结」模型。

## 文档结构

| 文档 | 内容 |
|---|---|
| [plugin-kernel-design.md](docs/plugin-kernel-design.md) §1–§2 | 背景、v1 复盘、Cordis 取舍、目标与非目标、8 条硬约束 |
| §3 | 分层、进程/线程模型、调用链路 |
| §4–§8 | Context/Fiber、Service、Effect、Event、Config（含 Go API 与语义表） |
| §9–§11 | 插件形态与清单 v2、esbuild 编译管线与 Artifact、moejs 运行时与桥 |
| §12 | **编排**：装配 / 交互（流水线）/ 调度 / 组合 patch / **Agent Core 的 preset 组合（§12.6）** |
| §13–§15 | 安全与信任、可观测与 CLI、mygo 集成、性能与体积（含实测数） |
| §16–§18 | 目录与骨架代码、里程碑（19–21 周）、风险 |
| 附录 A–E | Cordis→Go 对照、错误码、v1→v2 迁移映射、12 个 spike、待决问题 |
| [seam-api.md](docs/seam-api.md) | **首批 seam 契约**：llm / tools / session / prompts 的 Go 接口、事件、管线、不变量与契约测试 |
| [seam-api-security.md](docs/seam-api-security.md) | **安全 seam 契约**：sandbox / fs / shell 的三道闸、虚拟路径、装饰器强制链、拒绝矩阵 |
| [kernel-api.md](docs/kernel-api.md) | M1 冻结的内核 API 与实现边界 |
| [spikes.md](docs/spikes.md) | M0 验证报告（S1/S2/S6/S8/S9 通过） |

## 实测基线（Go 1.27.1 / windows/amd64 / CGO=0）

| 二进制 | 未 strip | strip |
|---|---|---|
| hello | 1.85 MB | 1.22 MB |
| + moejs | 12.32 MB | 8.63 MB |
| + esbuild | 11.65 MB | 8.33 MB |
| 两者同进程 | ≈ 22 MB | ≈ 16 MB |

`Transform` 单文件 0.77–0.88 ms；moejs 新建 Runtime 1.39 µs、加载插件 71 µs、单次调用 6.86 µs、每 Runtime 常驻 80.5 KiB。

## 当前状态:M1 已落地(2026-11-19 修复轮)

```powershell
go test -race -count=1 -timeout 120s ./...   # 内核 20 个用例 + schema 7 个用例
go run ./cmd/ada-demo                        # 依赖门控 + 级联重载演示
pwsh scripts/guard.ps1                       # 门禁:词汇/依赖/gofmt/build/vet/race/交叉编译/预算
```

已完成:`kernel/`(Context / Fiber / Service / Effect / Event / Config / Schema)、`kernel/schema`、`cmd/ada-demo`、`scripts/guard.ps1`、`docs/kernel-api.md`、`docs/spikes.md`、`docs/adr/0001`。
未开始:`compile/`(esbuild)、`jsrt/`(moejs)、`assemble/`、`plugins/seam/*`。

本轮修复了 M1 评审中复现的缺陷(waterfall 超时幽灵执行、兄弟插件被误拆、failed 不可复活、卸载错误被吞、fiber 注册表泄漏、Intercept 合并反向、Stop 后仍派发、schema 静默放行),每条都有回归用例;清单见 [kernel-api.md §6](docs/kernel-api.md)。

## 下一步

1. 评审 seam 契约的 13 个待决问题（`seam-api.md` 附录 B + `seam-api-security.md` §6）。
2. M2：`compile/` + `jsrt/` + 调用桥，把 Go 插件与 TS 插件放到同一个 `kernel.Plugin` 下（S3/S5/S7 三个 spike 随 M2 一起做）。
3. M4.5 按顺序冻结合同：`session` → `prompts` → `llm` → `tools`；安全侧 `sandbox` → `fs` → `shell`。


