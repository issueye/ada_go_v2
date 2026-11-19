package auditlog_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/v2/kernel"
	auditlog "github.com/adag/ada-go/v2/plugins/policy/audit-log"
	seamfs "github.com/adag/ada-go/v2/plugins/seam/fs"
	seamnet "github.com/adag/ada-go/v2/plugins/seam/net"
	seamshell "github.com/adag/ada-go/v2/plugins/seam/shell"
)

// 1. 事件被写成一行 JSON。
func TestWrites(t *testing.T) {
	path := t.TempDir() + "/audit.jsonl"
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if _, err := app.Plugin(auditlog.Plugin(), kernel.NewConfig(map[string]any{"path": path})); err != nil {
		t.Fatalf("挂载失败: %v", err)
	}
	app.Root().Emit(seamfs.EventObserve, map[string]any{"op": "read", "path": "/workspace/a"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读审计文件失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("应有 1 行,实际 %d", len(lines))
	}
	var rec struct {
		Event   string         `json:"event"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("行不是合法 JSON: %v", err)
	}
	if rec.Event != seamfs.EventObserve || rec.Payload["op"] != "read" {
		t.Fatalf("记录不正确: %+v", rec)
	}
}

// 2. 多类事件按顺序追加。
func TestAppendsMultiple(t *testing.T) {
	path := t.TempDir() + "/audit.jsonl"
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if _, err := app.Plugin(auditlog.Plugin(), kernel.NewConfig(map[string]any{"path": path})); err != nil {
		t.Fatalf("挂载失败: %v", err)
	}
	app.Root().Emit(seamfs.EventObserve, map[string]any{"op": "read"})
	app.Root().Emit(seamshell.EventObserve, map[string]any{"op": "run"})
	app.Root().Emit(seamnet.EventObserve, map[string]any{"op": "fetch"})
	data, _ := os.ReadFile(path)
	if got := strings.Count(string(data), "\n"); got != 3 {
		t.Fatalf("应有 3 行,实际 %d", got)
	}
}

// 3. 缺少 path → 插件失败。
func TestMissingPath(t *testing.T) {
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	fiber, err := app.Plugin(auditlog.Plugin(), kernel.NewConfig(map[string]any{}))
	if err != nil {
		t.Fatalf("挂载调用失败: %v", err)
	}
	rctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := fiber.Ready(rctx); err == nil {
		t.Fatalf("缺 path 应失败")
	}
}
