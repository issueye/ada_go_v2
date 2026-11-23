package agentloop_test

import (
	"context"
	"testing"

	"github.com/issueye/ada_go_v2/platform/host/builtins"
	"github.com/issueye/ada_go_v2/platform/kernel"
	"github.com/issueye/ada_go_v2/platform/loader"
	"github.com/issueye/ada_go_v2/platform/plugin"
)

// mountToolFS 挂载 TS 实现的 tool-fs;Go 内置注册表已不再包含它。
func mountToolFS(t *testing.T, app *kernel.App) {
	t.Helper()
	m, err := plugin.Load("../../../plugins/tool/fs")
	if err != nil {
		t.Fatalf("读取 tool-fs 清单失败: %v", err)
	}
	lookup := loader.Lookup(loader.Options{Go: builtins.Plugins()})
	p, err := lookup(m)
	if err != nil {
		t.Fatalf("构造 tool-fs 失败: %v", err)
	}
	f, err := app.Plugin(p, kernel.NewConfig(nil))
	if err != nil {
		t.Fatalf("挂载 tool-fs 失败: %v", err)
	}
	if err := f.Ready(context.Background()); err != nil {
		t.Fatalf("tool-fs 未就绪: %v", err)
	}
}
