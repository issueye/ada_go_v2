// 脚本插件示例:text-helper。
//
// 文本读写/编辑/搜索/列目录由注入模块 ada:fs 提供;插件只做校验与格式化。
import { read, write, edit, search, list } from "ada:fs"

type ToolSpec = { name: string; summary?: string; input?: Record<string, unknown>; readOnly?: boolean }
type Ctx = {
  log: (msg: string) => void
  tool: (spec: ToolSpec, handler: (args: any) => any) => void
}

type Match = { Path: string; Line: number; Text: string }
type Entry = { Path: string; Name: string; Kind: string; Size: number }

function asPath(value: unknown): string {
  if (typeof value !== "string" || value.trim() === "") throw new Error("path 不能为空")
  const p = value.trim()
  if (p.includes("\u0000")) throw new Error("path 非法")
  return p.startsWith("/") ? p : "/workspace/" + p
}

export function apply(ctx: Ctx): void {
  ctx.log("text-helper-ts 装配完成")

  ctx.tool(
    {
      name: "text_read",
      summary: "读取工作区文本文件",
      readOnly: true,
      input: { type: "object", required: ["path"], properties: { path: { type: "string" } } },
    },
    (args: any) => read(asPath(args?.path)),
  )

  ctx.tool(
    {
      name: "text_write",
      summary: "写入工作区文本文件",
      input: {
        type: "object",
        required: ["path", "content"],
        properties: { path: { type: "string" }, content: { type: "string" } },
      },
    },
    (args: any) => {
      const path = asPath(args?.path)
      if (typeof args?.content !== "string") throw new Error("content 必须是字符串")
      write(path, args.content, true)
      return "已写入 " + path
    },
  )

  ctx.tool(
    {
      name: "text_edit",
      summary: "精确替换文本",
      input: {
        type: "object",
        required: ["path", "old", "new"],
        properties: {
          path: { type: "string" }, old: { type: "string" }, new: { type: "string" },
          all: { type: "boolean", description: "是否替换全部" },
        },
      },
    },
    (args: any) => {
      const path = asPath(args?.path)
      if (typeof args?.old !== "string" || args.old === "") throw new Error("old 不能为空")
      if (typeof args?.new !== "string") throw new Error("new 必须是字符串")
      const n = edit(path, args.old, args.new, args.all === true)
      return `已替换 ${n} 处(${path})`
    },
  )

  ctx.tool(
    {
      name: "text_search",
      summary: "搜索工作区文本",
      readOnly: true,
      input: {
        type: "object",
        required: ["query"],
        properties: {
          query: { type: "string" }, path: { type: "string" }, limit: { type: "integer" },
        },
      },
    },
    (args: any) => {
      const query = typeof args?.query === "string" ? args.query : ""
      if (query === "") throw new Error("query 不能为空")
      const path = typeof args?.path === "string" && args.path !== "" ? asPath(args.path) : undefined
      const limit = Number(args?.limit)
      const matches = search(query, path, Number.isFinite(limit) ? limit : 0) as Match[]
      if (matches.length === 0) return "(无匹配)"
      return matches.map((m) => `${m.Path}:${m.Line}: ${m.Text}`).join("\n")
    },
  )

  ctx.tool(
    {
      name: "text_list",
      summary: "列出工作区目录",
      readOnly: true,
      input: {
        type: "object",
        properties: { path: { type: "string" }, recursive: { type: "boolean" }, limit: { type: "integer" } },
      },
    },
    (args: any) => {
      const path = typeof args?.path === "string" && args.path !== "" ? asPath(args.path) : undefined
      const recursive = args?.recursive === true
      const limit = Number(args?.limit)
      const entries = list(path, recursive, Number.isFinite(limit) ? limit : 0) as Entry[]
      if (entries.length === 0) return "(空目录)"
      return entries.map((e) => `${e.Kind} ${e.Path}`).join("\n")
    },
  )
}