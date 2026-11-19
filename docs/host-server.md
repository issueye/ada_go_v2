# host/server(只读 HTTP 适配器)

> 配套 [plugin-kernel-design.md](plugin-kernel-design.md) §12(宿主)与 AGENTS §2.2(宿主适配器)。

## 1. 范围

```
host/server/   只读 HTTP API:plan / caps / list
```

它只做协议映射与路径约束,不做业务;所有查询都必须落在 `Options.Root` 之下。

## 2. 路由

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/plan?path=<相对路径>` | 插件目录或 `.preset.json` → 依赖顺序/问题(JSON) |
| GET | `/api/caps?path=<相对路径>` | 清单 caps/trust → `GrantManifests` 静态检查(JSON) |
| GET | `/api/list?dest=<相对路径>` | 读取安装索引(JSON) |
| GET | `/` | 列出可用路由 |

- 非 GET → **405**;
- 绝对路径、`..` 逃逸、符号链接指向根外 → **400**;
- 没有鉴权,只应绑定 localhost 或由外层网关保护。

## 3. 安全约束

`resolve` 做三层检查:相对路径 + lexical 越界 + `EvalSymlinks` 后的越界(防止符号链接逃逸)。
根目录在 `NewHandler` 时解析为绝对路径并解析符号链接一次。

## 4. 测试

`host/server.Test*` 用 `httptest` 直接打 handler:

- plan 顺序、caps 授权、路径逃逸 400、POST 405、list 索引、根路由;
- 不启动真实端口,不依赖网络。

## 5. 偏差与边界

1. **只读**:没有 `up`(挂载/生命周期)与审批/执行接口;写操作留给后续设计。
2. **无鉴权/无 TLS**:本地开发工具定位;跨机部署需外层网关。
3. **root 内任意目录可读**:没有按插件/用户的能力过滤。
4. **没有分页/流式**:结果一次性 JSON;大插件集另说。
5. **没有 CORS/限流**。

## 6. 下一步

1. 认证(令牌)与只读白名单;
2. `/api/inspect`(需要一次挂载生命周期与单例 app);
3. SSE/流式输出;
4. desktop 宿主(mygo)共享同一套路由语义。

## 7. 验证命令

```powershell
go test -race -count=1 ./host/server
go test -race -count=1 ./...
pwsh scripts/guard.ps1
```