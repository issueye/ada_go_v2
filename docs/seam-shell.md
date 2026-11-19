# M5 安全(第二刀:seam/shell)

> 契约来源:[seam-api-security.md](seam-api-security.md) §4。本页记录**已实现子集**、偏差与边界。

## 1. 范围

```
plugins/seam/shell/             契约:Command / Result / Chunk / Env / Chain / seam 插件
plugins/provider/shell-local/   实现:无 shell 直接 exec、受管环境、超时、输出上限、流式
plugins/policy/shell-guard/     策略:只读白名单、shell 模式默认拒绝
```

装配关系与 fs 一致:`seam-shell` 提供 `*shell.Chain`;provider `Install` 实现;policy `Decorate`;消费者 `Get` 得到组装后的 `Shell`。

## 2. 契约要点(已实现)

| 类型/字段 | 说明 |
|---|---|
| `Command.Program` | 程序名/路径,**不含空白**;`echo; rm -rf` 这类串被拒绝(不交给 shell) |
| `Command.Args` | 直接 exec 参数,不经 shell 解析 |
| `Command.Dir` | 虚拟路径(`/workspace`、`/tmp`);越界拒绝;缺省为 workspace 根 |
| `Command.Env` | 只接受受管变量(白名单 ∩ 宿主 + `ADA_*`);注入其它键 → `EINVALID` |
| `Command.Timeout` | 缺省 120s;上限 30m(更长请走 jobs) |
| `Command.Shell` | 默认 false;true 需要 `allowShell` 授权,否则 `EDENIED` |
| `Result.Facts` | 本次实际约束;未接沙箱时 `Enforced=false`,结果如实反映 |
| `Result.Truncated` | 输出超限(默认 4 MiB,可配)时置 true,命令被终止 |
| `Stream` | 按 pipe 读取块回调(**≤64 KiB**),同步回调形成回压;回调失败 → 终止命令 |

## 3. 安全不变量(实现侧)

- **无 shell 默认**:`exec.CommandContext(program, args...)`,程序名有空白直接拒绝。
- **环境净化**:子进程 `Env` 只含受管变量;宿主 `SECRET`/未声明变量不可见。
- **超时终止**:`context.WithTimeout` + 显式 `terminate`;POSIX 用进程组杀整棵树。
- **输出上限**:收集器截断并取消命令;超限返回 `Truncated=true`(不是静默截断)。
- **工作目录**:虚拟路径解析,`..`/反斜杠/NUL/未知根拒绝。
- **非零退出不是错误**:退出码是结果的一部分;seam 错误只表示「没跑起来/被拒绝/超时」。

## 4. 偏差与边界(诚实记录)

1. **Windows 进程树未实现**:只 `Process.Kill()` 父进程,未用 Job Object;超时后子进程可能残留
   (测试用单进程 sleep 规避,但这正是缺口)。POSIX 已用 `Setpgid` + `kill(-pgid)`。
2. **`Facts` 是 shell 本地类型**:`sandbox` seam 未落地,`Enforced` 恒为 false、`Backend="none"`。
   设计要求的「Facts 来自 sandbox」未满足。
3. **没有事件**:`shell/start`、`shell/exited`、`shell/denied` 尚未发出(观测/审计缺口)。
4. **没有 `shell-env` 策略**:provider 自身限制 env;独立策略文件未做。
5. **沙箱判定已接到 `sandbox.Resolve`**:`shell-guard` 先问 sandbox(操作/程序),再做 shell 门与白名单;程序级 jail 仍靠 OS 级后端(未实现)。
6. **没有能力位/审批**:caps broker 与 approval 尚未实现;`allowShell` 是配置开关而非授权流程。
7. **流式顺序**:stdout/stderr 由两个 reader 并发推送,`Seq` 只保证每路递增,不保证两路全序。
8. **后台任务**:超过 30s 的命令被拒绝(未接 jobs seam)。
9. **Windows 路径特性**(盘符/UNC/保留名)未覆盖。

## 5. 测试矩阵

| 场景 | 用例 |
|---|---|
| 基本执行:输出/退出码/耗时/Facts | `shelllocal.TestRunEcho` |
| 环境净化与未白名单注入 | `shelllocal.TestEnvIsolation` |
| `Program` 含空白拒绝 | `shelllocal.TestRejectsProgramWithSpace` |
| 超时(`ETIMEOUT`)且及时返回 | `shelllocal.TestTimeout` |
| 输出上限截断 | `shelllocal.TestOutputTruncation` |
| 流式 chunk + 汇总 | `shelllocal.TestStream` |
| `Shell=true` 默认拒绝 | `shelllocal.TestShellModeDenied` |
| 非零退出码是结果不是错误 | `shelllocal.TestNonZeroExit` |
| `Dir` 越界拒绝 | `shelllocal.TestDirEscape` |
| 只读白名单 / shell 授权 / 内核装配 / 无 provider | `shellguard.TestReadOnlyAllowlist`、`TestShellFlag`、`TestKernelIntegration`、`TestKernelWithoutProvider`、`TestProviderRequiresWorkspace` |

## 6. 下一步

1. `sandbox` seam + probe(landlock/bwrap/Job Object),让 `Facts` 真正回填;Windows 用 Job Object 杀树。
2. `shell/start`/`shell/exited`/`shell/denied` 事件与审计。
3. `policy/shell-env` 独立装饰器(env 过滤从 provider 移出)。
4. `seam/jobs`:长任务、后台、交互。
5. caps broker:把 `allowShell` 从配置升级为能力授权 + 审批(fail-closed)。

## 7. 验证命令

```powershell
go test -race -count=1 ./plugins/...
go test -race -count=1 ./...
pwsh scripts/guard.ps1
```