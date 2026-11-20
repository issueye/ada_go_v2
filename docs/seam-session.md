# seam/session、session-memory 与 session-jsonl

> 契约来源:[seam-api.md](seam-api.md) §4。本页记录**已实现子集**、与契约的偏差、以及 provider 的约定;
> 未实现的部分明确列在 §5。

## 1. 范围

```
plugins/seam/seamerr/            跨 seam 的统一错误码(seam-api §1.2 子集)
plugins/seam/session/            session seam:类型 + 接口 + Registry + seam 插件
plugins/seam/session/sessiontest/ 契约测试(provider 必须全绿)
plugins/provider/session-memory/ 内存 provider + 内核插件
plugins/provider/session-jsonl/  文件 provider:一个会话一个 JSONL + 每会话 actor
plugins/provider/session-jsonl/journal/ 持久化抽象(Store / Journal)
plugins/provider/session-jsonl/file/    JSONL 文件后端
```

装载方式(kernel 服务模型):

```
seam/session 插件 ──provide "session"──▶ *session.Registry
memory/jsonl 插件 ──inject "session"──▶ Registry.Register(Provider)
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

## 3. session-memory

| 行为 | 约定 |
|---|---|
| `Open` | 每个会话独立;id 形如 `mem-1` |
| `Append` | 整批先校验再写入;seq 从 1 连续分配;补 `TS`/`SessionID`/`Source=core` |
| `Events` | `from` 是起始 seq(含);`limit<=0` 不限;返回副本 |
| `Subscribe` | 先回放既有事件再推新事件;缓冲 256,**慢订阅者丢事件**(日志才是权威来源) |
| `Turn` | 从 `turn/start` / `turn/end` 的载荷维护;`state` 缺省 `running`/`done` |
| `Fork` | 复制 `at` 为止的历史并**重新编号** seq;`Meta.Parent` 记录来源;父子互不影响 |
| `Compact` | 校验 `upTo` 范围后追加 `compact/summary` 事件,并记录已压缩边界 |

## 4. session-jsonl

一个会话一个文件,目录形如:

```
<dir>/<session-id>.jsonl
<dir>/<session-id>.meta.json
```

- `plugins/provider/session-jsonl` 的 `Store` / `Journal` 是持久化抽象:
  以后换 SQLite、bbolt 或内存后端时,只实现这两个接口,`Session` 与 `Provider` 的并发语义不变。
- `Store` 负责打开/创建/复制日志;`Journal` 负责单会话的 append、read、compact。
- **每会话 actor**:`Append` / `Compact` 进入该会话的命令队列,由同一个 goroutine 落盘;
  不同会话各有 actor 与文件,没有共享写锁,因此不会出现 SQLite 那样的全局写队列。
- 同一会话内仍保持单写者:seq 连续、append-only、不会交错;多个生产者可以并发调用
  `Append`,但由 actor 串行分配 seq。
- `Append` 默认 `sync=true`,每批落盘后 `fsync`;可配置 `sync=false` 换吞吐。
- 崩溃恢复:打开时逐行解析,最后一行不是完整 JSON 就截断到最后一条完整事件之后;
  完整但缺换行的尾行会补一个换行。
- `Fork` 保留 seq,把 `SessionID` 改为子会话 id,并写 `Meta.Parent`;
  `Compact` 只追加摘要,不重写历史。
- `OpenID` 是 provider 扩展(不属于 seam 最小接口),用于恢复已有会话:
  同一 id 已被打开时返回 `EINVALID`,避免两个写句柄。

配置:

```jsonc
{
  "id": "session-jsonl",
  "kind": "go",
  "inject": {"required": ["session"]},
  "config": {"dir": "examples/agent/sessions", "sync": true}
}
```

## 5. 与 seam-api §4 的偏差(有意,不是遗漏)

1. **`Project` / `Projection` 未实现**:接口仍不包含 `Project`;投影需要注册面与检查点,
   属于下一刀。
2. **`Fork` 重新编号 seq**:子会话保持「seq 从 1 连续」,坐标来源由 `Meta.Parent` 表达。
3. **单写者由 actor 串行,不是拒绝**:session 契约的「单写者」是逻辑语义;
   `Append` 可以来自多个 goroutine,但会被 actor 串行化。provider 只拒绝同一 id 的第二个 `Open`。
4. **无多进程租约**:当前假设一个进程拥有会话文件;跨进程写需要租约(后续)。
5. **`Compact` 不裁剪日志**:只追加摘要并记录边界,读路径仍能拿到全部历史。

## 6. 测试矩阵

| 场景 | 用例 |
|---|---|
| append 顺序 / seq 连续 / 分页 | `sessiontest.Run` → 「append 顺序」 |
| 空 type 拒绝(`EINVALID`) | 「空类型被拒绝」 |
| 未知插件事件可忽略 | 「未知事件可忽略」 |
| turn 状态 | 「turn 状态」 |
| 订阅回放 + 实时 + dispose 后不再推送 | 「订阅回放与推送」 |
| fork 隔离 | 「fork 隔离」 |
| compact 追加摘要 + 范围校验 | 「压缩追加摘要」 |
| jsonl 契约测试 | `jsonl.TestConformance` |
| 重新打开后事件/seq/meta 恢复 | `jsonl.TestPersistenceAcrossReopen` |
| 多会话并发写 | `jsonl.TestConcurrentSessions`(`-race`) |
| 同会话多生产者由 actor 串行 | `jsonl.TestSameSessionActorSerializes`(`-race`) |
| 半行崩溃恢复 | `jsonl.TestTornTailRecovery` |
| 路径穿越 id 拒绝 | `jsonl.TestRejectBadID` |
| 经内核装配 | `jsonl.TestPluginThroughKernel` |
| memory provider 经内核装配 | `memory.TestSeamAndProviderThroughKernel` |
| 无 provider 时返回 `EUNIMPLEMENTED` | `memory.TestRegistryWithoutProvider` |

## 7. 验证命令

```powershell
go test -race -count=1 ./plugins/provider/session-jsonl ./plugins/provider/session-memory
go run ./cmd/ada plan --preset examples/agent/agent-jsonl.preset.json
go run ./cmd/ada agent --preset examples/agent/agent-jsonl.preset.json -message "读取 a.txt 并总结"
pwsh scripts/guard.ps1
```

## 8. 已知边界与下一步

1. 投影(`Projection` + `Project` + 检查点)。
2. `core/session`:把 session 包装成 agent core 插件(轮次生命周期、compaction 触发)。
3. 多进程租约与跨进程恢复。
4. 日志裁剪/归档策略(当前只追加摘要)。
5. TS 侧访问:JS 插件目前没有 `ada:session` 模块;需要先把 seam 服务暴露给 bridge。
6. 版本协商:消费者声明期望的 seam 版本,provider 版本不足时应在 `Registry.Open` 阶段拒绝
   (现在只比较 `Provider.Version()`,没有消费者侧期望)。
