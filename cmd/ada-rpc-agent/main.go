// Command ada-rpc-agent serves an assembled ada agent over seam RPC for a
// desktop host such as ada_chat: one process instance per bot, its working
// directory as the workspace root.
//
// This is the P4 host adapter prototype: the agent preset, workspace root,
// name and instruction come from the environment set by the host; the process
// cwd is the selected working directory.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/issueye/ada_go_v2/platform/assemble"
	"github.com/issueye/ada_go_v2/platform/host/builtins"
	"github.com/issueye/ada_go_v2/platform/kernel"
	"github.com/issueye/ada_go_v2/platform/loader"
	"github.com/issueye/ada_go_v2/platform/plugin"
	"github.com/issueye/ada_go_v2/platform/preset"
	agentloop "github.com/issueye/ada_go_v2/plugins/core/agent-loop"
	openai "github.com/issueye/ada_go_v2/plugins/provider/llm-openai"
	rpcjson "github.com/issueye/ada_go_v2/plugins/provider/rpc-json"
	rpcnet "github.com/issueye/ada_go_v2/plugins/provider/rpc-net"
	rpcpipe "github.com/issueye/ada_go_v2/plugins/provider/rpc-pipe"
	seamllm "github.com/issueye/ada_go_v2/plugins/seam/llm"
	seamrpc "github.com/issueye/ada_go_v2/plugins/seam/rpc"
	seamsession "github.com/issueye/ada_go_v2/plugins/seam/session"
	seamtools "github.com/issueye/ada_go_v2/plugins/seam/tools"
)

const (
	envAddr        = "ADA_BOT_ADDR"
	envToken       = "ADA_BOT_TOKEN"
	envName        = "ADA_BOT_NAME"
	envModel       = "ADA_BOT_MODEL"
	envInstruction = "ADA_BOT_INSTRUCTION"
	envWorkspace   = "ADA_BOT_WORKSPACE"
	envPreset      = "ADA_GO_AGENT_PRESET"
	envPresetAlt   = "ADA_BOT_PRESET"
	envClient      = "ADA_BOT_CLIENT"
	envPresetBase  = "ADA_GO_AGENT_BASE"
	envLLMProvider = "ADA_LLM_PROVIDER"
	envLLMEndpoint = "ADA_LLM_ENDPOINT"
	envLLMModel    = "ADA_LLM_MODEL"
	envLLMAPIKey   = "ADA_LLM_API_KEY"
)

func main() {
	if err := run(); err != nil {
		slog.Error("ada-rpc-agent", "error", err)
		os.Exit(1)
	}
}

