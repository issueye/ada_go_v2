# M3 装配层(第一刀)

> 配套 [plugin-kernel-design.md](plugin-kernel-design.md) §9.2(清单 v2)与 §12.1(装配编排)。
> 本页描述**已实现**的部分与**已知边界**;install / 签名 / capability / preset 都不在这一刀里。

## 1. 范围

M3 要回答的是「装哪些插件、按什么顺序、装不上怎么办」。第一刀交付四个东西:

```
plugin.json ──plugin.LoadDir──▶ []*Manifest(校验过的清单)
                                     │
                                     ▼
                         assemble.Plan ──▶ Report(顺序/问题/警告)
                                     │
                                     ▼
              loader.Lookup ◀── kind ──▶ go: 宿主注册表 / ts: 编译或 Artifact
                                     │
                                     ▼
                         assemble.Up ──▶ 挂载 + 失败整体回滚
                                     │
                                     ▼
                    ada plan / ada up ──▶ 人可读 / JSON 报告 / 真挂载
```

**不在这一刀里**:插件安装与更新、签名校验、capability 授权、preset/bundle/profile 的 patch 合并、`ada inspect`。

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

```go
type Report struct {
    Order    []string  // 拓扑顺序(有问题时只含已确定的部分)
    Problems []Problem // kind / plugin / service / detail
    Warnings []string
}
func (r *Report) OK() bool
func (r *Report) Err() error // *assemble.Error,码 EASSEMBLEPLAN
```

## 4. 装载(`loader.Lookup`)

按 `kind` 选路,把清单变成可挂载的 `kernel.Plugin`:

| kind | 来源 | 说明 |
|---|---|---|
| `go` | 宿主注册表 `map[id]func() kernel.Plugin` | Go 插件必须编译进宿主;CLI 里没有注册表,会得到明确错误 |
| `ts` + `artifact` | `compile.ParseArtifact` | 校验摘要后交给 `bridge.Plugin` |
| `ts` + `entry` | `loader.CollectSources` + `compile.Build` | 现场扫描目录(跳过 `node_modules` 与隐藏目录)并编译 |

装载层不做依赖排序(那是 `assemble.Plan`),也不做事务与回滚(那是 `assemble.Up`);它返回的函数类型与 `assemble.Lookup` 一致,所以装配层不需要知道 `compile`/`bridge` 的存在。

## 5. 装配(`assemble.Up`)

```go
up, err := assemble.Up(app, manifests, plan, lookup)
```

- 按计划顺序挂载,每个插件等待 `Ready`(上限 10s);
- **事务语义**:任一插件挂载/就绪失败 → 已挂载的插件**按逆序卸载**(与内核 effect 栈一致);
- 失败返回 `*assemble.UpError`:插件名、原因、回滚数量与回滚错误(码 `EASSEMBLEUP`);
- 计划本身不通过时直接拒绝执行(不会「装一半再报错」)。

## 6. CLI: `ada plan` / `ada up`

```powershell
go run ./cmd/ada plan examples/plugins       # 纯 Go 清单:顺序 + 可选依赖告警
go run ./cmd/ada plan examples/plugins -json # 机器可读报告
go run ./cmd/ada up examples/plugins-ts      # 真的挂载 TS 插件,打印 fiber/服务后停止
```

`ada up` 的实际输出:

```
  [js] hello-ts 装配完成,name=ada
装配完成: hello-ts
  fiber   hello-ts     active
  service ts.hello     provider=hello-ts#1 dependents=0
  [js] hello-ts 清理
已停止。
```

退出码:`0` 成功 / `1` 不可装配或装配失败 / `2` 用法错误。

## 7. 测试矩阵

| 场景 | 用例 |
|---|---|
| 清单解析与字段校验 | `plugin.TestParseValidManifest`、`plugin.TestValidateRules` |
| 未知字段拒绝 | `plugin.TestRejectsUnknownField` |
| 目录扫描 / 单插件目录 / 空目录 | `plugin.TestLoadDir` |
| 拓扑顺序 + 可选依赖告警 | `assemble.TestPlanOrderIsDeterministic` |
| 缺提供者 / 重复提供者 / 成环 / 级联阻断 / disabled / 重复 id | `assemble.TestPlan*` |
| 按序挂载并真的提供服务 | `assemble.TestUpMountsInPlanOrder` |
| 失败逆序回滚 | `assemble.TestUpRollsBackOnFailure` |
| 坏计划拒绝执行 / 缺 Lookup | `assemble.TestUpRejectsBadPlan`、`assemble.TestUpWithoutLookup` |
| go 插件按注册表装载(含缺注册报错) | `loader.TestGoLookup` |
| ts 源码模式装载并挂载 | `loader.TestTSSourceLookup` |
| ts Artifact 模式装载并挂载 | `loader.TestTSArtifactLookup` |
| 清单信息不足/未知 kind | `loader.TestTSLookupErrors` |
| 源码扫描跳过 node_modules 与隐藏目录 | `loader.TestCollectSources` |

## 8. 已知边界与下一步

1. **install / 签名 / capability** 未做(M5):`ada plan/up` 只读本地目录。
2. **版本约束缺失**:`inject` 只按服务名匹配,没有「期望 provider ≥ v2」;内核对 `MinVersion` 已有支持,plan 还没用上。
3. **preset / bundle / patch** 未做(M4):当前一个目录就是一份计划。
4. **`ada inspect`** 未做:目前只能看到 `ada up` 打印的即时快照。
5. **Go 插件无法从 CLI 动态加载**:Go 插件必须编译进宿主,CLI 只对 ts 插件完整可用;这符合「Go 优先、JS 用于第三方」的方向,但宿主需要自己提供注册表。
6. **多提供者的隔离槽**:`Isolate` 语义还没进入清单(`isolate: { shell: true }`)。
7. **热重载/增量编译**未做:每次 `ada up` 都是全量编译;S8 的 0.66 ms 数据说明代价可接受,但还没有接。

## 9. 验证命令

```powershell
go test -race -count=1 ./...                 # 含 plugin / assemble / loader
go run ./cmd/ada plan examples/plugins       # 人可读计划
go run ./cmd/ada up examples/plugins-ts      # 真的挂载 TS 插件
pwsh scripts/guard.ps1                       # 红线门禁
```