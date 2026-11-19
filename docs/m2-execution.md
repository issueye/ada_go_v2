# M2 执行层(第一刀)

> 配套 [plugin-kernel-design.md](plugin-kernel-design.md) §10–§11 与 [ADR 0002](adr/0002-execution-layer-deps.md)。
> 本页描述**已实现**的部分与**已知边界**;未实现的部分明确列在 §5,不假装存在。

## 1. 范围

M2 的目标是「TS 插件在进程内跑起来,且不需要 Node」。第一刀只切最小闭环:

```
TS 源文件 ──compile.Build──▶ Artifact(每个文件的 JS,纯数据)
                                   │
                                   ▼
                    jsrt.New ──▶ moejs Runtime(每实例一个 goroutine)
                                   │  Call(export, JSON 参数)
                                   ▼
                    bridge.Plugin ──▶ kernel 插件(fiber active/failed)
```

**不在第一刀里**:装配(assemble)、插件清单与安装、权限/能力、完整 `ctx.*` JS API、跨 goroutine 的锁、池化、sourcemap 反查、预算/熔断。

## 2. 三个包

### 2.1 `compile`

| API | 语义 |
|---|---|
| `Transform(name, src, opts)` | 单文件降级到 ES2022 ESM;不解析路径、不打包、不落盘 |
| `Build(id, entry, files, opts)` | 逐文件转换并计算摘要,产出 `Artifact` |
| `Artifact.Resolve(referrer, spec)` | **只支持相对导入**;允许省略扩展名、`.js` → `.ts`、`/index` |
| `Artifact.Verify()` | 结构与摘要校验;从磁盘/网络拿到的产物必须先过这一关 |
| `ParseArtifact`/`JSON` | 落盘与跨进程传输用的纯数据格式 |

错误统一为 `*compile.Error`(带 `KindSyntax`/`KindInternal` 与逐条行列诊断)。

### 2.2 `jsrt`

| API | 语义 |
|---|---|
| `New(ctx, Options)` | 编译 Artifact → `moejs.Link` → `Load`;每实例一个 worker goroutine |
| `Call(ctx, export, args...)` | 参数/返回值只走 JSON;ctx 取消会 `Interrupt` 正在运行的 JS |
| `Close(ctx)` | 停止 worker;可重复调用 |
| `ErrClosed` / `ErrBroken` | 关闭后调用 / 中断后未在宽限期内收尾 |

并发模型:Runtime 只在 worker goroutine 上使用,只有 `Interrupt`/`ClearInterrupt` 跨协程——这是 moejs 的硬约束(C1),包内用 `mu` 保护 `raw` 指针。

### 2.3 `bridge`

`bridge.Plugin` 实现 `kernel.Plugin`:装配时调用入口模块的 `apply(cfg)`,配置与返回值都走 JSON;卸载时关闭运行时。

**目前注入的宿主能力只有 `__adaLog`**(裸 Runtime 没有 `console`,C4)。这是有意的最小面:在跨 goroutine 的锁落地之前,JS 不允许回写内核状态(provide/emit/effect 都会触碰服务表与事件总线)。

## 3. 约束落点

| 约束 | 落点 |
|---|---|
| C1 单 goroutine | `jsrt.worker` 是唯一接触 `moejs.Runtime` 的 goroutine |
| C2 字节码不可序列化 | Artifact 只存 JS 源文本;不缓存字节码 |
| C3 跨边界只传 JSON | `Call` 逐个 `json.Marshal`/`ParseJSON`,返回值 `ToGo` |
| C4 裸 Runtime 无环境能力 | 只显式注入 `__adaLog`;默认零权限 |
| P8 数据面留在 Go | JS 只处理控制面/小对象;大对象留后续句柄化 |
| 内核无知 | `kernel/` 不 import `compile`/`jsrt`/`bridge`(guard 的 `go list -deps` 强制) |

## 4. 测试矩阵(M2 DoD 的 12 类 fixture)

| fixture | 状态 | 用例 |
|---|---|---|
| 正常调用 | ✅ | `jsrt.TestCallMultiModule`、`bridge.TestJSPluginRunsInKernel` |
| JS 抛错 | ✅ | `jsrt.TestCallReportsJSException` |
| 超时/死循环 | ✅ | `jsrt.TestTimeoutInterruptsAndRuntimeRecovers`(中断后运行时仍可用) |
| 多文件相对导入 | ✅ | `jsrt.TestCallMultiModule`、`compile.TestBuildResolveAndDigest` |
| 循环依赖 | ✅ | `jsrt.TestCircularImports` |
| TS enum / 参数属性 | ✅ | `compile.TestTransformLowersTSRuntimeSemantics` |
| 装饰器(现代 + 实验) | ✅ | `compile.TestTransformDecorator` |
| TSX | ✅ | `compile.TestTransformTSX` |
| 缺模块 | ✅ | `jsrt.TestErrorsAreExplicit` |
| 缺导出 | ✅ | `jsrt.TestErrorsAreExplicit` |
| CJS(`require`/`module.exports`) | ❌ | design §10 规定走离线 Bundle,M2 未做 |
| OOM / 内存上限 | ❌ | `jsrt.Options.MemoryLimit` 已透传,未写拒绝用例 |
| 卡死(不响应中断) | ❌ | `ErrBroken` 路径已实现,未写用例(需要不可中断的宿主函数) |

## 5. 已知边界与下一步

1. **`ctx.*` JS API 缺失**:JS 侧目前只能 `apply` + `__adaLog`。下一步按 design §11.6 加 `ctx.log/on/emit/provide/effect`,前提是先设计「JS 回调进入内核」的串行化(内核单写者假设,M1 §1)。
2. **无装配层**:Artifact 目前由代码直接构造;插件清单、安装、签名、capability 属于 M3/M5。
3. **sourcemap 未接**:`Transform` 支持产出 map,但错误堆栈还没有反查(design §10.6 / R3)。
4. **池化与预算**:`pool: N`、内存/时间预算、熔断未做。
5. **`__adaLog` 是全局函数而非对象**:等 `ctx` 对象落地后统一收进 `ctx.log`,保留 `__adaLog` 作为兼容层一个版本。

## 6. 验证命令

```powershell
go test -race -count=1 ./...          # 内核 + schema + compile + jsrt + bridge
go run ./cmd/ada-demo                 # M1:纯 Go 插件装配
go run ./cmd/ada-ts-demo              # M2:TS 插件编译→加载→挂到内核
pwsh scripts/guard.ps1                # 红线门禁(含 kernel 不依赖执行层的检查)
```
