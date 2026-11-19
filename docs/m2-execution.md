# M2 执行层(第一刀 + 双向 ctx 桥)

> 配套 [plugin-kernel-design.md](plugin-kernel-design.md) §10–§11 与 [ADR 0002](adr/0002-execution-layer-deps.md)。
> 本页描述**已实现**的部分与**已知边界**;未实现的部分明确列在 §5,不假装存在。

## 1. 范围

M2 的目标是「TS 插件在进程内跑起来,且不需要 Node」。当前已打通**双向**:

```
TS 源文件 ──compile.Build──▶ Artifact(每个文件的 JS,纯数据)
                                   │
                                   ▼
                    jsrt.New ──▶ moejs Runtime(每实例一个 goroutine)
                                   │
        ┌──────────────────────────┴──────────────────────────┐
        │ JS → 内核(暂存,回到内核协程提交)                  │ 内核 → JS(投递到 worker)
        ▼                                                      ▼
 apply(ctx, cfg)                                      ctx.on(event, handler)
   ctx.provide / ctx.emit ──▶ stager ──▶ 服务表/事件总线   内核事件 ──▶ CallFunction
                                                             服务方法 ──▶ CallFunction
```

**不在这一刀里**:装配(assemble)、插件清单与安装、权限/能力、`ctx.effect`、waterfall 型回调、`async apply`、池化、sourcemap 反查、预算/熔断。

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
| `Options.Host` | `provide` / `emit` / `on` 的落地回调 |
| `Service` | JS 侧 `ctx.provide` 的产物;`Call(ctx, method, args...)` 转发到 worker |
| `Close(ctx)` | 停止 worker;可重复调用 |
| `ErrClosed` / `ErrBroken` | 关闭后调用 / 中断后未在宽限期内收尾 |

并发模型:Runtime 只在 worker goroutine 上使用,只有 `Interrupt`/`ClearInterrupt` 跨协程——这是 moejs 的硬约束(C1),包内用 `mu` 保护 `raw` 指针。`ctx.on` 注册的函数与 JS 服务方法共用 worker 独占的 `handlers` 表(函数 + `this`),调用一律经 `CallFunction` 在 worker 上执行。

### 2.3 `bridge`

`bridge.Plugin` 实现 `kernel.Plugin`,入口约定与 AGENTS §6.2 一致:**`apply(ctx, config)`**。

JS 侧 `ctx` 目前有四个能力:

| 能力 | 方向 | 行为 |
|---|---|---|
| `ctx.log(...)` | JS → 宿主 | 立即写宿主日志(线程安全) |
| `ctx.provide(name, value)` | JS → 内核 | 暂存;`apply` 返回后提交为 `kernel.Provide`。对象上的函数成员成为**服务方法** |
| `ctx.emit(event, payload)` | JS → 内核 | 暂存;`apply` 返回后提交为 `EmitContext` |
| `ctx.on(event, handler)` | 内核 → JS | 暂存注册;提交为内核监听器,事件触发时把载荷投递到 worker 执行 JS 函数 |

**写方向为什么暂存**:内核的生命周期操作是单写者(M1 §1),而 JS 跑在自己的 goroutine 上。直接在 JS 线程里 `Provide`/`Emit` 会同时触碰服务表、事件总线与 fiber 表;加锁只能消除数据竞争,消除不了「半新半旧的依赖图」这类语义竞争。

**提交语义**:
- 顺序 = JS 调用顺序:先挂 `on`,再 `provide`,最后 `emit`——否则 apply 里 emit 的事件打不到自己刚注册的处理器;
- 提交发生在 `apply` 返回之后、仍在调用 `Apply` 的内核协程上;
- `apply` 结束后再调用 `provide`/`emit`/`on` 会抛 JS 异常(没有提交点的调用必须报错,不能静默丢失)。

