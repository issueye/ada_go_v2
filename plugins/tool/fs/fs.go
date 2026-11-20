// Package toolfs 是 `tool/fs`:把 fs seam 的读写/编辑/搜索/列目录暴露成模型工具。
//
// 它只负责参数到 seam 调用的映射;路径限制、只读/沙箱由 fs 实现与策略负责。
package toolfs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/issueye/ada_go_v2/platform/kernel"
	"github.com/issueye/ada_go_v2/platform/kernel/schema"
	"github.com/issueye/ada_go_v2/plugins/seam/fs"
	"github.com/issueye/ada_go_v2/plugins/seam/llm"
	"github.com/issueye/ada_go_v2/plugins/seam/tools"
)

// PluginName 是插件与工具来源 id。
const PluginName = "tool-fs"

const (
	readTool   = "read_file"
	writeTool  = "write_file"
	editTool   = "edit_file"
	searchTool = "search_files"
	listTool   = "list_dir"
)

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type editArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
	All  bool   `json:"all,omitempty"`
}
type searchArgs struct {
	Query string `json:"query"`
	Path  string `json:"path,omitempty"`
	Limit int    `json:"limit,omitempty"`
}
type listArgs struct {
	Path      string `json:"path,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type readArgs struct {
	Path string `json:"path"`
}

// Plugin 返回 tool-fs 插件:inject fs,注册 read_file。
func Plugin() kernel.Plugin {
	return kernel.Func(PluginName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		registry, err := kernel.Service[*tools.Registry](ctx, "tools")
		if err != nil {
			return nil, err
		}
		chain, err := kernel.Service[*fs.Chain](ctx, "fs")
		if err != nil {
			return nil, err
		}
		def := tools.ToolDef{
			Name:     readTool,
			Summary:  "读取工作区内的文本文件",
			ReadOnly: true,
			Timeout:  30 * time.Second,
			Source:   PluginName,
			Input: schema.FromMap(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "相对 /workspace 的路径,或以 / 开头的虚拟路径"},
				},
				"required": []any{"path"},
			}),
			Handler: func(callCtx context.Context, call tools.CallInfo) (tools.Result, error) {
				var a readArgs
				if err := json.Unmarshal(call.Args, &a); err != nil {
					return tools.Result{Status: tools.StatusError, Content: []llm.Block{{Kind: llm.BlockText, Text: "参数解析失败: " + err.Error()}}}, nil
				}
				p := a.Path
				if !strings.HasPrefix(p, "/") {
					p = "/workspace/" + p
				}
				impl, err := chain.Get()
				if err != nil {
					return tools.Result{Status: tools.StatusError, Content: []llm.Block{{Kind: llm.BlockText, Text: err.Error()}}}, nil
				}
				res, err := impl.Read(callCtx, fs.ReadRequest{Path: fs.Path(p)})
				if err != nil {
					return tools.Result{Status: tools.StatusError, Content: []llm.Block{{Kind: llm.BlockText, Text: fmt.Sprintf("读取 %s 失败: %v", p, err)}}}, nil
				}
				return tools.Result{Status: tools.StatusOK, Content: []llm.Block{{Kind: llm.BlockText, Text: res.Content}}}, nil
			},
		}
		// 其余工具:写/编辑/搜索/列目录;统一走 fs seam 的 Write/Edit/Search/List。
		defs := []tools.ToolDef{def, {
			Name: writeTool, Summary: "写入工作区文本文件(原子写)", Timeout: 30 * time.Second, Source: PluginName,
			Input: schema.FromMap(map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"},
			}, "required": []any{"path", "content"}}),
			Handler: func(callCtx context.Context, call tools.CallInfo) (tools.Result, error) {
				var a writeArgs
				if err := json.Unmarshal(call.Args, &a); err != nil {
					return toolErr("参数解析失败: " + err.Error()), nil
				}
				impl, err := chain.Get()
				if err != nil {
					return toolErr(err.Error()), nil
				}
				res, err := impl.Write(callCtx, fs.WriteRequest{Path: fs.Path(resolvePath(a.Path)), Content: []byte(a.Content), CreateDirs: true})
				if err != nil {
					return toolErr(fmt.Sprintf("写入 %s 失败: %v", resolvePath(a.Path), err)), nil
				}
				return tools.Result{Status: tools.StatusOK, Content: []llm.Block{{Kind: llm.BlockText,
					Text: fmt.Sprintf("已写入 %s(%d bytes)", resolvePath(a.Path), res.Size)}}}, nil
			},
		}, {
			Name: editTool, Summary: "精确替换文本(read-before-edit)", Timeout: 30 * time.Second, Source: PluginName,
			Input: schema.FromMap(map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"}, "old": map[string]any{"type": "string"},
				"new": map[string]any{"type": "string"}, "all": map[string]any{"type": "boolean"},
			}, "required": []any{"path", "old", "new"}}),
			Handler: func(callCtx context.Context, call tools.CallInfo) (tools.Result, error) {
				var a editArgs
				if err := json.Unmarshal(call.Args, &a); err != nil {
					return toolErr("参数解析失败: " + err.Error()), nil
				}
				impl, err := chain.Get()
				if err != nil {
					return toolErr(err.Error()), nil
				}
				read, err := impl.Read(callCtx, fs.ReadRequest{Path: fs.Path(resolvePath(a.Path))})
				if err != nil {
					return toolErr(fmt.Sprintf("读取 %s 失败: %v", resolvePath(a.Path), err)), nil
				}
				res, err := impl.Edit(callCtx, fs.EditRequest{
					Path: fs.Path(resolvePath(a.Path)), IfVersion: read.Version,
					Old: a.Old, New: a.New, All: a.All,
				})
				if err != nil {
					return toolErr(fmt.Sprintf("编辑 %s 失败: %v", resolvePath(a.Path), err)), nil
				}
				return tools.Result{Status: tools.StatusOK, Content: []llm.Block{{Kind: llm.BlockText,
					Text: fmt.Sprintf("已替换 %d 处(%s)", res.Replacements, resolvePath(a.Path))}}}, nil
			},
		}, {
			Name: searchTool, Summary: "在工作区搜索文本", ReadOnly: true, Timeout: 30 * time.Second, Source: PluginName,
			Input: schema.FromMap(map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			}, "required": []any{"query"}}),
			Handler: func(callCtx context.Context, call tools.CallInfo) (tools.Result, error) {
				var a searchArgs
				if err := json.Unmarshal(call.Args, &a); err != nil {
					return toolErr("参数解析失败: " + err.Error()), nil
				}
				if strings.TrimSpace(a.Query) == "" {
					return toolErr("query 不能为空"), nil
				}
				path := resolvePath(a.Path)
				if a.Path == "" {
					path = "/workspace"
				}
				limit := clampLimit(a.Limit, 200, 1000)
				impl, err := chain.Get()
				if err != nil {
					return toolErr(err.Error()), nil
				}
				res, err := impl.Search(callCtx, fs.SearchRequest{Path: fs.Path(path), Query: a.Query, Limit: limit})
				if err != nil {
					return toolErr(fmt.Sprintf("搜索失败: %v", err)), nil
				}
				if len(res.Matches) == 0 {
					return tools.Result{Status: tools.StatusOK, Content: []llm.Block{{Kind: llm.BlockText, Text: "(无匹配)"}}}, nil
				}
				var b strings.Builder
				for _, m := range res.Matches {
					fmt.Fprintf(&b, "%s:%d: %s\n", m.Path, m.Line, m.Text)
				}
				return tools.Result{Status: tools.StatusOK, Content: []llm.Block{{Kind: llm.BlockText, Text: strings.TrimSpace(b.String())}}}, nil
			},
		}, {
			Name: listTool, Summary: "列出工作区目录", ReadOnly: true, Timeout: 30 * time.Second, Source: PluginName,
			Input: schema.FromMap(map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"}, "recursive": map[string]any{"type": "boolean"},
				"limit": map[string]any{"type": "integer"},
			}}),
			Handler: func(callCtx context.Context, call tools.CallInfo) (tools.Result, error) {
				var a listArgs
				if err := json.Unmarshal(call.Args, &a); err != nil {
					return toolErr("参数解析失败: " + err.Error()), nil
				}
				path := "/workspace"
				if a.Path != "" {
					path = resolvePath(a.Path)
				}
				impl, err := chain.Get()
				if err != nil {
					return toolErr(err.Error()), nil
				}
				entries, err := impl.List(callCtx, fs.ListRequest{Path: fs.Path(path), Recursive: a.Recursive, Limit: clampLimit(a.Limit, 200, 1000)})
				if err != nil {
					return toolErr(fmt.Sprintf("列目录失败: %v", err)), nil
				}
				if len(entries) == 0 {
					return tools.Result{Status: tools.StatusOK, Content: []llm.Block{{Kind: llm.BlockText, Text: "(空目录)"}}}, nil
				}
				var b strings.Builder
				for _, e := range entries {
					fmt.Fprintf(&b, "%s %s\n", e.Kind, e.Path)
				}
				return tools.Result{Status: tools.StatusOK, Content: []llm.Block{{Kind: llm.BlockText, Text: strings.TrimSpace(b.String())}}}, nil
			},
		}}
		var disposers []kernel.Dispose
		for _, d := range defs {
			disp, err := registry.Register(d)
			if err != nil {
				for _, prior := range disposers {
					_ = prior(context.Background())
				}
				return nil, err
			}
			disposers = append(disposers, disp)
		}
		return func(c context.Context) error {
			var first error
			for i := len(disposers) - 1; i >= 0; i-- {
				if err := disposers[i](c); err != nil && first == nil {
					first = err
				}
			}
			return first
		}, nil
	}).WithInject("tools", "fs")
}

func toolErr(msg string) tools.Result {
	return tools.Result{Status: tools.StatusError, Content: []llm.Block{{Kind: llm.BlockText, Text: msg}}}
}

func resolvePath(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/workspace/" + p
}

func clampLimit(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}
