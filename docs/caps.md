# M5 能力授权(caps)

> 配套 [plugin-kernel-design.md](plugin-kernel-design.md) §13.2(信任等级与能力上限)、§13.3(强制点)。

## 1. 范围

```
caps/       L2 基础设施:能力名语法、信任等级、授权表(Broker)、拒绝审计
plugin/     清单新增 caps / trust 字段并在校验期拒绝非法值
plugins/policy/caps-gate/  调用点强制(fs/shell):principal → Broker 授权
plugins/provider/caps-static/  提供 "caps" 服务(*caps.Broker),让宿主/装配层共享同一个授权表
assemble/                 GrantManifests:按清单 caps/trust 灌入 Broker
```

本刀**只做静态授权本身**,不做调用点强制:能力真正的强制点在装饰器/服务层(fs/shell),
而调用点需要「调用者插件 id」,内核尚未把它传进 `ctx.Context`(见 §5)。

## 2. 能力名语法

```
fs.read | fs.write
exec
process | process.isolated
net.fetch | net.fetch.<host>
secrets.<name>
config.<namespace>
ui
```

- 未知命名空间、层级错误、空名一律非法(清单校验期拒绝)。
- 通配只放宽**目标**,不放宽命名空间:`net.fetch` 覆盖 `net.fetch.example.com`;
  `secrets.*` 覆盖 `secrets.GITHUB_TOKEN`;持有 `fs.read` 不会获得 `fs.write`。

## 3. 信任等级

| 能力 | 最低信任 |
|---|---|
| `fs.read` / `net.fetch` | third-party |
| `fs.write` / `ui` / `config` / `secrets` | verified |
| `exec` / `process` | official |

`builtin` > `official` > `verified` > `third-party`;`AllowedByTrust` 做等级比较。
信任来自**安装来源**(签名/仓库),不来自清单自述——校验期只检查值是否合法,来源判定属于安装流程(未实现)。

## 4. Broker

```go
broker := caps.NewBroker(audit)
broker.Grant("plugin-a", "fs.read", "exec")
broker.Authorize(ctx, "plugin-a", "fs.write", "/workspace/a") // → *DeniedError(EDENIED)
```

- **fail-closed**:未登记插件、未授予能力、能力名非法 → 拒绝。
- 拒绝写审计(`Deny{Plugin,Cap,Target,At}`);`AuthorizeWithTrust` 额外检查信任等级。
- `Grant` 整批校验,任一能力非法则整批失败(不会部分生效);`Revoke` 撤销全部。
- `Snapshot()` 供诊断。

## 5. 偏差与边界

1. **调用点强制已落地(fs/shell)**:`policy/caps-gate` 从 `kernel.Principal(ctx)` 取调用者,向 Broker 查能力;
   缺少身份/未授权一律拒绝。`caps-gate` 优先使用注入的 `caps` 服务;`assemble.GrantManifests` 可把清单 `caps`/`trust` 灌入同一个 Broker,但 CLI/preset 还没自动接线。
2. **清单未装载进 Broker**:`plugin.Manifest.Caps/Trust` 已能解析与校验,
   但 loader/assemble 还没把它们灌进 Broker;也没有 preset 层的信任覆盖。
3. **没有安装同意**:design 的「安装时展示能力并征得同意」未实现;`third-party` 的签名升级未实现。
4. **没有审计落地**:拒绝只走回调,没有 JSONL audit 出口与脱敏规则。
5. **没有目标级约束**:`fs.write` 的「工作区 + 插件自身目录」由 sandbox/fs-local 承担,
   caps 层不重复判定路径;`exec` 的程序 allowlist 未与 caps 合并。
6. **没有 net/config/secrets/ui 的强制点**:这些 seam 本身还没有实现。

## 6. 测试矩阵

| 场景 | 用例 |
|---|---|
| fs 授权放行/未授权/无身份拒绝 | `capsgate.TestFSAuthorization` |
| shell exec 授权 | `capsgate.TestShellAuthorization` |
| 内核集成:`ctx.Context()` 携带身份,background 绕过被拒 | `capsgate.TestKernelIntegration` |
| caps 服务优先于 config grants | `capsgate.TestServiceBrokerPreferred` |
| caps-static 提供 Broker | `capsstatic.TestProvidesBroker` |
| 清单 caps/trust 灌入 Broker、trust 不足拒绝 | `assemble.TestGrantManifests` |
| 能力名合法/非法集合 | `caps.TestParse` |
| 通配匹配(主机/名字/命名空间不放宽) | `caps.TestGrantsWildcard` |
| 授权 fail-closed、拒绝错误码、审计、撤销 | `caps.TestBrokerAuthorize` |
| 信任等级矩阵 | `caps.TestTrust` |
| 并发授权(-race) | `caps.TestConcurrent` |
| 拒绝信息可读 | `caps.TestDeniedMessage` |
| 清单 caps/trust 校验 | `plugin.TestCapsAndTrust` |

## 7. 下一步

1. assemble/loader 把清单的 `caps`/`trust`、install 的 trust 自动灌进 Broker(替代 config grants)。
2. 在 plan 报告里展示能力清单与 trust。
3. 安装同意 + 签名(§13.4)与 trust 来源判定。
4. JSONL 审计出口与脱敏。

## 8. 验证命令

```powershell
go test -race -count=1 ./caps ./plugin
go test -race -count=1 ./...
pwsh scripts/guard.ps1
```