**读方向**:
- 事件监听器在内核协程上被调用,把载荷 JSON 投递到 worker 并同步等待;返回值按 `emit` 语义忽略,抛错记为监听器失败。
- 服务方法由 Go 消费者显式调用:`svc.Call(ctx, "render", args...)`,同样走「内核 → worker → JSON」。
- `this` 绑定到服务对象本身,所以方法里读 `this.text` 能拿到兄弟成员。

**Go 侧拿到的服务值**:

```go
svc, err := kernel.Service[*jsrt.Service](ctx, "ts.greeting")
text := svc.Data()["text"]                 // 非函数成员(JSON)
out, err := svc.Call(ctx, "render")        // 方法调用转发到 JS worker
```

非对象值(标量/数组)放在 `svc.Value()` 里;`svc.Methods()` 给出方法名(排序)。

## 3. 约束落点

| 约束 | 落点 |
|---|---|
| C1 单 goroutine | `jsrt.worker` 是唯一接触 `moejs.Runtime` 的 goroutine |
| C2 字节码不可序列化 | Artifact 只存 JS 源文本;不缓存字节码 |
| C3 跨边界只传 JSON | 调用参数、事件载荷、方法参数与返回值都经 JSON 编解码 |
| C4 裸 Runtime 无环境能力 | 只有 `ctx.log/provide/emit/on`;默认零权限 |
| P8 数据面留在 Go | JS 只处理控制面/小对象;大对象留后续句柄化 |
| 单写者(写方向) | JS 侧写请求一律经 stager,回到内核协程提交 |
| 反向调用(读方向) | 内核 → worker → `CallFunction`;只读不写内核 |
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
| 内核事件 → JS 回调 | ✅ | `jsrt.TestHostOnInvokesJSHandler`、`bridge.TestJSPluginRunsInKernel` |
| Go 调用 JS 服务方法 | ✅ | `jsrt.TestServiceMethods`、`bridge.TestJSPluginRunsInKernel` |
| ctx 能力未启用/参数非法时报错 | ✅ | `jsrt.TestHostCallbackErrorsSurfaceInJS`、`jsrt.TestHostOnRejectsNonFunction` |
| CJS(`require`/`module.exports`) | ❌ | design §10 规定走离线 Bundle,M2 未做 |
| OOM / 内存上限 | ❌ | `jsrt.Options.MemoryLimit` 已透传,未写拒绝用例 |
| 卡死(不响应中断) | ❌ | `ErrBroken` 路径已实现,未写用例(需要不可中断的宿主函数) |

## 5. 已知边界与下一步

1. **`ctx.effect` 缺失**:注册型 API 还差「卸载时通知 JS 清理」这一环;需要把 disposer 反向调用的生命周期与 fiber 卸载顺序对齐。
2. **没有 waterfall 型回调**:`ctx.on` 是通知语义,处理器返回值被忽略。要参与「改写/否决」,需要 `ctx.waterfall` 与返回值穿透(与内核的 waterfall 闸门语义对齐)。
3. **回调里不能写内核**:处理器与服务方法里只能 `log`。要允许回调里 `emit`/`provide`,需要运行期提交队列,并避免「内核等 JS、JS 又等内核」的环。
4. **`apply` 必须同步返回**:`async apply` 的 Promise 尚未 await;落地时用 `moejs.PromiseResult`。
5. **无装配层**:Artifact 目前由代码直接构造;插件清单、安装、签名、capability 属于 M3/M5。
6. **sourcemap 未接**:`Transform` 支持产出 map,但错误堆栈还没有反查(design §10.6 / R3)。
7. **池化与预算**:`pool: N`、内存/时间预算、熔断未做。
8. **`__adaLog` 兼容别名**:上一版的全局函数仍保留,等 `ctx` 稳定后删除。

## 6. 验证命令

```powershell
go test -race -count=1 ./...          # 内核 + schema + compile + jsrt + bridge
go run ./cmd/ada-demo                 # M1:纯 Go 插件装配
go run ./cmd/ada-ts-demo              # M2:TS 插件编译→加载→双向 ctx(服务/事件/回调/方法)
pwsh scripts/guard.ps1                # 红线门禁(含 kernel 不依赖执行层的检查)
```
