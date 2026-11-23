# 编译与发布打包

> 脚本:[`scripts/package.ps1`](../scripts/package.ps1)、[`scripts/build.ps1`](../scripts/build.ps1)、
> 内置清单导出工具:[`scripts/tools/builtins-dump`](../scripts/tools/builtins-dump/main.go)。
> 目标:发布包里的 `plugins/` 与二进制内置插件**同源**,preset 不引用源码开发目录。

## 1. 为什么需要单独的打包脚本

源码开发时,`examples/agent/plugins`、`examples/workspace/plugins` 是手写清单;
发布二进制里的 Go 插件集合由 [`platform/host/builtins`](../platform/host/builtins/builtins.go) 决定。
两者靠人手同步,必然出现「二进制里有插件、发布目录里没有清单」或「清单有、二进制没编译」的偏差。

`package.ps1` 的做法:

1. 编译全部入口到 `dist/<name>/bin/`;
2. 运行 `builtins-dump`,**从编译进宿主的 `builtins.Plugins()` 枚举所有 Go 插件**,
   用 `kernel.Plugin` 的 `Provide()`/`Inject()` 自动生成 `plugins/<id>/plugin.json`;
3. 生成只引用包内 `../plugins`、`workspace/`、`data/` 的 preset;
4. 用打包后的二进制执行 `ada plan` 自检(同平台时);
5. 打 zip。

因此发布清单不会随 `examples/` 的手写文件漂移。

## 2. 用法

```powershell
pwsh scripts/package.ps1                                  # 当前平台,版本 dev-<commit>
pwsh scripts/package.ps1 -Version 0.1.0                   # 指定版本
pwsh scripts/package.ps1 -Version 0.1.0 -GOOS linux -GOARCH amd64
pwsh scripts/package.ps1 -SkipBuild -NoZip                 # 复用已有 bin,只出目录
pwsh scripts/package.ps1 -StripDebug                       # 发布 strip
pwsh scripts/package.ps1 -LLMEndpoint http://127.0.0.1:3050/v1 -LLMModel tokenharbor/deepseek-v4.1-flash:free
```

`-LLMEndpoint` + `-LLMModel` 同时给出时,额外生成
`presets/agent-real-release.preset.json`;否则只生成回放 preset。

## 3. 包结构

```text
dist/ada-<version>-<goos>-<goarch>/
  bin/
    ada[.exe]
    ada-rpc-agent[.exe]
    ada-demo[.exe]
    ada-ts-demo[.exe]
  plugins/
    <id>/plugin.json            # 由 builtins 自动导出的全部 Go 内置插件
  presets/
    agent-release.preset.json
    workspace-release.preset.json
    agent-real-release.preset.json   # 仅当给了 LLMEndpoint/LLMModel
  data/
    agent-replay.jsonl
    workspace-replay.jsonl
  workspace/
    a.txt
  docs/
    a.txt                       # 只读示例工作区
  package.json                  # version/commit/goos/goarch/plugins/binaries
  README.txt
```

在包根目录运行:

```powershell
bin/ada.exe plan  --preset presets/agent-release.preset.json
bin/ada.exe agent --preset presets/agent-release.preset.json -message "读取 a.txt 并总结"
bin/ada.exe agent --preset presets/workspace-release.preset.json --workspace docs -message "查询工作区并读取 a.txt"
```

## 4. preset 的 allowlist

`plugins/` 包含全部 52 个 Go 内置插件,但发布 preset 只启用栈内插件,其余全部写入 `disable`:

- `agent-release`: agent-loop、session-memory/stats、system-prompt、seam-llm + llm-replay、
  seam-fs + fs-local、seam-tools + tool-fs、compaction-basic;
- `workspace-release`: 再增加 seam/workspace + workspace-local + workspace-guard + tool-workspace;
- `agent-real-release`(可选): 用 `llm-openai` 替换 `llm-replay`。

未启用的插件仍以清单形式随包发布,方便后续在 preset 中启用;启用时必须补上它自己的配置。

## 5. 自检

同平台打包时,脚本会进入包目录执行:

```powershell
bin/ada plan --preset presets/agent-release.preset.json
bin/ada plan --preset presets/workspace-release.preset.json
```

任一失败即中止打包。交叉编译到其它 GOOS/GOARCH 时跳过运行自检,但仍校验二进制与清单存在。

## 6. 边界

1. **只打包 Go 内置插件**:`builtins-dump` 不包含 TS 脚本插件;需要脚本插件时在发布项目里
   单独提供源码/Artifact 与清单。
2. **preset 路径依赖包根目录**:`llm-replay`、`fs-local.workspace` 是相对进程 cwd 的配置,
   必须在包根目录运行;`ada-rpc-agent` 由宿主设置 cwd/环境变量。
3. **没有真实端点自动探测**:真实 preset 的 endpoint/model 只来自参数,不在脚本里猜。
4. **zip 只是本地产物**:未做签名/公证/安装器;发布签名仍走 `install` 的密钥体系。
5. **交叉编译不能在本机执行自检**:这是有意跳过,不代表包已验证目标平台的运行时行为。
