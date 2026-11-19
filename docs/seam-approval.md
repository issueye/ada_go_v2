# M5 安全(第四刀:seam/approval)

> 契约来源:[seam-api.md](seam-api.md) §5 与 [plugin-kernel-design.md](plugin-kernel-design.md) §12.2(门禁)。
> 本页记录**已实现子集**、偏差与边界。

## 1. 范围

```
plugins/seam/approval/           契约:Request / Decision / Approver / Registry / seam 插件
plugins/provider/approval-deny/  默认审批人:永远拒绝,给出可展示理由
plugins/policy/fs-sandbox/       集成点:可升级的拒绝(sandbox Escalatable)→ 问审批
```

装配关系:

```
seam/approval 插件 ──provide "approval"──▶ *approval.Registry
approval-deny   ──inject "approval"──▶ Registry.Register(默认拒绝)
fs-sandbox      ──可选 inject "approval"──▶ 拒绝且 Escalatable 时 Approve()
```

`fs-sandbox` 用**可选依赖**(`WithOptional("approval")` + `TryService`):没有 approval 服务时不报 pending,
但可升级的拒绝一律按拒绝处理(fail-closed)。

## 2. 契约要点

| 类型 | 说明 |
|---|---|
| `Request` | `Source` / `Op` / `Target` / `Reason` / `Details` / `Timeout` |
| `Decision` | `Allow` / `Reason`(必须可展示) / `Approver` |
| `Approver` | `Name()` + `Approve(ctx, Request)` |
| `Registry` | 按名注册/选择;默认取名字排序第一个;没有审批人 → 错误 |

## 3. 语义(本轮实现)

- **fail-closed**:没有 approval 服务、没有审批人、审批人报错 → 全部按拒绝处理;
  只有明确的 `Allow=true` 才放行。
- **默认拒绝人**:`approval-deny` 提供一个零交互、零等待的默认审批人,
  拒绝理由为「默认审批人:拒绝升级(未配置交互式审批)」。
- **升级触发点**:只有 sandbox 判定 `Allow=false` **且** `Escalatable=true` 时才问审批;
  不可升级的拒绝直接拒绝,不打扰审批人。
- **批准即放行本次操作**(在 policy-only 后端下等价于临时提升);OS 级后端需要重新施加约束,尚未实现。

## 4. 偏差与边界

1. **只有默认拒绝审批人**:没有交互式 UI/CLI 审批人;`core/approval`(M4.5)未实现。
2. **无超时**:`Request.Timeout` 字段预留,`Approve` 没有强制 deadline;一个挂起的审批人会阻塞调用方。
3. **没有事件**:未发 `approval/requested` / `approval/decided`;审计面缺口。
4. **只接了 fs**:`shell-guard` 的 `shell=true` 拒绝还没有走审批;`sandbox` 的 shell 判定也没有升级路径。
5. **批准绕过沙箱判定**:批准后的放行由策略层决定,OS 级后端尚未实现「按批准临时提升模式」。
6. **无 caps broker**:审批与能力授权仍分离;审批不是能力授权的替代。
7. **审批人选择**:默认按名字排序,没有「按 op/来源路由」的策略(例如 fs 用 A、shell 用 B)。

## 5. 测试矩阵

| 场景 | 用例 |
|---|---|
| 默认审批人永远拒绝 + 可展示理由 | `approvaldeny.TestAlwaysDenies` |
| 经内核装配(seam + provider) | `approvaldeny.TestKernelIntegration` |
| 没有审批人时 `EUNIMPLEMENTED` | `approvaldeny.TestNoApprover` |
| 审批允许 → 写放行;拒绝/缺失 → 仍拒绝 | `fssandbox.TestApprovalEscalation` |

## 6. 下一步

1. `shell-guard` 接入审批(`Shell=true` 与只读白名单外的命令)。
2. `approval/requested` / `approval/decided` 事件与审计。
3. 超时与取消(审批人必须尊重 ctx;gate 侧强制 deadline)。
4. `core/approval` 交互式审批人(UI/CLI),与 preset 的 approval 策略联动。
5. caps broker:静态能力授权先于动态沙箱/审批。

## 7. 验证命令

```powershell
go test -race -count=1 ./plugins/...
go test -race -count=1 ./...
pwsh scripts/guard.ps1
```