func run() error {
	addr, err := seamrpc.Parse(os.Getenv(envAddr))
	if err != nil {
		return fmt.Errorf("读取 %s: %w", envAddr, err)
	}
	token := []byte(os.Getenv(envToken))
	if len(token) == 0 {
		return fmt.Errorf("缺少 %s", envToken)
	}
	presetPath := firstNonEmpty(os.Getenv(envPreset), os.Getenv(envPresetAlt))
	if presetPath == "" {
		return fmt.Errorf("缺少 %s", envPreset)
	}
	if abs, err := filepath.Abs(presetPath); err == nil {
		presetPath = abs
	}
	name := firstNonEmpty(os.Getenv(envName), "机器人")
	model := os.Getenv(envModel)
	instruction := os.Getenv(envInstruction)
	client := firstNonEmpty(os.Getenv(envClient), "ada-chat")
	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("读取工作目录: %w", err)
	}
	if v := strings.TrimSpace(os.Getenv(envWorkspace)); v != "" {
		if abs, err := filepath.Abs(v); err == nil {
			workspace = abs
		}
	}

	// Presets often use paths relative to the ada_go_v2 module root (for example
	// llm-replay's replay.jsonl). Assemble in that base directory, then return
	// to the selected working directory before serving.
	base := firstNonEmpty(os.Getenv(envPresetBase), moduleRoot(filepath.Dir(presetPath)), filepath.Dir(presetPath))
	restoreCwd := false
	if base != "" && !samePath(base, workspace) {
		if err := os.Chdir(base); err != nil {
			return fmt.Errorf("进入 preset 基目录 %s: %w", base, err)
		}
		restoreCwd = true
	}

	override := llmOverride{
		Provider: strings.TrimSpace(os.Getenv(envLLMProvider)),
		Endpoint: strings.TrimSpace(os.Getenv(envLLMEndpoint)),
		Model:    strings.TrimSpace(os.Getenv(envLLMModel)),
	}
	manifests, err := loadManifests(presetPath, workspace, name, instruction, override)
	if err != nil {
		return err
	}
	actualModel := firstNonEmpty(presetModel(manifests), model)
	report := assemble.Plan(manifests)
	if !report.OK() {
		for _, p := range report.Problems {
			fmt.Fprintln(os.Stderr, "问题:", p.String())
		}
		return fmt.Errorf("agent preset 不可装配")
	}
	if err := builtins.ApplyAutoGrants(manifests); err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	app := kernel.New(kernel.Options{Logger: logger})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = app.Stop(ctx)
	}()
	lookup := loader.Lookup(loader.Options{
		Go: builtins.Plugins(),
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "  [js] %s\n", fmt.Sprintf(format, args...))
		},
	})
	if _, err := assemble.Up(app, manifests, report, lookup); err != nil {
		return fmt.Errorf("装配 agent: %w", err)
	}
	if restoreCwd {
		if err := os.Chdir(workspace); err != nil {
			return fmt.Errorf("回到工作目录 %s: %w", workspace, err)
		}
	}

	// The RPC host stack is mounted after the agent so any preset can be used.
	for _, p := range []kernel.Plugin{seamrpc.Plugin(), rpcnet.Plugin(), rpcpipe.Plugin(), rpcjson.Plugin()} {
		if _, err := app.Plugin(p, kernel.NewConfig(nil)); err != nil {
			return fmt.Errorf("挂载 rpc: %w", err)
		}
	}
	chain, err := kernel.Service[*seamrpc.Chain](app.Root(), "rpc")
	if err != nil {
		return err
	}
	svc, err := chain.Get()
	if err != nil {
		return err
	}
	raw, err := app.Root().Service("agent")
	if err != nil {
		return fmt.Errorf("没有 agent 服务: %w", err)
	}
	ag, ok := raw.(*agentloop.Agent)
	if !ok {
		return fmt.Errorf("agent 服务类型不正确: %T", raw)
	}
	ctx := context.Background()
	sessions := map[int]seamsession.Session{}
	open := func(title string) (seamsession.Session, error) {
		return ag.Open(ctx, seamsession.Meta{Title: title})
	}
	defaultSess, err := open(name)
	if err != nil {
		return fmt.Errorf("打开会话: %w", err)
	}

	server, err := svc.Listen(ctx, addr, seamrpc.ServerOptions{
		Auth:    seamrpc.Auth{Mode: seamrpc.AuthToken, Token: token},
		PeerIDs: []string{client},
	})
	if err != nil {
		return fmt.Errorf("监听 %s: %w", addr.String(), err)
	}
	defer func() { _ = server.Close() }()

	// 流式与工具事件:宿主需要边生成边显示,而不是等 Run 结束后回放。
	// 这些监听只做转发,不参与策略判定(事件不是门禁)。
	stream := newTurnStream()
	publish := func(topic string, payload map[string]any) {
		if err := server.Publish(topic, payload); err != nil {
			logger.Warn("发布事件失败", "topic", topic, "error", err)
		}
	}
	app.Root().OnWaterfall(seamllm.EventStream, func(_ context.Context, _ *kernel.Context, input any, next kernel.Next) (any, error) {
		v, err := next(input)
		if err != nil {
			return v, err
		}
		delta, ok := v.(*seamllm.Delta)
		if !ok || delta == nil {
			return v, nil
		}
		if turn := stream.current(); turn > 0 {
			switch delta.Kind {
			case seamllm.DeltaText:
				if delta.Text != "" {
					stream.sawText()
					publish("chat/delta", map[string]any{"turn": turn, "text": delta.Text})
				}
			case seamllm.DeltaReasoning:
				// 思考内容单独走一条事件，宿主可以把它显示成可折叠的“思考过程”。
				if delta.Text != "" {
					publish("chat/reasoning", map[string]any{"turn": turn, "text": delta.Text})
				}
			}
		}
		return v, nil
	}, kernel.WithTimeout(2*time.Second))
	app.Root().OnWaterfall(seamtools.EventPreExecute, func(_ context.Context, _ *kernel.Context, input any, next kernel.Next) (any, error) {
		if call, ok := input.(*seamtools.CallInfo); ok && call != nil {
			if turn := stream.current(); turn > 0 {
				stream.toolStarted(call.ID)
				publish("chat/tool", map[string]any{
					"turn": turn, "id": call.ID, "name": call.Name,
					"args": string(call.Args), "status": "running",
				})
			}
		}
		return next(input)
	}, kernel.WithTimeout(2*time.Second))
	app.Root().On(seamtools.EventResult, func(_ context.Context, _ *kernel.Context, args []any) (any, error) {
		if len(args) == 0 {
			return nil, nil
		}
		payload, ok := args[0].(map[string]any)
		if !ok {
			return nil, nil
		}
		call, callOK := payload["call"].(seamtools.CallInfo)
		result, resultOK := payload["result"].(seamtools.Result)
		if !callOK || !resultOK {
			return nil, nil
		}
		turn := stream.current()
		if turn <= 0 {
			return nil, nil
		}
		status := string(result.Status)
		if status == "" {
			status = string(seamtools.StatusOK)
		}
		publish("chat/tool", map[string]any{
			"turn": turn, "id": call.ID, "name": call.Name,
			"args": string(call.Args), "status": status,
			"text":       blockText(result.Content, 8<<10),
			"durationMs": stream.toolTook(call.ID).Milliseconds(),
		})
		return nil, nil
	})

	profile := map[string]any{
		"name": name, "model": actualModel, "workspace": workspace,
		"pid": os.Getpid(), "preset": presetPath, "instruction": instruction,
	}
	if _, err := server.Handle("bot.info", func(context.Context, json.RawMessage) (any, error) {
		return profile, nil
	}); err != nil {
		return err
	}
	var agentMu sync.Mutex
	if _, err := server.Handle("chat.send", func(chatCtx context.Context, params json.RawMessage) (any, error) {
		var in struct {
			Turn    int    `json:"turn"`
			Text    string `json:"text"`
			BotID   int    `json:"botId"`
			BotName string `json:"botName"`
		}
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, seamrpc.Invalid("chat.send 参数非法: " + err.Error())
		}
		if in.Turn <= 0 || strings.TrimSpace(in.Text) == "" {
			return nil, seamrpc.Invalid("chat.send 需要 turn 与 text")
		}
		agentMu.Lock()
		defer agentMu.Unlock()
		stream.begin(in.Turn)
		defer stream.end()
		sess := defaultSess
		if in.BotID > 0 {
			if s, ok := sessions[in.BotID]; ok {
				sess = s
			} else {
				title := firstNonEmpty(in.BotName, name)
				s, err := open(title)
				if err == nil {
					sessions[in.BotID] = s
					sess = s
				}
			}
		}
		res, err := ag.Run(chatCtx, sess, in.Text)
		if err != nil {
			if chatCtx.Err() != nil {
				return nil, chatCtx.Err()
			}
			logger.Error("agent 运行失败", "error", err)
			return nil, seamrpc.Internal("agent 运行失败")
		}
		// 模型没有给出文本增量时(例如非流式 provider),退回整段回放,
		// 宿主仍然能看到逐字出现的回答。
		if !stream.streamed() {
			if err := streamText(chatCtx, server, in.Turn, res.Text); err != nil {
				return nil, err
			}
		}
		_ = server.Publish("chat/done", map[string]int{"turn": in.Turn})
		return map[string]any{
			"turn": in.Turn, "text": res.Text, "steps": res.Steps, "toolCalls": res.ToolCalls,
		}, nil
	}); err != nil {
		return err
	}
	return server.Serve(ctx)
}

