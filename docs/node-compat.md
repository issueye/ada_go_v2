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
| `node:fs` | moejs `node` 包 + 宿主补齐层 | 根固定为 `/workspace`;同步与回调形状可用;`mkdtemp`/`symlink`/`link`/`chmod`/`chown`/`truncate`/`utimes` 由宿主补齐(§3.1,三形态) |
| `node:fs/promises` | moejs `node` 包 | 同上;真异步:promise 由事件循环驱动,`await` 之后文件已落盘 |
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
| `node:zlib` | `platform/jsrt/nodemods` | 同步 `gzipSync`/`gunzipSync`/`deflateSync`/`inflateSync`/`deflateRawSync`/`inflateRawSync`/`unzipSync`;无流式接口与 brotli |
| `node:child_process` | `platform/jsrt/nodemods` | 只有 `execFile`/`execFileSync`;经 shell seam、无 shell;`cwd` 限 `/workspace` |
| `node:http` / `node:https` | `platform/jsrt/nodemods` | 同步客户端:`request`/`get`/`ClientRequest`/`IncomingMessage`;无 `createServer` |
| `node:dns` / `node:dns/promises` | `platform/jsrt/nodemods` | 同步解析:`lookup`/`lookupSync`/`resolve*`/`reverse`;`setServers` 不支持 |
| `node:net` | `platform/jsrt/nodemods` | `isIP` 系列、`SocketAddress`、同步 TCP `Socket`(含 `read(n)` 扩展);无 `createServer` |
| `node:timers` | `platform/jsrt/nodemods` | 全局定时器的同一组函数(`timers.setTimeout === setTimeout`),调用内生效 |
| `node:timers/promises` | `platform/jsrt/nodemods` | `setTimeout`/`setImmediate`/`scheduler.wait`;`setInterval` 是异步可迭代对象 |
| `node:stream` | `platform/jsrt/nodemods` | `Readable`/`Writable`/`Duplex`/`Transform`/`PassThrough`、`pipe`、`pipeline`、`finished`、`Readable.from`、`for await` |
| `node:stream/promises` | `platform/jsrt/nodemods` | `pipeline`/`finished` 的 Promise 形态 |
| `node:readline` / `readline/promises` | `platform/jsrt/nodemods` | 非 terminal 模式:按行切分 + `line`/`close` 事件 + 异步迭代 + `question`;不渲染 prompt |
| `node:tty` | `platform/jsrt/nodemods` | `isatty()` 恒 false;`ReadStream`/`WriteStream` 构造即抛 `ERR_NOT_SUPPORTED`(没有可包的 fd) |
| `node:perf_hooks` | `platform/jsrt/nodemods` | `performance.now`(宿主单调时钟)/`timeOrigin`/`mark`/`measure`/`getEntries*`/`clear*` + `PerformanceObserver`(微任务投递) |
| `ada:fs` / `ada:shell` | `platform/bridge` | **旧接口,保留兼容**;不经过 Node 运行时 |
| `ada:secrets` | `platform/bridge` | `get(name)` 按名读 secret、`has(name)` 只回答能否读到;走 `seam/secrets` 链(provider + policy),key 不进配置/日志 |

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
- `node:fs` 的异步形状由事件循环驱动([event-loop.md](event-loop.md)):`await` 之后
  操作已经完成;但**调用结束后**仍在飞的 promise 不会被等到——要么 await,要么用工具/事件回调。

### 3.1 `node:fs` 的补齐 API

moejs 的 `node` 包只实现了一部分 `node:fs`;常用但缺失的元数据类操作由宿主补齐
(`platform/jsrt/nodefsx.go` 做操作,`platform/jsrt/fsx.js` 做形态):

| API | 形态 | 说明 |
|---|---|---|
| `mkdtemp` | `mkdtempSync` / `mkdtemp(prefix, cb)` / `promises.mkdtemp` | 在 `/workspace` 下建随机后缀目录,返回虚拟路径;连续 10 次撞名才报 `EEXIST` |
| `symlink` | 同上三形态 | 目标**必须是相对路径**(`os.Root` 不允许绝对目标,越界目标直接 `EPERM`) |
| `link` | 同上三形态 | 硬链接 |
| `chmod` | 同上三形态 | 权限位;Windows 上受限(与 Node 一致) |
| `chown` | 同上三形态 | Windows 上返回 `ENOTSUP`(平台没有 POSIX 属主) |
| `truncate` | 同上三形态 | 经由 `OpenFile(O_WRONLY)` + `Truncate`,`len < 0` 报 `EINVAL` |
| `utimes` | 同上三形态 | 接受秒数或 `Date`,`atime`/`mtime` |

边界:

