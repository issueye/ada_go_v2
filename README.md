# ada_go_v2

**一个以插件为核心的程序基座**：Go 内核 + TypeScript 插件（纯 Go 编译与执行，无需 Node 环境）+ Cordis 式的作用域与编排。

> 当前处于 **M3 装配 + M4 编排(`sched`/`quota`/`flow`)+ M4.5 seam(`session`)+ M5 安全(`fs`/`shell`/`sandbox`/`approval`,三闸串联 + 默认拒绝审批)**:M1/M2/M3 已落地;M4 与 M5 仍在推进,M5 剩余 caps/签名/宿主。
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
| [m2-execution.md](docs/m2-execution.md) | M2 执行层第一刀:compile / jsrt / bridge 的契约、测试矩阵与已知边界 |
| [m3-assembly.md](docs/m3-assembly.md) | M3 装配层:清单、依赖图、preset、事务化装配与 `ada plan`/`up` |
| [m4-orchestration.md](docs/m4-orchestration.md) | M4 编排:sched 与 quota |
| [m4-flow.md](docs/m4-flow.md) | M4 流水线:声明式 flow、受限表达式、checkpoint/resume |
| [m4-composition.md](docs/m4-composition.md) | M4 组合层:bundle / profile(整体替换与补丁) |
| [version.md](docs/version.md) | 版本与引擎约束:semver 解析与 engine/engines 校验 |
| [host-cli.md](docs/host-cli.md) | M5 宿主适配器第一刀:host/cli 命令实现与测试 |
| [host-server.md](docs/host-server.md) | 第二宿主适配器:host/server 只读 HTTP API |
| [host-desktop.md](docs/host-desktop.md) | 宿主适配器骨架:host/desktop 视图映射 |
| [host-registry.md](docs/host-registry.md) | 在线仓库服务端:host/registry 只读 HTTP |
| [audit-log.md](docs/audit-log.md) | M5 审计落盘:policy/audit-log JSONL |
| [seam-net.md](docs/seam-net.md) | M5 网络能力:seam/net 与 provider/net-http |
| [seam-secrets.md](docs/seam-secrets.md) | M5 secret 能力:seam/secrets 与 provider/secrets-env |
| [seam-session.md](docs/seam-session.md) | seam/session 与 session-memory |
| [seam-fs.md](docs/seam-fs.md) | M5:seam/fs、fs-local 与 fs-sandbox |
| [seam-shell.md](docs/seam-shell.md) | M5:seam/shell、shell-local 与 shell-guard |
| [seam-sandbox.md](docs/seam-sandbox.md) | M5:seam/sandbox 与 sandbox-none |
| [seam-approval.md](docs/seam-approval.md) | M5:seam/approval、approval-deny 与升级审批 |
| [caps.md](docs/caps.md) | M5 能力授权:caps 语法、信任等级与 Broker |
| [install.md](docs/install.md) | M5 安装与签名:清单/摘要/Ed25519/原子安装 |
| [spikes.md](docs/spikes.md) | M0 验证报告（S1/S2/S6/S8/S9 通过） |

## 实测基线（Go 1.27.1 / windows/amd64 / CGO=0）

| 二进制 | 未 strip | strip |
|---|---|---|
| hello | 1.85 MB | 1.22 MB |
| + moejs | 12.32 MB | 8.63 MB |
| + esbuild | 11.65 MB | 8.33 MB |
| 两者同进程 | ≈ 22 MB | ≈ 16 MB |

`Transform` 单文件 0.77–0.88 ms；moejs 新建 Runtime 1.39 µs、加载插件 71 µs、单次调用 6.86 µs、每 Runtime 常驻 80.5 KiB。

## 当前状态:M1 内核 + M2 执行层 + M3 装配 + M4 编排 + M4.5 seam + M5 安全(fs/shell/sandbox/approval,三闸串联)

