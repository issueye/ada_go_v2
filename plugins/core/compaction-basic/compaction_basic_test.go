package compaction_test

import (
	"context"
	"encoding/json"
	"testing"

	compaction "github.com/adag/ada-go/v2/plugins/core/compaction-basic"
	memory "github.com/adag/ada-go/v2/plugins/provider/session-memory"
	"github.com/adag/ada-go/v2/plugins/seam/session"
)

func TestCompactAppendsSummary(t *testing.T) {
	ctx := context.Background()
	provider := memory.New()
	s, err := provider.Open(ctx, session.Meta{})
	if err != nil {
		t.Fatalf("打开会话失败: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := s.Append(session.Event{
			Type: session.TypeUserMessage,
			Data: []byte(`{"role":"user","content":[{"kind":"text","text":"message number ` + string(rune('0'+i%10)) + `"}]}`),
		}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}

	c := compaction.New(4, 1024)
	if err := c.Compact(ctx, s, nil); err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	events, err := s.Events(0, 0)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(events) != 13 {
		t.Fatalf("应追加 1 条摘要,实际 %d 条", len(events))
	}
	last := events[len(events)-1]
	if last.Type != session.TypeCompactSummary {
		t.Fatalf("最后一条应为摘要,实际 %s", last.Type)
	}
	var payload struct {
		UpTo    uint64 `json:"upTo"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(last.Data, &payload); err != nil {
		t.Fatalf("摘要载荷不合法: %v", err)
	}
	if payload.UpTo == 0 || payload.Summary == "" {
		t.Fatalf("摘要内容不完整: %+v", payload)
	}
}

func TestCompactNoopWhenShort(t *testing.T) {
	ctx := context.Background()
	provider := memory.New()
	s, err := provider.Open(ctx, session.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(session.Event{Type: session.TypeUserMessage}); err != nil {
		t.Fatal(err)
	}
	c := compaction.New(8, 1024)
	if err := c.Compact(ctx, s, nil); err != nil {
		t.Fatalf("短会话不应报错: %v", err)
	}
	if events, _ := s.Events(0, 0); len(events) != 1 {
		t.Fatalf("短会话不应追加摘要,实际 %d 条", len(events))
	}
}