- 路径同样先夹在 `/workspace` 里:`/etc/x`、`/workspace/../x` 一律 `EPERM`;
- 写权限沿用 `node:fs` 的 `Write` 开关:只读工作区返回 `EROFS`;
- 错误是 Node 形状:带 `code`(`ENOENT`/`EEXIST`/`EACCES`/`EINVAL`/`EROFS`/`EPERM`/`ENOTSUP`/`EIO`)
  与 `syscall` 字段,而不是一句字符串;
- 与 moejs 的实现一样,**这些写入不经过 `fs-local` 的原子写/版本守卫,也不产生
  `fs-redact`/`fs-observe` 事件**;要策略保护请用 `ada:fs`(走 fs Chain);
- 补齐只覆盖「一次操作」,不做文件描述符(`open`/`read`/`write`/`close`)与流
  (`createReadStream`/`createWriteStream`)——那两者需要跨调用的句柄状态,属于后续。

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
- 请求在 `req.end()`(或 `http.get()`)时**立即完成**:这个模块本身是同步实现
  (运行时已有事件循环,但 http 的状态机不挂在它上面),
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

以下模块不安装,按 Node 的方式在链接期失败(`No such built-in module: ...`),
而不是返回近似值。分三类:

**引擎没有对应机制(已写 ADR,见 [ADR 0015](adr/0015-refused-node-modules.md))**

- `node:worker_threads`:没有第二个 isolate,假 Worker 会在同一个堆上跑;
- `node:inspector`:moejs 不暴露 V8 Inspector 会话;
- `node:async_hooks`:引擎没有 async hook 钩子,`AsyncLocalStorage` 会静默丢上下文;
- `node:trace_events`:没有 V8 trace buffer。

替代路径:并行工作用 `platform/sched` 与 Go 插件;调试用 `ctx.log` + `ada inspect`;
请求上下文隔离用调用点 ctx;追踪用 `policy/audit-log` + `node:perf_hooks`。

**需要宿主能力,尚未做**

- `node:tls`(TLS 只在 `node:https` 内部使用,不暴露原始握手);
- `node:dgram`(UDP)、`node:http2`、`node:cluster`;
- `node:module`、`node:vm`(`vm` 需要引擎支持隔离执行);
- `node:wasi`、`node:v8`。

**已废弃或没有意义**

- `node:domain`、`node:punycode`、`node:constants`、`node:console`
  (日志走 `ctx.log`;裸 Runtime 没有 console,见 C4)。

`node:zlib` 只做同步的一次性压缩与解压:`createGzip`/`createDeflate` 这类流式接口、
brotli 与 zip 归档都不实现。解压输出上限 8 MiB(`maxZlibOutBytes`),压缩比可以做到
上万倍,不设上限等于把 zip bomb 交给插件;输入上限同为 8 MiB。非法容器(gzip 头不对、
zlib 校验失败)一律抛错,不返回近似结果。

`node:stream/web`(`ReadableStream`/`WritableStream`)也不安装:它属于 WHATWG 流,
与 `node:stream` 是两套 API;需要时用 `node:stream`。

## 5.2 `node:child_process` 的边界

`node:child_process` 的边界:

- 只有 `execFile`/`execFileSync`,参数数组直接交给 shell seam,不经过 shell 解析;
- `spawn`/`fork`/`exec`/`execSync` 存在但调用即抛 `ERR_NOT_SUPPORTED`,不是可绕过;
- `cwd` 必须在 `/workspace` 下;`options.shell` 与 `options.env` 拒绝;
- 命令超时、取消、沙箱与审计由 shell 实现/策略负责。

## 6. 测试

```powershell
go test ./platform/bridge -run 'NodeBuiltins|NodeHTTP|NodeZlib|NodeChild|JSShell|JSTool'
go test ./platform/jsrt
go test ./platform/jsrt -run 'Await|Timer|Loop'   # 事件循环(见 event-loop.md)
go test ./platform/jsrt -run 'NodeFSExt'          # node:fs 补齐 API 的三形态与错误码
go test ./platform/jsrt -run 'TestNodeTimers|TestNodeStream|TestNodeReadline|TestNodePerfHooks|TestNodeTty'
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
- `TestNodeZlib`:`node:zlib` 的 gzip/deflate/raw 往返与 `unzipSync`,
  且 JS 产出的 gzip 能被 Go 的 `compress/gzip` 解开;
- `TestNodeChildProcess`:`execFileSync` 经 shell seam 执行,`spawn` 显式失败;
- `TestNodeHTTPDNSNet`:本地 HTTP 服务上跑 `http.get`(含响应头/响应体)与
  `http.request`+`write`/`end`(含请求体);`dns.lookupSync`/`lookup` 解析 localhost;
  `net.isIP` 判定与同步 TCP 读写;`createServer` 显式拒绝;
- `TestNodeHTTPError`:连不上时抛 `EHTTP`,不静默返回空响应;
- `TestNodeFSDisabledByPolicy`:fs 链有 `fs-sandbox` 时 `node:fs` 必须不可用。
