package bridge_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/issueye/ada_go_v2/platform/host/builtins"
	"github.com/issueye/ada_go_v2/platform/kernel"
	"github.com/issueye/ada_go_v2/platform/loader"
	"github.com/issueye/ada_go_v2/platform/plugin"
	"github.com/issueye/ada_go_v2/plugins/seam/tools"
)

// TestJSToolMemory 用真实 fs-local 跑 memory-helper 的完整回路:
// 写入 → 合并 → 中文检索 → 读取 → 更新 → 忘记 → 统计。
func TestJSToolMemory(t *testing.T) {
	dir := t.TempDir()
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	mountBuiltin(t, app, "seam-tools", nil)
	mountBuiltin(t, app, "seam-fs", nil)
	mountBuiltin(t, app, "fs-local", map[string]any{"workspace": dir})

	manifest, err := plugin.Load("../../plugins/tool/memory-helper")
	if err != nil {
		t.Fatalf("读取 memory-helper 清单失败: %v", err)
	}
	lookup := loader.Lookup(loader.Options{Go: builtins.Plugins()})
	p, err := lookup(manifest)
	if err != nil {
		t.Fatalf("构造 memory-helper 失败: %v", err)
	}
	mount(t, app, p)

	raw, err := app.Root().Service("tools")
	if err != nil {
		t.Fatal(err)
	}
	reg := raw.(*tools.Registry)

	call := func(name string, args map[string]any) tools.Result {
		t.Helper()
		data, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		res, err := reg.Invoke(context.Background(), tools.CallInfo{Name: name, Args: data})
		if err != nil {
			t.Fatalf("%s 调用失败: %v", name, err)
		}
		return res
	}
	text := func(res tools.Result) string {
		t.Helper()
		if len(res.Content) == 0 {
			return ""
		}
		return res.Content[0].Text
	}

	// 1. 写入:记录模型、标签与重要度。
	written := call("memory_write", map[string]any{
		"text":       "用户偏好中文回答,代码注释也使用中文。",
		"tags":       []string{"preference", "language"},
		"importance": 4,
	})
	if written.Status != tools.StatusOK {
		t.Fatalf("memory_write 失败: %+v", written)
	}
	out := text(written)
	if !strings.Contains(out, "已记住") {
		t.Fatalf("写入结果不正确: %s", out)
	}
	idRe := regexp.MustCompile(`#([0-9a-f]{8})`)
	m := idRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("结果里没有短 id: %s", out)
	}
	shortID := m[1]

	// 2. 重复写入应合并而不是新增。
	again := call("memory_write", map[string]any{
		"text":       "用户偏好中文回答,代码注释也使用中文。",
		"tags":       []string{"style"},
		"importance": 5,
	})
	if again.Status != tools.StatusOK || !strings.Contains(text(again), "合并") {
		t.Fatalf("重复写入应合并: %+v", again)
	}

	// 3. 中文检索:bigram 命中,结果带短 id。
	search := call("memory_search", map[string]any{"query": "中文回答", "limit": 5})
	if search.Status != tools.StatusOK || !strings.Contains(text(search), "#"+shortID) {
		t.Fatalf("memory_search 未命中: %+v", search)
	}

	// 4. 按前缀读取完整内容。
	read := call("memory_read", map[string]any{"id": shortID})
	if read.Status != tools.StatusOK || !strings.Contains(text(read), "用户偏好中文回答") {
		t.Fatalf("memory_read 失败: %+v", read)
	}

	// 5. 更新重要度与标签。
	update := call("memory_update", map[string]any{"id": shortID, "importance": 5, "tags": []string{"preference"}})
	if update.Status != tools.StatusOK || !strings.Contains(text(update), "已更新") {
		t.Fatalf("memory_update 失败: %+v", update)
	}

	// 6. 列表与统计都应当看到这一条。
	list := call("memory_list", map[string]any{"limit": 10})
	if list.Status != tools.StatusOK || !strings.Contains(text(list), "#"+shortID) {
		t.Fatalf("memory_list 失败: %+v", list)
	}
	stats := call("memory_stats", map[string]any{})
	if stats.Status != tools.StatusOK || !strings.Contains(text(stats), "总数: 1") {
		t.Fatalf("memory_stats 失败: %+v", stats)
	}

	// 7. 忘记后检索不到,文件里也没有正文了。
	forget := call("memory_forget", map[string]any{"id": shortID})
	if forget.Status != tools.StatusOK || !strings.Contains(text(forget), "已忘记") {
		t.Fatalf("memory_forget 失败: %+v", forget)
	}
	search = call("memory_search", map[string]any{"query": "中文回答"})
	if search.Status != tools.StatusOK || !strings.Contains(text(search), "没有匹配") {
		t.Fatalf("忘记后仍能检索到: %+v", search)
	}

	file := filepath.Join(dir, ".ada", "memory.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("记忆文件不存在: %v", err)
	}
	if strings.Contains(string(data), "用户偏好中文回答") {
		t.Fatalf("忘记后文件仍保留正文: %s", data)
	}
	var store struct {
		Version int `json:"version"`
		Records []struct {
			ID string `json:"id"`
		} `json:"records"`
	}
	if err := json.Unmarshal(data, &store); err != nil {
		t.Fatalf("记忆文件不是合法 JSON: %v", err)
	}
	if store.Version != 1 || len(store.Records) != 0 {
		t.Fatalf("记忆文件结构不正确: %+v", store)
	}
}