```powershell
go test -race -count=1 -timeout 300s ./...   # 367 个用例:kernel 29 + schema 7 + compile 6 + jsrt 15 + bridge 3 + plugin 7 + preset 5 + assemble 21 + loader 5 + session 3 + sched 27 + quota 11 + flow 26 + expr 2 + bundle 12 + version 3 + caps 6 + install 31 + fs 12 + shell 17 + sandbox 5 + approval 10 + approval-cli 5 + caps-gate 4 + fs-redact 5 + fs-observe 5 + shell-observe 5 + shell-redact 5 + net-http 6 + net-guard 7 + net-observe 3 + secrets-env 5 + secrets-guard 3 + audit-log 3 + caps-static 1 + host/builtins 3 + host/cli 29 + host/server 10 + host/desktop 2 + host/registry 3
go run ./cmd/ada-demo                        # 依赖门控 + 级联重载演示
pwsh scripts/guard.ps1                       # 门禁:词汇/依赖/gofmt/build/vet/staticcheck/race/交叉编译/预算
go run ./cmd/ada-ts-demo                     # M2:TS 插件编译→加载→提供服务/派发事件→内核消费
go run ./cmd/ada plan examples/plugins       # M3:读清单→依赖图→装配顺序/问题报告
go run ./cmd/ada up examples/plugins-ts      # M3:现场编译 TS 插件并真的挂载一遍
go run ./cmd/ada plan --preset examples/presets/base.preset.json   # M3:preset(多目录/disabled/patch)
go run ./cmd/ada inspect --preset examples/presets/ts-only.preset.json  # M3:诊断快照(fiber/服务/effect)
go run ./cmd/ada verify examples/plugins-ts/hello-ts  # M5:清单/摘要/验签/信任
go run ./cmd/ada list -dest <安装根>                 # M5:本机安装索引
go run ./cmd/ada uninstall <id> -dest <安装根>        # M5:卸载全部版本
go run ./cmd/ada upgrade <目录> -dest <安装根>         # M5:升级并清理旧版本
go run ./cmd/ada rollback <id> -dest <安装根>          # M5:回滚到上一版本
go run ./cmd/ada update <id> -repo <仓库> -dest <安装根>  # M5:离线仓库更新
go run ./cmd/ada caps examples/plugins -json          # M5:能力自检(caps/trust → Broker)
go run ./cmd/ada check examples/plugins-ts           # M5:清单/计划/caps/编译一键校验
pwsh scripts/bench.ps1                       # 性能预算:≤20 μs/次、50 插件冷启动 ≤150 ms
```

已完成:`kernel/`(Context / Fiber / Service / Effect / Event / Config / Schema / 闲置回收)、`kernel/schema`、`cmd/ada`、`cmd/ada-demo`、`cmd/ada-ts-demo`、`compile/`(esbuild)、`jsrt/`(moejs)、`bridge/`、`plugin/`(清单)、`preset/`(组合数据)、`assemble/`(计划/事务)、`loader/`(按 kind 装载)、`assemble/` 延迟激活(`UpLazy`/`Deferred`)、`plugins/seam/session`(seam + 契约测试)、`plugins/provider/session-memory`、`sched/`(调度底座)、`quota/`(配额与回压)、`flow/`(声明式流水线)、`bundle/`(组合层)、`expr/`(受限表达式)、`plugins/seam/fs` + `provider/fs-local` + `policy/fs-sandbox`、`plugins/seam/shell` + `provider/shell-local` + `policy/shell-guard`、`plugins/policy/fs-redact`(脱敏)、`plugins/policy/fs-observe`·`shell-observe`(观察事件)、`shell-redact`(输出脱敏)、`plugins/seam/sandbox` + `provider/sandbox-none`、`plugins/seam/approval` + `provider/approval-deny`、`caps/`(能力授权)、`install/`(安装与签名)、`host/cli`(命令行宿主适配器)、`host/server`(只读 HTTP 宿主)、`host/builtins`(共享内置插件)、`host/desktop`(视图映射骨架)、`host/registry`(只读仓库服务)(M5 安全)、`scripts/guard.ps1`、`scripts/bench.ps1`、`docs/kernel-api.md`、`docs/m2-execution.md`、`docs/m3-assembly.md`、`docs/m4-orchestration.md`、`docs/m4-flow.md`、`docs/seam-session.md`、`docs/seam-fs.md`、`docs/seam-shell.md`、`docs/seam-sandbox.md`、`docs/seam-approval.md`、`docs/adr/0001`、`docs/adr/0002`。
未开始:M5 剩余(caps 运行时接线(CLI 内置栈)/在线更新/`host/desktop`·`host/server`/Windows Job Object 进程树终止/OS 级沙箱后端)、剩余 seam(`prompts`/`llm`/`tools`);M4 剩余:flow 的 fanout、触发器接线(`onCron`/`onPage`,onService 已有 helper);M4.5 剩余:投影、session-jsonl、core/session。

本轮修复了 M1 评审中复现的缺陷(waterfall 超时幽灵执行、兄弟插件被误拆、failed 不可复活、卸载错误被吞、fiber 注册表泄漏、Intercept 合并反向、Stop 后仍派发、schema 静默放行),每条都有回归用例;清单见 [kernel-api.md §6](docs/kernel-api.md)。

## 下一步

1. 评审 seam 契约的 13 个待决问题（`seam-api.md` 附录 B + `seam-api-security.md` §6）。
2. M2：`compile/` + `jsrt/` + 调用桥，把 Go 插件与 TS 插件放到同一个 `kernel.Plugin` 下（S3/S5/S7 三个 spike 随 M2 一起做）。
3. M4.5 按顺序冻结合同：`session` → `prompts` → `llm` → `tools`；安全侧 `sandbox` → `fs` → `shell`。


