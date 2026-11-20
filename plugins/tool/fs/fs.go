// Package toolfs 是 `tool/fs`:把 fs seam 的读取能力暴露成模型工具。
//
// 它只负责参数到 seam 调用的映射;路径限制、只读/沙箱由 fs 实现与策略负责。
package toolfs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/adag/ada-go/v2/platform/kernel"
	"github.com/adag/ada-go/v2/platform/kernel/schema"
	"github.com/adag/ada-go/v2/plugins/seam/fs"
	"github.com/adag/ada-go/v2/plugins/seam/llm"
	"github.com/adag/ada-go/v2/plugins/seam/tools"
)

// PluginName 是插件与工具来源 id。
const PluginName = "tool-fs"

const readTool = "read_file"

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
		return registry.Register(def)
	}).WithInject("tools", "fs")
}
