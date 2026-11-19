# policy/audit-log(审计落盘)

> 配套 [seam-fs.md](seam-fs.md)/[seam-shell.md](seam-shell.md)/[seam-net.md](seam-net.md)/[seam-approval.md](seam-approval.md) 的事件契约。

## 1. 范围

```
plugins/policy/audit-log/   把观察/审批事件追加为 JSONL 文件
```

它只做「落盘」:不脱敏、不轮转、不上报、不阻断。

## 2. 事件

| 事件 | 来源 |
|---|---|
| `fs/observe` | `policy/fs-observe` |
| `shell/observe` | `policy/shell-observe` |
| `net/observe` | `policy/net-observe` |
| `approval/requested` | `seam/approval` 插件 |

## 3. 配置与格式

```json
{ "path": "审计文件路径(必填,父目录需存在)" }
```

每行:

```json
{"ts":"2026-...Z","event":"fs/observe","payload":{...}}
```

- 追加写,`0600` 权限;
- 互斥锁保护多事件并发写;
- 写入失败被忽略(审计不阻断业务),但会丢失记录(见边界)。

## 4. 测试

- `auditlog.TestWrites`:单事件写成一行、payload 完整;
- `auditlog.TestAppendsMultiple`:三类事件追加三行;
- `auditlog.TestMissingPath`:缺 `path` 插件失败。

## 5. 偏差与边界

1. **不脱敏**:依赖观察事件本身不含 body/secret;若事件 payload 含敏感字段会原样落盘。
2. **不轮转/不压缩**:文件无限增长;没有保留期。
3. **写入失败静默**:磁盘满/权限错误不会阻断业务,也没有错误上报。
4. **没有 fsync**:崩溃可能丢最后几行。
5. **父目录必须存在**:不自动 MkdirAll。
6. **没有远程上报/标准化 schema**:只是本地 JSONL。

## 6. 下一步

1. 轮转与保留期;
2. `fsync` 策略与失败计数;
3. 标准审计 schema(会话 id/调用链)与远程 sink。

## 7. 验证命令

```powershell
go test -race -count=1 ./plugins/policy/audit-log
go test -race -count=1 ./...
pwsh scripts/guard.ps1
```