// Package compaction 是 `core/compaction-basic`:把较早的会话事件折叠成一条摘要。
//
// 它不删除历史:只调用 session.Compact 追加 `compact/summary`;render 按投影边界跳过旧事件。
// 第一版摘要由事件文本确定性地拼接,不调用模型;模型摘要留给后续 provider。
package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/adag/ada-go/v2/platform/kernel"
	"github.com/adag/ada-go/v2/plugins/seam/llm"
	"github.com/adag/ada-go/v2/plugins/seam/session"
	"github.com/adag/ada-go/v2/plugins/seam/tools"
)

// PluginName 同时是插件 id。
const PluginName = "compaction-basic"

// ServiceName 是 agent-loop 查找压缩器的服务名。
const ServiceName = "compaction"

type config struct {
	KeepRecent     int `json:"keepRecent,omitempty"`
	MaxSummaryRune int `json:"maxSummaryRune,omitempty"`
}

// Compactor 生成摘要并追加 compact/summary。
type Compactor struct {
	keepRecent int
	maxRune    int
}

// New 构造压缩器;keepRecent 默认 8,最大摘要默认 4096 字符。
func New(keepRecent, maxRune int) *Compactor {
	if keepRecent < 2 {
		keepRecent = 8
	}
	if maxRune <= 0 {
		maxRune = 4096
	}
	return &Compactor{keepRecent: keepRecent, maxRune: maxRune}
}

// Compact 把最老的一段事件压缩成摘要;已压缩或没有可压缩前缀时为空操作。
func (c *Compactor) Compact(ctx context.Context, sess session.Session, _ any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	events, err := sess.Events(0, 0)
	if err != nil {
		return err
	}
	if len(events) <= c.keepRecent {
		return nil
	}
	cut := len(events) - c.keepRecent
	last := compactedTo(events)
	if cut <= 0 || events[cut-1].Seq <= last {
		return nil
	}
	upTo := events[cut-1].Seq
	text := c.summary(events[:cut], last)
	if text == "" {
		text = fmt.Sprintf("已压缩 %d 条早期事件", cut)
	}
	data, err := json.Marshal(map[string]any{
		"upTo": upTo, "summary": text, "events": cut,
		// 与 turn-stats 使用同一估算口径,投影据此扣减已压缩前缀的 token。
		"tokens": c.tokens(events[:cut], last),
	})
	if err != nil {
		return err
	}
	return sess.Compact(ctx, upTo, session.Event{
		Type:   session.TypeCompactSummary,
		Data:   data,
		Source: "core/compaction-basic",
	})
}

func (c *Compactor) summary(events []session.Event, after uint64) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Seq <= after {
			continue
		}
		switch ev.Type {
		case session.TypeUserMessage:
			var m llm.Message
			if json.Unmarshal(ev.Data, &m) == nil {
				appendLine(&b, "user", m.Text(), c.maxRune)
			}
		case session.TypeAssistantMessage:
			var p struct {
				Message llm.Message `json:"message"`
			}
			if json.Unmarshal(ev.Data, &p) == nil {
				appendLine(&b, "assistant", p.Message.Text(), c.maxRune)
			}
		}
		if b.Len() >= c.maxRune {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// tokens 与 core/session-stats 的估算口径一致:4 个 UTF-8 字符 ≈ 1 token。
func (c *Compactor) tokens(events []session.Event, after uint64) int {
	total := 0
	for _, ev := range events {
		if ev.Seq <= after {
			continue
		}
		switch ev.Type {
		case session.TypeUserMessage:
			var m llm.Message
			if json.Unmarshal(ev.Data, &m) == nil {
				total += tokensBlocks(m.Content)
			}
		case session.TypeAssistantMessage:
			var p struct {
				Message llm.Message `json:"message"`
				Usage   llm.Usage   `json:"usage"`
			}
			if json.Unmarshal(ev.Data, &p) == nil {
				total += tokensBlocks(p.Message.Content)
				total += p.Usage.InputTokens + p.Usage.OutputTokens
			}
		case session.TypeToolResult:
			var p struct {
				Result tools.Result `json:"result"`
			}
			if json.Unmarshal(ev.Data, &p) == nil {
				total += tokensBlocks(p.Result.Content)
			}
		}
	}
	return total
}

func tokensBlocks(blocks []llm.Block) int {
	total := 0
	for _, b := range blocks {
		if b.Kind == llm.BlockText {
			total += estimateTokens(b.Text)
		}
	}
	return total
}

func estimateTokens(s string) int {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

func appendLine(b *strings.Builder, role, text string, max int) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if utf8.RuneCountInString(text) > 240 {
		r := []rune(text)
		text = string(r[:240]) + "…"
	}
	line := role + ": " + text
	if b.Len()+utf8.RuneCountInString(line)+1 > max {
		return
	}
	b.WriteString(line)
	b.WriteByte('\n')
}

func compactedTo(events []session.Event) uint64 {
	var out uint64
	for _, ev := range events {
		if ev.Type != session.TypeCompactSummary {
			continue
		}
		var p struct {
			UpTo uint64 `json:"upTo"`
		}
		if json.Unmarshal(ev.Data, &p) == nil && p.UpTo > out {
			out = p.UpTo
		}
	}
	return out
}

// Plugin 返回 compaction-basic 插件:provide "compaction" 服务。
func Plugin() kernel.Plugin {
	return kernel.Func(PluginName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		var c config
		if err := cfg.Decode(&c); err != nil {
			return nil, kernel.Fail(kernel.CodeInvalid, "compaction-basic: 解析配置失败: %v", err)
		}
		_, err := ctx.Provide(ServiceName, New(c.KeepRecent, c.MaxSummaryRune),
			kernel.WithVersion(1), kernel.WithKind("object"))
		return nil, err
	}).WithProvide(ServiceName)
}
