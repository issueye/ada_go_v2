# M2 执行层(第一刀 + ctx 桥)

> 配套 [plugin-kernel-design.md](plugin-kernel-design.md) §10–§11 与 [ADR 0002](adr/0002-execution-layer-deps.md)。
> 本页描述**已实现**的部分与**已知边界**;未实现的部分明确列在 §5,不假装存在。

## 1. 范围

M2 的目标是「TS 插件在进程内跑起来,且不需要 Node」。当前已打通:

```
TS 源文件 ──compile.Build──▶ Artifact(每个文件的 JS,纯数据)
                                   │
                                   ▼
                    jsrt.New ──▶ moejs Runtime(每实例一个 goroutine)
                                   │  CallWithHost(apply, cfg)
                                   ▼
   apply(ctx, cfg) ──ctx.provide/emit──▶ stager ──返回内核协程──▶ 服务表 / 事件总线
                                   │
                                   ▼
                    bridge.Plugin ──▶ kernel 插件(fiber active/failed)
```

**不在这一刀里**:装配(assemble)、插件清单与安装、权限/能力、反向回调(`ctx.on` / `ctx.effect` / 服务方法)、`async apply`、池化、sourcemap 反查、预算/熔断。

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
`Options.TsconfigRaw` 可透传 `experimentalDecorators` 等开关。

### 2.2 `jsrt`

| API | 语义 |
|---|---|
| `New(ctx, Options)` | 编译 Artifact → `moejs.Link` → `Load`;每实例一个 worker goroutine |
| `Call(ctx, export, args...)` | 参数/返回值只走 JSON |
| `CallWithHost(ctx, export, args...)` | 在参数最前插入 `ctx` 对象,供 `apply(ctx, cfg)` 使用 |
| `Options.Host` | `ctx.provide` / `ctx.emit` 的落地回调(在 worker goroutine 上执行) |
| `Close(ctx)` | 停止 worker;可重复调用 |
| `ErrClosed` / `ErrBroken` | 关闭后调用 / 中断后未在宽限期内收尾 |

并发模型:Runtime 只在 worker goroutine 上使用,只有 `Interrupt`/`ClearInterrupt` 跨协程——这是 moejs 的硬约束(C1),包内用 `mu` 保护 `raw` 指针。

### 2.3 `bridge`

`bridge.Plugin` 实现 `kernel.Plugin`,入口约定与 AGENTS §6.2 一致:**`apply(ctx, config)`**。

JS 侧的 `ctx` 目前只有三个能力:

| 能力 | 行为 |
|---|---|
| `ctx.log(...)` | 立即写宿主日志(线程安全) |
| `ctx.provide(name, value)` | 暂存;`apply` 返回后由内核协程提交为 `kernel.Provide` |
| `ctx.emit(event, payload)` | 暂存;`apply` 返回后由内核协程提交为 `EmitContext` |

**为什么要暂存**:内核的生命周期操作是单写者(M1 §1),而 JS 跑在自己的 goroutine 上。直接在 JS 线程里 `Provide`/`Emit` 会同时触碰服务表、事件总线与 fiber 表;加锁只能消除数据竞争,消除不了「半新半旧的依赖图」这类语义竞争。

**提交语义**:
- 顺序 = JS 调用顺序;先提交 `provide`,再派发 `emit`;
- 提交发生在 `apply` 返回之后、仍在调用 `Apply` 的内核协程上;
- `apply` 结束后再调用 `provide`/`emit` 会抛 JS 异常(没有提交点的调用必须报错,不能静默丢失)。

## 3. 约束落点

| 约束 | 落点 |
|---|---|
| C1 单 goroutine | `jsrt.worker` 是唯一接触 `moejs.Runtime` 的 goroutine |
| C2 字节码不可序列化 | Artifact 只存 JS 源文本;不缓存字节码 |
| C3 跨边界只传 JSON | `Call` 逐个 `json.Marshal`/`ParseJSON`,返回值 `ToGo` |
| C4 裸 Runtime 无环境能力 | 只有 `ctx.log/provide/emit`;默认零权限 |
| P8 数据面留在 Go | JS 只处理控制面/小对象;大对象留后续句柄化 |
| 单写者 | JS 侧写请求一律经 stager,回到内核协程提交 |
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
| JS 提供服务并由 Go 消费 | ✅ | `bridge.TestJSPluginRunsInKernel` |
| JS 派发事件并由 Go 监听 | ✅ | `bridge.TestJSPluginRunsInKernel` |
| ctx 能力未启用时报错 | ✅ | `jsrt.TestHostCallbackErrorsSurfaceInJS` |
| CJS(`require`/`module.exports`) | ❌ | design §10 规定走离线 Bundle,M2 未做 |
| OOM / 内存上限 | ❌ | `jsrt.Options.MemoryLimit` 已透传,未写拒绝用例 |
| 卡死(不响应中断) | ❌ | `ErrBroken` 路径已实现,未写用例(需要不可中断的宿主函数) |

## 5. 已知边界与下一步

1. **反向回调缺失**:`ctx.on`、`ctx.effect`、由 JS 实现的**服务方法**(内核 → JS 调用)都还没有。它们需要事件循环 + 调用队列:`jsrt` 已经具备「一次调用一个结果」的通道,缺的是「内核发起、JS 应答」的对称队列。
2. **`apply` 必须同步返回**:`async apply` 返回的 Promise 尚未 await;落地时用 `moejs.PromiseResult` + 回调队列。
3. **apply 之后不能 provide/emit**:暂存点只存在于装配期;运行期事件需要与反向回调一起设计,否则会把事件派发变成跨协程写。
4. **无装配层**:Artifact 目前由代码直接构造;插件清单、安装、签名、capability 属于 M3/M5。
5. **sourcemap 未接**:`Transform` 支持产出 map,但错误堆栈还没有反查(design §10.6 / R3)。
6. **池化与预算**:`pool: N`、内存/时间预算、熔断未做。
7. **`__adaLog` 兼容别名**:上一版的全局函数仍保留,等 `ctx` 稳定后删除。

## 6. 验证命令

```powershell
go test -race -count=1 ./...          # 内核 + schema + compile + jsrt + bridge
go run ./cmd/ada-demo                 # M1:纯 Go 插件装配
go run ./cmd/ada-ts-demo              # M2:TS 插件编译→加载→提供服务/派发事件→内核消费
pwsh scripts/guard.ps1                # 红线门禁(含 kernel 不依赖执行层的检查)
```
