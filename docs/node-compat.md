# Node 内置模块兼容层

> 执行层位于 `platform/jsrt`;模块解析同时覆盖 `node:*`、裸名别名与旧的 `ada:*` 注入模块。
> 参考实现:ada_go 的 `internal/agent_core/plugins/host/nodemods` 与 `caps.go`。

## 1. 目标

插件作者应当能用 Node 的习惯写脚本插件:

```ts
import fs from "node:fs"
import path from "node:path"
import { EventEmitter } from "node:events"

fs.writeFileSync("out.txt", "hello")
const p = path.join("/workspace", "out.txt")
```

不再要求每个项目自己记一套 `ada:fs` / `ada:shell` 的私有 API。`ada:*` 保留为兼容入口,
新脚本优先写 `node:*`。

## 2. 已实现模块

| 模块 | 来源 | 说明 |
|---|---|---|
| `node:fs` | moejs `node` 包 | 根固定为 `/workspace`;同步形状可用,回调/promise 形状已导出 |
| `node:fs/promises` | moejs `node` 包 | 同上;当前插件生命周期是同步的,promise 完成不保证早于 `apply` 返回 |
| `node:buffer` | moejs `node` 包 | 同时安装全局 `Buffer` |
| `node:path` | `platform/jsrt/nodemods` | posix 算法;裸名 `path` 同实例 |
| `node:os` | `platform/jsrt/nodemods` | 来自 `__adaOSInfo`;`hostname()`/`homedir()` 为空 |
| `node:events` | `platform/jsrt/nodemods` | `EventEmitter` 常用面 |
| `node:util` | `platform/jsrt/nodemods` | `format`/`inspect`/`promisify` 等子集 |
| `node:assert` | `platform/jsrt/nodemods` | `ok`/`equal`/`deepStrictEqual`/`throws` 等 |
| `node:url` | `platform/jsrt/nodemods` | `URL`/`URLSearchParams`/`fileURLToPath` 子集 |
| `node:querystring` | `platform/jsrt/nodemods` | Node 形状子集 |
| `node:string_decoder` | `platform/jsrt/nodemods` | 仅 UTF-8 |
| `node:process` | `platform/jsrt/nodemods` | `cwd()` 固定 `/workspace`;`env` 为空;`exit` 抛错 |
| `node:crypto` | `platform/jsrt/nodemods` | 仅 `createHash("sha256")`/`randomBytes`/`randomUUID`;`update` 只收字符串;随机源是宿主 `crypto/rand` |
| `node:child_process` | `platform/jsrt/nodemods` | 只有 `execFile`/`execFileSync`;经 shell seam、无 shell;`cwd` 限 `/workspace` |
| `node:http` / `node:https` | `platform/jsrt/nodemods` | 同步客户端:`request`/`get`/`ClientRequest`/`IncomingMessage`;无 `createServer` |
| `node:dns` / `node:dns/promises` | `platform/jsrt/nodemods` | 同步解析:`lookup`/`lookupSync`/`resolve*`/`reverse`;`setServers` 不支持 |
| `node:net` | `platform/jsrt/nodemods` | `isIP` 系列、`SocketAddress`、同步 TCP `Socket`(含 `read(n)` 扩展);无 `createServer` |
| `ada:fs` / `ada:shell` | `platform/bridge` | **旧接口,保留兼容**;不经过 Node 运行时 |

`node:*` 与裸名(`fs`、`path`、`os`、`events`、`buffer` ...)解析到同一份模块实例。

## 3. `node:fs` 的根与策略

`node:fs` 不是 `fs` seam 的装饰器链;它由 moejs 的 `node` 包直接持有工作区根,
路径夹在 `Root` 内,符号链接与 `..` 由 `os.Root` 处理。

因此桥接层只在**没有 fs/workspace 策略装饰器**时安装 `node:fs`:

```text
fs Chain.Applied() == []        且 workspace Chain.Applied() == []
  → NodeFS{Root: <工作区根>, Mount: "/workspace", Write: true}
否则
  → 只装 node:path / node:os / node:events / node:buffer 等
  → import "node:fs" 明确失败("this runtime has no file system")
```

这样 `fs-sandbox`、`caps-gate`、`workspace-guard` 不会被 Node 路径绕过。
需要策略保护的脚本继续使用 `ada:fs`(走 fs Chain),或等桥接层补上
policy-aware 的 Node fs 适配。

已知边界:

- `node:fs` 写入不经过 `fs-local` 的版本守卫/原子写;
- 没有 fs 策略时它直接写工作区根,仍由 `os.Root` 限制在根内;
- `node:fs` 只挂 `/workspace`;`os.tmpdir()` 返回 `/workspace/.ada-tmp`,
  安装时会创建该目录;
- `node:fs` 的 `writeFile` 等成员没有 `fs-redact`/`fs-observe` 事件;
- 当前 `jsrt` 的插件入口是同步调用:异步 fs 的 promise 形存在,但不要在
  `apply` 里依赖它在返回前完成;需要异步文件 IO 的脚本应使用工具/事件回调,
  或等事件循环切片。

