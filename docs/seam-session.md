# seam/session 与 session-memory(第一刀)

> 契约来源:[seam-api.md](seam-api.md) §4。本页记录**已实现子集**、与契约的偏差、以及 provider 的约定;
> 未实现的部分明确列在 §5。

## 1. 范围

```
plugins/seam/seamerr/            跨 seam 的统一错误码(seam-api §1.2 子集)
plugins/seam/session/            session seam:类型 + 接口 + Registry + seam 插件
plugins/seam/session/sessiontest/ 契约测试(provider 必须全绿)
plugins/provider/session-memory/ 内存 provider + 内核插件
```

装载方式(kernel 服务模型):

```
seam/session 插件 ──provide "session"──▶ *session.Registry
provider 插件 ──inject "session"──▶ Registry.Register(Provider)
消费者 ──inject "session"──▶ Registry.Open(name, meta) ──▶ Session
```

**为什么 seam 也是插件**:产品可以只装 seam 不装实现;没有 provider 时消费者拿到的是
`EUNIMPLEMENTED`(而不是「服务不存在」导致 fiber 永远 `pending`)。

## 2. 契约(已实现)

```go
type Session interface {
    ID() string
    Meta() Meta
    Append(events ...Event) ([]uint64, error)
    Events(from uint64, limit int) ([]Event, error)
    Subscribe(from uint64) (<-chan Event, kernel.Dispose)
    Turn() TurnInfo
    Fork(at uint64) (Session, kernel.Dispose, error)
    Compact(ctx context.Context, upTo uint64, summary Event) error
}
```

- 事件类型常量与 `ModelVisible`(只有 `user/message`、`assistant/message`、`tool/result`、
  `compact/summary` 进入模型可见上下文)。
- `PluginEvent(id, kind)` 生成 `plugin:<id>/<kind>` 事件名;契约要求这类事件 `Ignorable=true`。
- `Registry` 的选择规则**与注册顺序无关**:默认取名字排序后的第一个 provider。
  「后注册覆盖先注册」会让装配顺序决定行为,这是装配层最忌讳的随机性。

## 3. 内存 provider 的语义

| 行为 | 约定 |
|---|---|
| `Open` | 每个会话独立;id 形如 `mem-1` |
| `Append` | 整批先校验再写入;seq 从 1 连续分配;补 `TS`/`SessionID`/`Source=core` |
| `Events` | `from` 是起始 seq(含);`limit<=0` 不限;返回副本 |
| `Subscribe` | 先回放既有事件再推新事件;缓冲 256,**慢订阅者丢事件**(日志才是权威来源) |
| `Turn` | 从 `turn/start` / `turn/end` 的载荷维护;`state` 缺省 `running`/`done` |
| `Fork` | 复制 `at` 为止的历史并**重新编号** seq;`Meta.Parent` 记录来源;父子互不影响 |
| `Compact` | 校验 `upTo` 范围后追加 `compact/summary` 事件,并记录已压缩边界 |

错误统一 `*seamerr.Error`:空事件类型 → `EINVALID`;取消 → `ECANCELED`。

## 4. 与 seam-api §4 的偏差(有意,不是遗漏)

1. **`Project` / `Projection` 未实现**:接口暂不包含 `Project`。投影需要注册面与检查点,
   属于下一刀;现在调用方只能用 `Events` 自行计算派生状态。
2. **`Fork` 重新编号 seq**:契约只说「fork at seq」,没说子会话的 seq 空间。重新编号保证
   子会话仍然满足「seq 从 1 连续」的不变量;坐标来源由 `Meta.Parent` 表达。
3. **单写者未被强制**:内存实现并发安全,但不会拒绝第二个写入方。约定由 `core/agent-loop`
   持有唯一写句柄;要强制需要租约(token),属后续工作。
4. **无持久化**:`provider/session-memory` 不做冷启动恢复;持久化由 `provider/session-jsonl`
   承担(未实现)。
5. **`Compact` 不裁剪日志**:只追加摘要并记录边界,读路径仍能拿到全部历史。裁剪策略需要
   与 resume/replay 一起设计。

## 5. 已知边界与下一步

1. 投影(`Projection` + `Project` + 检查点)。
2. `provider/session-jsonl`:一行一事件、崩溃安全的默认后端(seam-api §4.5)。
3. `core/session`:把 session 包装成 agent core 插件(轮次生命周期、compaction 触发)。
4. TS 侧访问:JS 插件目前没有 `ada:session` 模块;需要先把 seam 服务暴露给 bridge。
5. 版本协商:消费者声明期望的 seam 版本,provider 版本不足时应在 `Registry.Open` 阶段拒绝
   (现在只比较 `Provider.Version()`,没有消费者侧期望)。

## 6. 测试矩阵

| 场景 | 用例 |
|---|---|
| append 顺序 / seq 连续 / 分页 | `sessiontest.Run` → 子测试「append 顺序」 |
| 空 type 拒绝(`EINVALID`) | 子测试「空类型被拒绝」 |
| 未知插件事件可忽略 | 子测试「未知事件可忽略」 |
| turn 状态 | 子测试「turn 状态」 |
| 订阅回放 + 实时 + dispose 后不再推送 | 子测试「订阅回放与推送」 |
| fork 隔离 | 子测试「fork 隔离」 |
| compact 追加摘要 + 范围校验 | 子测试「压缩追加摘要」 |
| seam + provider 经内核装配 | `memory.TestSeamAndProviderThroughKernel` |
| 无 provider 时返回 `EUNIMPLEMENTED` | `memory.TestRegistryWithoutProvider` |

## 7. 验证命令

```powershell
go test -race -count=1 ./plugins/...
pwsh scripts/guard.ps1
```
