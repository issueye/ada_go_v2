package agentloop_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adag/ada-go/v2/platform/host/builtins"
	"github.com/adag/ada-go/v2/platform/kernel"
	agentloop "github.com/adag/ada-go/v2/plugins/core/agent-loop"
	replay "github.com/adag/ada-go/v2/plugins/provider/llm-replay"
	"github.com/adag/ada-go/v2/plugins/seam/llm"
	"github.com/adag/ada-go/v2/plugins/seam/session"
	"github.com/adag/ada-go/v2/plugins/seam/tools"
)

func TestTurnWithReplayAndTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := kernel.New(kernel.Options{})
	defer func() { _ = app.Stop(context.Background()) }()
	mount := func(id string, cfg map[string]any) {
		t.Helper()
		mk, ok := builtins.Plugins()[id]
		if !ok {
			t.Fatalf("内置插件缺失: %s", id)
		}
		f, err := app.Plugin(mk(), kernel.NewConfig(cfg))
		if err != nil {
			t.Fatalf("挂载 %s 失败: %v", id, err)
		}
		if err := f.Ready(ctx); err != nil {
			t.Fatalf("等待 %s 就绪失败: %v", id, err)
		}
	}

	steps := []replay.Step{
		{Response: llm.Response{
			Message:   llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Kind: llm.BlockText, Text: "我先读文件"}}},
			ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "read_file", Args: json.RawMessage(`{"path":"a.txt"}`)}},
			Finish:    llm.FinishToolCalls,
			Usage:     llm.Usage{InputTokens: 12, OutputTokens: 4},
		}},
		{Response: llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Kind: llm.BlockText, Text: "文件内容是 hello"}}},
			Finish:  llm.FinishStop,
			Usage:   llm.Usage{InputTokens: 20, OutputTokens: 6},
		}},
	}
	model := []llm.ModelInfo{{ID: "replay", ContextWindow: 8192, MaxOutput: 1024}}

	mount("seam-session", nil)
	mount("session-memory", nil)
	mount("seam-prompts", map[string]any{"budget": 4096})
	mount("system-prompt", map[string]any{"identity": "你是测试助手", "rules": []string{"只读工作区"}})
	mount("seam-llm", nil)
	mount("llm-replay", map[string]any{"steps": steps, "models": model, "defaultModel": "replay"})
	mount("seam-tools", nil)
	mount("seam-fs", nil)
	mount("fs-local", map[string]any{"workspace": workspace})
	mount("tool-fs", nil)
	mount("agent-loop", map[string]any{"model": "replay", "maxSteps": 4})

	raw, err := app.Root().Service("agent")
	if err != nil {
		t.Fatalf("取 agent 服务失败: %v", err)
	}
	agent, ok := raw.(*agentloop.Agent)
	if !ok {
		t.Fatalf("agent 服务类型不对: %T", raw)
	}

	sess, err := agent.Open(ctx, session.Meta{Title: "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(ctx, sess, "读取 a.txt 并总结")
	if err != nil {
		t.Fatalf("跑一轮失败: %v", err)
	}
	if res.Text != "文件内容是 hello" || res.Steps != 2 || res.ToolCalls != 1 {
		t.Fatalf("结果不对: %+v", res)
	}

	evs, err := sess.Events(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		session.TypeTurnStart, session.TypeUserMessage, session.TypeStepStart,
		session.TypeAssistantMessage, session.TypeToolCall, session.TypeToolResult,
		session.TypeStepEnd, session.TypeStepStart, session.TypeAssistantMessage,
		session.TypeStepEnd, session.TypeTurnEnd,
	}
	if len(evs) != len(want) {
		t.Fatalf("事件数 %d,期望 %d", len(evs), len(want))
	}
	for i, typ := range want {
		if evs[i].Type != typ {
			t.Fatalf("第 %d 条事件 %s,期望 %s", i+1, evs[i].Type, typ)
		}
	}

	var toolPayload struct {
		Call   tools.CallInfo `json:"call"`
		Result tools.Result   `json:"result"`
	}
	if err := json.Unmarshal(evs[5].Data, &toolPayload); err != nil {
		t.Fatal(err)
	}
	if toolPayload.Result.Status != tools.StatusOK || len(toolPayload.Result.Content) == 0 || toolPayload.Result.Content[0].Text != "hello" {
		t.Fatalf("工具结果不对: %+v", toolPayload.Result)
	}
}
