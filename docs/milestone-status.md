# M4 / M5 验收矩阵

> 本文件把里程碑验收项映射到**实现位置、证据命令与已知边界**。口径来源:
> [plugin-kernel-design.md](plugin-kernel-design.md) §17、AGENTS §10。
> 最近更新:2026-11-20(测试总数 389,门禁与 bench 均通过)。

## 0. 结论速览

| 里程碑 | 状态 | 说明 |
|---|---|---|
| M4 编排 | ✅ 完成(文档口径) | sched/quota/flow/bundle/回收/延迟激活/jobs 锁 |
| M5 安全与宿主 | ⚠️ 部分完成 | 仅剩 **OS 级沙箱强制** 与 **真实桌面窗口**;见 [ADR 0001](adr/0001-deferred-m5-scope.md) |
| 工程预算 | ⚠️ 有例外 | host 层预算例外;见 [ADR 0002](adr/0002-host-budget-exception.md) |

## 1. M4 验收矩阵

| 验收项 | 实现位置 | 证据 | 已知边界 |
|---|---|---|---|
| 调度:周期任务/cron | `sched/` | `go test ./sched`(27) | 内存/文件存储;文件锁依赖 `O_EXCL` |
| 任务:once/skip/all + 同次防重 | `sched/` | `sched.Test*` | 时钟注入,无分布式协调 |
| jobs 跨进程锁 | `sched/file_store.go` | `sched` 用例 | 租约 5 分钟,崩溃后依赖租约过期 |
| 配额与回压 | `quota/` | `quota`(11) | 全局上限默认 `min(4*GOMAXPROCS,64)` |
| 声明式 flow | `flow/` | `flow`(29) | 数据是 JSON;校验在 `Parse` 期 |
| parallel / fanout | `flow/flow.go` | `flow.TestFanout` | fanout 是 parallel 别名 |
| forEach/并发上限 | `flow/` | `flow.Test*` | 上限 32;共享值表浅拷贝 |
| subflow(嵌套/环检测/上限) | `flow/` | `flow.TestSubflow*` | 最大深度 10;不继承 checkpoint |
| compensate | `flow/` | `flow.TestCompensateReverse` | 逆序执行,失败仍继续补偿 |
| checkpoint / resume | `flow/` | `flow.TestCheckpointResume` | 父步骤才支持;MemoryStore/FileStore |
| 检查点文件存储 | `flow/file_store.go` | `flow.TestFileStore*` | runID 白名单;无 fsync/跨进程锁 |
| 受限表达式 | `expr/` | `expr`(2) | 只允许白名单操作与路径 |
| bundle / profile / patch | `bundle/`,`preset/` | `bundle`(12)、`preset`(5) | 补丁语义与整体替换 |
| 引擎版本约束 | `version/`,`bundle/`,`plugin/` | `version`(3)、`bundle` | 只支持 `>= > <= < = !=` |
| 内核空闲回收 | `kernel/` | `kernel`(29) | 基于 Touch/LastActive |
| on-demand 延迟激活 | `assemble/` | `assemble`(22) | onService/onCron/onEvent |
| 宿主事件源(page/open) | `host/desktop` | `desktop.TestOpenPage` | 无真实窗口 |
| 组合文档 | `docs/m4-*.md` | — | 与实现同步维护 |

## 2. M5 验收矩阵

### 2.1 沙箱与文件/命令安全

| 验收项 | 实现位置 | 证据 | 已知边界 |
|---|---|---|---|
| sandbox 判定 seam | `plugins/seam/sandbox` | `sandbox`(5) | 只做判定,不强制 |
| policy-only 后端 | `plugins/provider/sandbox-none` | `sandbox` 用例 | `Enforced=false`(**OS 级强制见 ADR 0001**) |
| fs seam/provider | `seam/fs`,`provider/fs-local` | `fs`(12) | TOCTOU 已知 |
| fs 沙箱策略 | `policy/fs-sandbox` | `fs` 用例 | 可升级走 approval |
| fs 脱敏/观察 | `policy/fs-redact`,`fs-observe` | `fs-redact`(5)、`fs-observe`(5) | 观察不含内容 |
| shell seam/provider | `seam/shell`,`provider/shell-local` | `shell`(18) | 超时杀进程组(POSIX)/Windows Job |
| shell 策略(caps/审批) | `policy/shell-guard` | `shellguard.TestCapsEnforcement` | `allowShell` 仍是配置开关 |
| shell 脱敏/观察 | `policy/shell-redact`,`shell-observe` | 各 5 | 分块边界可能漏脱敏 |

