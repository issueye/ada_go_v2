# M4 流水线(flow)

> 配套 [plugin-kernel-design.md](plugin-kernel-design.md) §12.2(3)「流水线」与 §12.4「受限表达式求值」。
> 本页记录**已实现**的声明式子集与已知边界;parallel / fanout / forEach / subflow / Compensate 尚未实现。

## 1. 范围

```
flow.json ──flow.Parse──▶ *Flow(数据)
                              │
                              ▼
                       flow.Run(ctx, Runtime, Options)
                              │
              ┌───────────────┴────────────────┐
              ▼                                ▼
   Runtime.CallService(service, method)   Runtime.DispatchEvent(name, mode)
              │                                │
              ▼                                ▼
   kernel.Context.Service(inject 门禁)     EmitContext / Waterfall
```

流水线是 **L2 基础设施(不是插件)**:它把「先调谁、再调谁、失败怎么办」变成数据;
真正的调用仍走内核的服务与事件(同一授权/预算/审计),因此产品差异不进入代码。

## 2. 数据模型

```go
type Flow struct {
    Name       string `json:"flow"`
    Steps      []Step `json:"steps"`
    OnFailure  []Step `json:"onFailure,omitempty"`
    Compensate []Step `json:"compensate,omitempty"` // 预留
}

type Step struct {
    ID         string   `json:"id"`
    Use        *UseStep `json:"use,omitempty"`
    Event      *EventStep `json:"event,omitempty"`
    Abort      string   `json:"abort,omitempty"`
    When       string   `json:"when,omitempty"`
    Retry      *Retry   `json:"retry,omitempty"`
    Timeout    Duration `json:"timeout,omitempty"`
    Checkpoint bool     `json:"checkpoint,omitempty"`
}
```

- `use` / `event` / `abort` **三者互斥**;`when` 是真值门。
- `use.args` 里的字符串 `"$.step.field"` 会解析成上一步的结果(递归解析 map/数组)。
- `Duration` 在 JSON 里写 `"500ms"`、`"2s"`;写错在解析期报错。
- **未知字段一律报错**(流线是数据契约,拼错的字段不能被静默忽略)。

## 3. `when`:受限表达式

只允许:路径(`$.step.field`)、字面量(字符串/数字/true/false/null)、比较(`== != > >= < <=`)、
逻辑(`&& || !`)、括号。**没有函数调用、没有赋值、没有 I/O**——数据不允许带执行。

| 规则 | 说明 |
|---|---|
| 路径 | 从值表里读 `step` 的字段;缺失路径视为 `null` |
| 相等 | 数字按数值比较,其余按深比较 |
| 大小 | 只比较数字与字符串;类型不可比时**报错**,而不是静默为假 |
| 短路 | `&&` / `||` 短路 |
| 条件类型 | 最终结果必须是布尔(或 `null` 视为假);数字等非布尔的裸条件报错 |
| 编译期 | 语法错误在 `Validate`/`Run` 开始前暴露 |

## 4. 执行语义

| 能力 | 语义 |
|---|---|
| 顺序 | 严格按 `steps` 顺序;每步结果写入值表 `values[step.id]` |
| `when` | 为假 → 跳过并记入 `Skipped`,不再执行动作 |
| `abort` | 执行到该步且 `when` 为真 → 返回 `EABORTED`(可携带原因) |
| `retry` | `max` 是**重试次数**,总尝试 = `max+1`;`backoff` 用注入的 `Clock` 等待 |
| `timeout` | 每次尝试独立 `context.WithTimeout`;实现必须尊重 `ctx` |
| `checkpoint` | 该步成功后写检查点(`Store` + `RunID`);写失败按运行失败处理 |
| `resume` | 相同 `RunID` 再跑:跳过已完成步骤并恢复值表;检查点的流线名必须一致 |
| `OnFailure` | 主链失败后按顺序执行;结果写入 `values["__failureErrors"]` |

`Run` 返回 `Result{Values, Completed, Skipped}`,失败时也返回(便于诊断)。

## 5. Runtime:与内核的接线

```go
type Runtime interface {
    CallService(ctx context.Context, service, method string, args map[string]any) (any, error)
    DispatchEvent(ctx context.Context, name, mode string, input any) (any, error)
}
```

- `flow.KernelRuntime(ctx)` 用插件上下文实现:
  - `CallService` 走 `ctx.Service`(**inject 门禁**),服务必须实现 `flow.Invoker`:
    ```go
    type Invoker interface {
        Invoke(ctx context.Context, method string, args map[string]any) (any, error)
    }
    ```
  - `DispatchEvent` 的 `emit` 走 `EmitContext`,`waterfall` 走 `Waterfall`。
- 测试与宿主可替换 `Runtime`(例如只允许白名单服务的受限实现),Flow 数据不变。

## 6. 测试矩阵

| 场景 | 用例 |
|---|---|
| 路径/比较/逻辑/字面量/短路 | `flow.TestEvalExpressions` |
| 非法表达式、类型不可比、非布尔条件 | `flow.TestEvalRejectsBadExpressions` |
| 顺序执行 + `$.path` 参数解析 + event | `flow.TestRunSequential` |
| `when` 跳过 / `abort` 否决 | `flow.TestWhenSkipAndAbort` |
| retry 成功 / 用尽失败 | `flow.TestRetrySucceeds`、`flow.TestRetryExhausted` |
| 步骤超时 | `flow.TestStepTimeout` |
| checkpoint / resume | `flow.TestCheckpointResume` |
| OnFailure 步骤 | `flow.TestOnFailureSteps` |
| 声明式 JSON 解析与校验 | `flow.TestParseFlowJSON` |
| 与内核集成(服务 + waterfall) | `flow.TestKernelRuntimeIntegration` |

## 7. 已知边界与下一步

1. **没有并行**:`parallel` / `fanout` / `forEach` / `join` 未实现;当前严格顺序。
2. **没有子流水线**:`subflow` 与 trace 共享未实现。
3. **没有补偿链**:`Compensate` 字段保留但未执行。
4. **参数模板只有 `$.path`**:不支持在 args 里写表达式;需要时先做求值器扩展。
5. **检查点只有内存实现**:`MemoryStore` 供测试;落 `ctx.store` 需要 store seam(M4.5/M6)。
6. **没有 flow 服务插件**:目前由宿主/插件在代码里调用 `flow.Run`;还没有 `flow` 服务与清单入口。
7. **JS 侧不可用**:bridge 尚未暴露 flow API。

## 8. 验证命令

```powershell
go test -race -count=1 ./flow
go test -race -count=1 ./...
pwsh scripts/guard.ps1
```