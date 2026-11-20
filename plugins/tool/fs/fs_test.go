package toolfs_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/issueye/ada_go_v2/platform/host/builtins"
	"github.com/issueye/ada_go_v2/platform/kernel"
	"github.com/issueye/ada_go_v2/plugins/seam/tools"
)

func mount(t *testing.T, app *kernel.App, id string, cfg map[string]any) {
	t.Helper()
	mk := builtins.Plugins()[id]
	if mk == nil {
		t.Fatalf("内置插件缺失: %s", id)
	}
	f, err := app.Plugin(mk(), kernel.NewConfig(cfg))
	if err != nil {
		t.Fatalf("挂载 %s 失败: %v", id, err)
	}
	if err := f.Ready(context.Background()); err != nil {
		t.Fatalf("等待 %s 就绪失败: %v", id, err)
	}
}

func invoke(t *testing.T, reg *tools.Registry, name string, args map[string]any) tools.Result {
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

// 1. 读写/编辑/搜索/列目录全链路。
func TestTextTools(t *testing.T) {
	dir := t.TempDir()
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	mount(t, app, "seam-tools", nil)
	mount(t, app, "seam-fs", nil)
	mount(t, app, "fs-local", map[string]any{"workspace": dir})
	mount(t, app, "tool-fs", nil)
	raw, err := app.Root().Service("tools")
	if err != nil {
		t.Fatal(err)
	}
	reg := raw.(*tools.Registry)

	if res := invoke(t, reg, "write_file", map[string]any{"path": "a.txt", "content": "hello world"}); res.Status != tools.StatusOK {
		t.Fatalf("write_file 失败: %+v", res)
	}
	if res := invoke(t, reg, "read_file", map[string]any{"path": "a.txt"}); res.Status != tools.StatusOK || !strings.Contains(res.Content[0].Text, "hello world") {
		t.Fatalf("read_file 失败: %+v", res)
	}
	if res := invoke(t, reg, "edit_file", map[string]any{"path": "a.txt", "old": "world", "new": "ada"}); res.Status != tools.StatusOK || !strings.Contains(res.Content[0].Text, "1 处") {
		t.Fatalf("edit_file 失败: %+v", res)
	}
	if res := invoke(t, reg, "search_files", map[string]any{"query": "ada"}); res.Status != tools.StatusOK || !strings.Contains(res.Content[0].Text, "a.txt") {
		t.Fatalf("search_files 失败: %+v", res)
	}
	if res := invoke(t, reg, "list_dir", map[string]any{}); res.Status != tools.StatusOK || !strings.Contains(res.Content[0].Text, "a.txt") {
		t.Fatalf("list_dir 失败: %+v", res)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(data) != "hello ada" {
		t.Fatalf("文件内容不正确: %v %q", err, data)
	}
}

// 2. 空搜索与非法参数返回工具错误/提示。
func TestTextToolsErrors(t *testing.T) {
	dir := t.TempDir()
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	mount(t, app, "seam-tools", nil)
	mount(t, app, "seam-fs", nil)
	mount(t, app, "fs-local", map[string]any{"workspace": dir})
	mount(t, app, "tool-fs", nil)
	raw, err := app.Root().Service("tools")
	if err != nil {
		t.Fatal(err)
	}
	reg := raw.(*tools.Registry)
	if res := invoke(t, reg, "search_files", map[string]any{"query": "nope"}); res.Status != tools.StatusOK || !strings.Contains(res.Content[0].Text, "无匹配") {
		t.Fatalf("空搜索处理不正确: %+v", res)
	}
	if res := invoke(t, reg, "search_files", map[string]any{"query": ""}); res.Status != tools.StatusError {
		t.Fatalf("空 query 应报错: %+v", res)
	}
}
