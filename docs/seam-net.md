# seam/net 与 provider/net-http(net.fetch 第一刀)

> 契约来源:[plugin-kernel-design.md](plugin-kernel-design.md) §13(capability `net.fetch[.host]`)。
> 本页记录已实现子集、偏差与边界。

## 1. 范围

```
plugins/seam/net/            契约:Request/Response/Client/Chain(只类型与链)
plugins/provider/net-http/   实现:基于 net/http 的一次抓取
plugins/policy/net-guard/    策略:principal + net.fetch.<host> + 私网段拒绝
plugins/policy/net-observe/  策略:每次抓取后广播 net/observe(观察,不阻断)
```

策略已有 `net-guard`:按 `net.fetch.<host>`(可用 `net.fetch` 通配)向 Broker 授权,默认拒绝私有/本地地址;审批与脱敏未实现。

## 2. 契约要点

| 类型/方法 | 语义 |
|---|---|
| `Request{Method,URL,Headers,Body}` | Method 空 = GET;只允许 http/https |
| `Response{Status,Headers,Body,URL,Truncated}` | 非 2xx **原样返回**,不自动报错 |
| `Chain.Install(base)` | provider 安装底座;重复安装报错 |
| `Chain.Decorate(fn, priority)` | policy 叠加;priority 越大越靠外 |
| `Chain.Get()` | 无底座 → `EUNIMPLEMENTED` |

## 3. provider 行为

- 超时:配置 `timeoutMs`(默认 30s);用 `context.WithTimeout` 包住整次请求;
- 响应上限:`maxBytes`(默认 8 MiB),超出截断并置 `Truncated=true`;
- 重定向:最多 5 次,之后返回最后一次响应(`http.ErrUseLastResponse`);
- 请求头逐项设置;响应头用 `", "` 合并多值;
- 非法 URL/scheme → 参数错误(`EINVALID`)。

## 4. 偏差与边界

1. **策略只有 `net-guard`**:caps 强制、字面私网与**域名解析后私网**拒绝已有;审批(访问未知 host 升级)与响应脱敏未实现;`net-http` 本身仍可访问任意 http(s),不装 net-guard 就没有门禁。
2. **没有请求体大小上限**:`Request.Body` 由调用方控制;provider 不限制。
3. **响应体全量读入内存**(到上限为止),没有流式接口。
4. **没有代理/证书/HTTP2 配置**;用默认 Transport。
5. **没有重试/退避**;一次失败即返回。
6. **SSRF 防护在 policy 层**:`net-guard` 拒绝字面私网与解析后的私网 IP;但存在 **TOCTOU**(校验时解析与请求时解析可能不同),且 provider 直接用默认 Transport,不做连接级地址校验。
7. **重定向后的最终 URL** 记录在 `Response.URL`,但中间跳转不可见。

### 4.1 `net/observe` 事件

`policy/net-observe` 在每次 Do 之后 `Emit("net/observe", payload)`:

| 字段 | 类型 | 说明 |
|---|---|---|
| `method` | string | 请求方法(空 = GET) |
| `host` | string | URL host(不含 path/query) |
| `status` | int | 响应状态码(失败/拒绝时 0) |
| `bytes` | int64 | 响应体字节数 |
| `durationMs` | int64 | 耗时(毫秒) |
| `plugin` | string | 调用者插件 id |
| `error` | string | 失败文本(成功时缺省) |

**不含 body/headers**(避免把 secret/内容写进事件);观察不阻断,`net-guard` 的拒绝也会被记录(priority=250 最外层)。

## 5. 测试矩阵

| 场景 | 用例 |
|---|---|
| 200 状态/头/体 | `nethttp.TestFetch` |
| 非 2xx 原样返回 | `nethttp.TestNon2xx` |
| 超限截断 | `nethttp.TestTruncate` |
| 超时失败 | `nethttp.TestTimeout` |
| 非法 scheme 拒绝 | `nethttp.TestBadScheme` |
| 内核集成(seam + provider + consumer) | `nethttp.TestKernelIntegration` |
| net-guard:host/通配授权、未授权/无身份、私网拒绝 | `netguard.TestAllowed`、`TestDenied`、`TestPrivateDenied` |
| net-guard 使用注入的 caps 服务 | `netguard.TestServiceBroker` |
| net-guard:DNS 解析到私网拒绝 | `netguard.TestResolvedPrivateDenied` |
| net-guard:DNS 解析公网放行 | `netguard.TestResolvedPublicAllowed` |
| net-guard:DNS 解析失败拒绝 | `netguard.TestResolverErrorDenied` |
| observe:成功/失败/内核事件身份 | `netobserve.TestRecordsSuccess`、`TestRecordsError`、`TestKernelIntegration` |

## 6. 下一步

1. `policy/net-guard`:按 `net.fetch.<host>` + principal 做授权(复用 caps.Broker);host 允许表与 deny 私有网段。
2. 把 `caps-gate` 扩展为也装饰 `net` 服务。
3. 请求体上限与流式响应。
4. 审批升级(访问未知 host 时走 approval)。

## 7. 验证命令

```powershell
go test -race -count=1 ./plugins/provider/net-http
go test -race -count=1 ./...
pwsh scripts/guard.ps1
```