func streamText(ctx context.Context, server seamrpc.Server, turn int, text string) error {
	const chunk = 18
	r := []rune(text)
	for i := 0; i < len(r); i += chunk {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(6 * time.Millisecond):
		}
		end := i + chunk
		if end > len(r) {
			end = len(r)
		}
		if err := server.Publish("chat/delta", map[string]any{"turn": turn, "text": string(r[i:end])}); err != nil {
			return seamrpc.Internal("发布流式回答失败")
		}
	}
	return nil
}

// turnStream 记录当前 chat.send 的轮次与工具起始时间。
//
// 一个进程同一时刻只跑一轮(chat.send 持有 agentMu),所以这些监听可以
// 用同一个轮次号;工具由 agent-loop 并行执行,起始时间按调用 id 记。
type turnStream struct {
	mu     sync.Mutex
	turn   int
	texted bool
	starts map[string]time.Time
}

func newTurnStream() *turnStream {
	return &turnStream{starts: map[string]time.Time{}}
}

func (s *turnStream) begin(turn int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turn = turn
	s.texted = false
	s.starts = map[string]time.Time{}
}

func (s *turnStream) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turn = 0
	s.starts = map[string]time.Time{}
}

func (s *turnStream) current() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn
}

func (s *turnStream) sawText() {
	s.mu.Lock()
	s.texted = true
	s.mu.Unlock()
}

func (s *turnStream) streamed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.texted
}

func (s *turnStream) toolStarted(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	s.starts[id] = time.Now()
	s.mu.Unlock()
}

func (s *turnStream) toolTook(id string) time.Duration {
	if id == "" {
		return 0
	}
	s.mu.Lock()
	start, ok := s.starts[id]
	if ok {
		delete(s.starts, id)
	}
	s.mu.Unlock()
	if !ok {
		return 0
	}
	return time.Since(start)
}

// blockText 把工具结果的文本块拼成一段,并限制回传体积。
func blockText(blocks []seamllm.Block, limit int) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Kind != seamllm.BlockText || blk.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(blk.Text)
	}
	out := strings.TrimSpace(b.String())
	if limit > 0 && len(out) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "…"
	}
	return out
}

