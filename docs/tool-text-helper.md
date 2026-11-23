# 脚本插件:text-helper(TS + 注入 ada:fs)

> 示例插件:脚本实现文本读写/编辑/搜索/列目录;标准 `plugins/tool/fs` 也已是 TS 实现。

## 1. 目的

| 层 | 实现 |
|---|---|
| 标准工具 | `plugins/tool/fs`(TS)提供 `read_file` / `write_file` / `edit_file` / `search_files` / `list_dir` |
| 脚本示例 | `examples/text-ts/plugins/text-helper` 通过注入模块 `ada:fs` 提供同类工具 |

脚本插件只做参数校验与结果格式化;实际读写/搜索走 `seam/fs`。

## 2. 文件

```
examples/text-ts/
  plugins/text-helper/plugin.ts     TS 插件
  plugins/text-helper/plugin.json   kind=ts,inject: tools/fs
  text.preset.json                  agent 栈 + text-helper
  replay.jsonl                      write → edit+search 回放
  workspace/                        示例工作区
```

## 3. 工具

| 工具 | 行为 |
|---|---|
| `text_read` | 读取文本 |
| `text_write` | 原子写入(自动建目录) |
| `text_edit` | 精确替换(read-before-edit) |
| `text_search` | 搜索文本,返回 `path:line: text` |
| `text_list` | 列目录(可递归) |

## 4. 注入模块

`ada:fs`:

```js
export function read(path) { return host.fs.read(path) }
export function write(path, content, createDirs) { return host.fs.write(path, content, createDirs) }
export function edit(path, old, newText, all) { return host.fs.edit(path, old, newText, all) }
export function search(query, path, limit) { return JSON.parse(host.fs.search(query, path || "/workspace", limit || 0)) }
export function list(path, recursive, limit) { return JSON.parse(host.fs.list(path || "/workspace", recursive, limit || 0)) }
```

对应 `jsrt.Host.FSRead/FSWrite/FSEdit/FSSearch/FSList`;搜索/列目录返回 JSON 数组。

## 5. 运行

```powershell
go run ./cmd/ada agent --preset examples/text-ts/text.preset.json -message "写文件、替换并搜索"
```

真实端点:切到 `llm-openai` 并设置 `ADA_LLM_API_KEY`;已手工验证 write→edit 成功。

## 6. 测试

| 场景 | 用例 |
|---|---|
| 标准 TS 工具全链路 | `platform/bridge.TestJSToolFS`(读/写/编辑/搜索/列目录) |
| 脚本装配 | `platform/host/cli.TestUpTextScriptPlugin` |
| 脚本写/编辑 | `platform/host/cli.TestAgentTextScriptPlugin` |
| fs-local Search | Go 工具测试覆盖(真实文件搜索) |

## 7. 已知边界

1. **搜索是字面量匹配、大小写敏感**,不是正则;只搜文本文件,单文件 ≤1 MiB。
2. **编辑是 read-before-edit**:并发改同一文件会冲突(`ECONFLICT`)。
3. **脚本路径只做词法前缀**:真实路径/越界由 fs-local 与策略负责。
4. **搜索/列目录返回 JSON**,脚本再格式化;大数据量受 limit 限制。
5. **无 SDK 类型**:`ctx.tool`/`ada:fs` 类型仍由示例声明。
6. **生产需挂 fs-sandbox/caps**:示例只挂 fs-local。
