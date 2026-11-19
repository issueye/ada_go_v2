# M3 装配层(第一刀)

> 配套 [plugin-kernel-design.md](plugin-kernel-design.md) §9.2(清单 v2)与 §12.1(装配编排)。
> 本页描述**已实现**的部分与**已知边界**;install / 签名 / capability / preset 都不在这一刀里。

## 1. 范围

M3 要回答的是「装哪些插件、按什么顺序、装不上怎么办」。第一刀交付三个东西:

```
plugin.json ──plugin.LoadDir──▶ []*Manifest(校验过的清单)
                                     │
                                     ▼
                         assemble.Plan ──▶ Report(顺序/问题/警告)
                                     │
                                     ▼
                         assemble.Up ──▶ 挂载 + 失败整体回滚
                                     │
                                     ▼
                         ada plan <目录> ──▶ 人可读 / JSON 报告
```

**不在这一刀里**:插件安装与更新、签名校验、capability 授权、preset/bundle/profile 的 patch 合并、`ada up` / `ada inspect`。

## 2. 清单(`plugin/`)

字段是 design §9.2 的子集:

| 字段 | 必填 | 语义 |
|---|---|---|
| `id` | ✅ | kebab-case,全局唯一 |
| `version` | ✅ | semver(`0.1.0`、`1.2.3-beta.1`) |
| `kind` | ✅ | `go` 或 `ts` |
| `entry` / `artifact` | ts 必填其一 | 源码入口 / 编译产物路径 |
| `inject.required` | | 必需依赖;无人提供 → 计划失败 |
| `inject.optional` | | 可选依赖;无人提供 → 只告警 |
| `provide` | | 本插件提供的服务名 |
| `config` | | 传给插件的配置对象 |
| `disabled` | | 为 true 时计划排除并告警 |

校验规则(全部有测试):

- **未知字段直接报错**(`DisallowUnknownFields`):清单是契约,拼错的字段静默忽略会让作者以为它生效了;
- `id` 必须 kebab-case,`version` 必须 semver,`kind` 只能是 `go`/`ts`;
- `provide`/`inject.*` 不允许空名与重复名;
- 同一服务不能既 `provide` 又 `require`(自依赖,装配期必然成环);
- 同一服务不能同时出现在 required 与 optional 里。

错误统一为 `*plugin.Error`,带 `EPLUGINIO` / `EPLUGINMANIFEST` 码。

## 3. 计划(`assemble.Plan`)

纯函数:同一批清单永远得到同一个顺序。

规则按优先级:

1. **重复 id** → 阻断;
2. **同一服务多个提供者** → 阻断(否则「谁生效取决于注册顺序」);
3. **必需依赖无人提供** → 阻断,该插件不进入顺序;
4. **级联**:提供者自己装不起来 → 依赖它的消费者也阻断(否则会永久 pending);
5. **可选依赖无人提供** → 只告警;
6. **成环** → 阻断,并点名环路径(`a → b → a`);
7. 拓扑排序,同级按 id 排序 → 确定性顺序。

报告结构:

```go
type Report struct {
    Order    []string  // 拓扑顺序(有问题时只含已确定的部分)
    Problems []Problem // 阻断项:kind / plugin / service / detail
    Warnings []string
}
func (r *Report) OK() bool
func (r *Report) Err() error // *assemble.Error,码 EASSEMBLEPLAN
```

## 4. 装配(`assemble.Up`)

```go
up, err := assemble.Up(app, manifests, plan, lookup)
```

- `lookup` 负责把清单变成 `kernel.Plugin`(go 插件查注册表,ts 插件由 bridge 构造);
- 按计划顺序挂载,每个插件等待 `Ready`(上限 10s);
- **事务语义**:任一插件挂载/就绪失败 → 已挂载的插件**按逆序卸载**(与内核 effect 栈一致);
- 失败返回 `*assemble.UpError`:带插件名、原因、回滚数量与回滚错误(码 `EASSEMBLEUP`);
- 计划本身不通过时直接拒绝执行(不会「装一半再报错」)。

## 5. CLI: `ada plan`

```powershell
go run ./cmd/ada plan examples/plugins
go run ./cmd/ada plan examples/plugins -json
```

输出示例:

```
清单(3): greeter@0.1.0(go), presenter@0.1.0(go), upper@0.1.0(go)
顺序: greeter → upper → presenter
警告: 插件 presenter 的可选依赖 "telemetry" 未提供(按缺失处理,不影响装配)
结果: 可装配
```

退出码:`0` 可装配 / `1` 不可装配或加载失败 / `2` 用法错误。`-json` 输出 `{ok, plugins, order, warnings, problems}`。

## 6. 测试矩阵

| 场景 | 用例 |
|---|---|
| 清单解析与字段校验 | `plugin.TestParseValidManifest`、`plugin.TestValidateRules` |
| 未知字段拒绝 | `plugin.TestRejectsUnknownField` |
| 目录扫描 / 单插件目录 / 空目录 | `plugin.TestLoadDir` |
| 拓扑顺序 + 可选依赖告警 | `assemble.TestPlanOrderIsDeterministic` |
| 缺提供者 | `assemble.TestPlanRejectsMissingProvider` |
| 重复提供者 | `assemble.TestPlanRejectsDuplicateProvider` |
| 成环并点名 | `assemble.TestPlanRejectsCycle` |
| 级联阻断 | `assemble.TestPlanCascadesBlockedProvider` |
| disabled / 重复 id | `assemble.TestPlanDisabledAndDuplicateID` |
| 按序挂载并真的提供服务 | `assemble.TestUpMountsInPlanOrder` |
| 失败逆序回滚 | `assemble.TestUpRollsBackOnFailure` |
| 坏计划拒绝执行 | `assemble.TestUpRejectsBadPlan` |
| 缺 Lookup 明确报错 | `assemble.TestUpWithoutLookup` |

## 7. 已知边界与下一步

1. **install / 签名 / capability** 未做(M5):当前 plan 只读本地目录。
2. **ts 插件的 Artifact 解析**没接:清单里的 `entry`/`artifact` 只做必填校验,还没有把它接到 `compile.Build` + `bridge.Plugin`。
3. **版本约束缺失**:`inject` 只按服务名匹配,没有「期望 provider ≥ v2」;内核对 `MinVersion` 已有支持,plan 还没用上。
4. **preset / bundle / patch** 未做(M4):当前一个目录就是一份计划。
5. **`ada up` / `ada inspect`** 未做:装配执行目前只能从代码调用 `assemble.Up`。
6. **多提供者的隔离槽**:`Isolate` 语义还没进入清单(`isolate: { shell: true }`)。

## 8. 验证命令

```powershell
go test -race -count=1 ./...              # 含 plugin / assemble
go run ./cmd/ada plan examples/plugins    # 人可读计划
pwsh scripts/guard.ps1                    # 红线门禁
```