type llmOverride struct {
	Provider string
	Endpoint string
	Model    string
}

func loadManifests(presetPath, workspace, name, instruction string, ov llmOverride) ([]*plugin.Manifest, error) {
	p, err := preset.Load(presetPath)
	if err != nil {
		return nil, err
	}
	manifests, err := p.Expand()
	if err != nil {
		return nil, err
	}
	for _, m := range manifests {
		switch m.ID {
		case "fs-local":
			setConfig(m, "workspace", workspace)
			setConfig(m, "tmp", filepath.Join(workspace, ".ada-tmp"))
		case "shell-local":
			// 使用 workspace 服务时根由链解析,不要覆盖成宿主工作区;
			// 独立模式(shell 工具)则让命令在机器人工作区里执行。
			if enabledManifest(manifests, "workspace-local") == nil {
				setConfig(m, "workspace", workspace)
				setConfig(m, "tmp", filepath.Join(workspace, ".ada-tmp"))
			}
		case "system-prompt":
			identity := "你是 " + name + "，一个在本地工作目录里运行的 agent。"
			if strings.TrimSpace(instruction) != "" {
				identity += "\n" + instruction
			}
			setConfig(m, "identity", identity)
		}
	}
	if err := applyLLMOverride(manifests, ov); err != nil {
		return nil, err
	}
	return manifests, nil
}

// applyLLMOverride lets the desktop host choose the provider without editing
// the preset: it enables one LLM provider, disables the other, and points
// agent-loop at the chosen model.
func applyLLMOverride(manifests []*plugin.Manifest, ov llmOverride) error {
	provider := strings.ToLower(strings.TrimSpace(ov.Provider))
	if provider == "" {
		return nil
	}
	openaiM := findManifest(manifests, "llm-openai")
	replayM := findManifest(manifests, "llm-replay")
	switch provider {
	case "openai":
		if openaiM == nil {
			return fmt.Errorf("agent preset 不包含 llm-openai")
		}
		openaiM.Disabled = false
		if replayM != nil {
			replayM.Disabled = true
		}
		endpoint := strings.TrimSpace(ov.Endpoint)
		if endpoint == "" {
			endpoint = openai.DefaultEndpoint
		}
		model := strings.TrimSpace(ov.Model)
		if model == "" {
			model = openai.DefaultModel
		}
		setConfig(openaiM, "endpoint", endpoint)
		setConfig(openaiM, "model", model)
		setConfig(openaiM, "apiKeyEnv", envLLMAPIKey)
		setAgentModel(manifests, model)
	case "replay":
		if replayM == nil {
			return fmt.Errorf("agent preset 不包含 llm-replay")
		}
		replayM.Disabled = false
		if openaiM != nil {
			openaiM.Disabled = true
		}
		setAgentModel(manifests, replayModel(replayM))
	default:
		return fmt.Errorf("未知的 LLM provider: %s", provider)
	}
	return nil
}

func findManifest(manifests []*plugin.Manifest, id string) *plugin.Manifest {
	for _, m := range manifests {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// enabledManifest 只返回启用中的插件;preset 的清单里也含被 disable 的插件。
func enabledManifest(manifests []*plugin.Manifest, id string) *plugin.Manifest {
	for _, m := range manifests {
		if m.ID == id && !m.Disabled {
			return m
		}
	}
	return nil
}

func setAgentModel(manifests []*plugin.Manifest, model string) {
	if m := findManifest(manifests, "agent-loop"); m != nil {
		setConfig(m, "model", model)
	}
}

func replayModel(m *plugin.Manifest) string {
	if v, ok := m.Config["defaultModel"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if models, ok := m.Config["models"].([]any); ok && len(models) > 0 {
		if first, ok := models[0].(map[string]any); ok {
			if id, ok := first["id"].(string); ok && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
	}
	return "replay"
}

func setConfig(m *plugin.Manifest, key string, value any) {
	if m.Config == nil {
		m.Config = map[string]any{}
	}
	m.Config[key] = value
}

// presetModel returns the model the preset actually configured on agent-loop.
func presetModel(manifests []*plugin.Manifest) string {
	for _, m := range manifests {
		if m.ID != "agent-loop" {
			continue
		}
		if v, ok := m.Config["model"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// moduleRoot walks up from start until it finds the ada_go_v2 go.mod. It lets
// presets that reference repository-relative files keep working when the bot
// process is started in another working directory.
func moduleRoot(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func samePath(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	if aerr == nil && berr == nil {
		return os.SameFile(ai, bi)
	}
	aa, _ := filepath.Abs(a)
	ba, _ := filepath.Abs(b)
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(ba)) || filepath.Clean(aa) == filepath.Clean(ba)
}