### 2.2 审批与能力授权

| 验收项 | 实现位置 | 证据 | 已知边界 |
|---|---|---|---|
| approval seam | `plugins/seam/approval` | `approval`(10) | fail-closed,无审批人即拒绝 |
| 审批实现 | `approval-deny`,`approval-cli` | `approval-cli`(5) | CLI y/yes 交互 |
| 静态能力授权 | `plugins/provider/caps-static`,`caps/` | `caps`(6)、`caps-static`(1) | trust 等级决定能力 |
| 调用点强制 | `policy/caps-gate` | `caps-gate`(4) | fs/shell 调用点 |
| 网络升级审批 | `policy/net-guard` | `netguard.TestEscalation*` | 每次未授权都重新审批 |

### 2.3 网络与 secret

| 验收项 | 实现位置 | 证据 | 已知边界 |
|---|---|---|---|
| net seam/provider | `seam/net`,`provider/net-http` | `net-http`(6) | 超时/8MiB/重定向≤5/http(s) |
| net 授权/私网拒绝 | `policy/net-guard` | `net-guard`(9) | 校验与请求解析存在 TOCTOU |
| net 观察 | `policy/net-observe` | `net-observe`(4) | error 已脱敏 query;不含 body |
| secrets seam/provider | `seam/secrets`,`provider/secrets-env` | `secrets-env`(5) | 白名单空=拒绝;`ADA_SECRET_*` |
| secrets 授权 | `policy/secrets-guard` | `secrets-guard`(3) | 名称正则 + principal |

### 2.4 安装、审计与宿主

| 验收项 | 实现位置 | 证据 | 已知边界 |
|---|---|---|---|
| 安装校验/摘要/签名 | `install/` | `install`(31) | Ed25519;签名文件可选 |
| 原子安装/索引/回滚 | `install/` | `install` 用例 | 锁 TTL 10 分钟,TTL 误回收可能 |
| 在线索引/artifact | `install/fetch.go`,`artifact.go` | `install` 用例 | zip 16MiB/1000 文件;artifact 不单独签名 |
| 在线仓库服务端 | `host/registry` | `registry`(7) | 只读;无上传/发布 |
| CLI | `host/cli` | `host/cli`(29) | 参数手写解析;DevX 预算例外 |
| 只读 HTTP API | `host/server` | `host/server`(14) | Token/限流/TLS/CORS 可选 |
| 桌面映射骨架 | `host/desktop` | `desktop`(4) | **无真实窗口(mygo),见 ADR 0001** |
| 审计落盘 | `policy/audit-log` | `audit-log`(6) | 无 fsync;大小轮转 |

## 3. 门禁与证据

```powershell
pwsh scripts/guard.ps1     # 词汇/依赖/gofmt/build/vet/staticcheck/race/交叉编译/kernel+host 预算
pwsh scripts/bench.ps1     # 单次调用 ≤ 20 μs、50 插件冷启动 ≤ 150 ms
go test -race -count=1 ./...
```

当前实测:

- 测试 **389 个顶层用例**,全绿;
- kernel 生产 **2396 / 3000** 行;
- host 生产 **2108 / 2500**(cli 1254 / 1500,其它最大 404 / 800);
- bench:ServiceResolve 38ns、Emit 单监听 1.4μs、waterfall 4.4μs、50 插件 259μs。

## 4. 明确未完成项

1. **OS 级沙箱强制**(landlock/seatbelt/Job Object 级 fs/shell):当前只有 policy-only;
2. **真实桌面窗口(mygo)**:只有 Renderer/View/Subscribe/OpenPage 骨架。

两项均记录在 [ADR 0001](adr/0001-deferred-m5-scope.md),复议日期 2027-01-15;在此之前 **不得宣称 M5 完成**。