## 4. 宿主信息

`node:os` 读取 `globalThis.__adaOSInfo`:

| 字段 | 来源 |
|---|---|
| `platform` / `arch` / `type` | 宿主 `GOOS` / `GOARCH` |
| `cpus` | `runtime.NumCPU()` |
| `eol` | Windows `\r\n`,其他 `\n` |
| `hostname` / `homedir` | 固定空串 |
| `tmpdir` | 固定 `/workspace/.ada-tmp`(与 node:fs 的挂载一致) |

`ada:shell.platform()` 仍读取 `__adaOS` 字符串;两个全局分开,避免互相覆盖。

## 5. 网络模块:只受同步运行时约束,不受网络策略限制

决策见 [ADR 0011](adr/0011-node-network-direct.md) 与 [ADR 0012](adr/0012-remove-seam-net.md):
安全边界只约束**文件与写入**;网络请求不改变本地文件,因此 `node:http`/`https`/`dns`/`net`
**不经过**任何受控 seam,也不受 `caps` 约束。仓库里原有的 `seam/net` 与 `net-guard`
已随 ADR 0012 移除,网络只有这一条路径。

保留下来的限制都是运行时能力,不是策略:

- 只支持 `http`/`https`;其他协议在宿主侧拒绝;
- 请求体与响应体上限 4 MiB(`maxNodeHTTPBody`),超出时 `res.truncated === true`;
- 请求在 `req.end()`(或 `http.get()`)时**立即完成**:没有事件循环,
  `response`/`data`/`end` 回调按注册顺序同步触发;
- `http` 不跟随重定向,3xx 原样返回(与 Node 一致);
- `node:net` 的 `connect()` 同步建连,读数据用扩展方法 `socket.read(n)`;
  `createServer`/`listen` 不提供(接收连接必须阻塞等待);
- `dns.setServers()` 抛 `ERR_NOT_SUPPORTED`(宿主只用系统解析器);
- 失败一律可见:`http.get` 连不上会抛 `code = "EHTTP"` 的错误,
  只有显式注册 `error` 监听器时才改为事件。

典型用法(回调在 `get` 返回前就已触发,不需要事件循环):

```ts
import http from "node:http"

let body = ""
http.get("https://example.com/api", (res) => {
  res.setEncoding("utf8")
  res.on("data", (chunk) => { body += chunk })
  res.on("end", () => { ctx.log("完成", body.length) })
})
```

## 5.1 未实现的模块

以下模块暂不安装,按 Node 的方式在链接期失败,而不是返回近似值:

- `node:zlib`/`node:stream/web`/`node:timers`;
- `node:module`/`node:worker_threads`/`node:vm`;
- `node:tls`(TLS 只在 `node:https` 内部使用,不暴露原始握手)。

## 5.2 `node:child_process` 的边界

`node:child_process` 的边界:

- 只有 `execFile`/`execFileSync`,参数数组直接交给 shell seam,不经过 shell 解析;
- `spawn`/`fork`/`exec`/`execSync` 存在但调用即抛 `ERR_NOT_SUPPORTED`,不是可绕过;
- `cwd` 必须在 `/workspace` 下;`options.shell` 与 `options.env` 拒绝;
- 命令超时、取消、沙箱与审计由 shell 实现/策略负责。

## 6. 测试

```powershell
go test ./platform/bridge -run 'NodeBuiltins|NodeHTTP|NodeChild|JSShell|JSTool'
go test ./platform/jsrt
```

`TestNodeBuiltins` 覆盖:

- `node:fs` 读写与 `existsSync`;
- `node:fs/promises` 的导出形状;
- `node:crypto` 的 `sha256`(hex/base64)、`randomBytes` 长度与 v4 UUID 形状;
- `node:path.join`;
- `node:os.platform`;
- `node:buffer` 的 hex 编码;
- `node:events` 的 `on`/`emit`;
- `node:util.format`;
- `node:assert.strictEqual`;
- `node:querystring.parse`、`node:string_decoder`、`node:url.fileURLToPath`;
- `node:process.cwd()` 与 `node:process.platform`;
- `TestNodeChildProcess`:`execFileSync` 经 shell seam 执行,`spawn` 显式失败;
- `TestNodeHTTPDNSNet`:本地 HTTP 服务上跑 `http.get`(含响应头/响应体)与
  `http.request`+`write`/`end`(含请求体);`dns.lookupSync`/`lookup` 解析 localhost;
  `net.isIP` 判定与同步 TCP 读写;`createServer` 显式拒绝;
- `TestNodeHTTPError`:连不上时抛 `EHTTP`,不静默返回空响应;
- `TestNodeFSDisabledByPolicy`:fs 链有 `fs-sandbox` 时 `node:fs` 必须不可用。
