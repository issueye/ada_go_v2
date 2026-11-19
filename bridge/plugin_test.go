package bridge_test

import (
	"context"
	"testing"

	"github.com/adag/ada-go/v2/bridge"
	"github.com/adag/ada-go/v2/compile"
	"github.com/adag/ada-go/v2/kernel"
)

func buildArtifact(t *testing.T, files map[string]string) *compile.Artifact {
	t.Helper()
	art, err := compile.Build("demo", "plugin.ts", files, compile.Options{})
	if err != nil {
		t.Fatalf("构建 Artifact 失败: %v", err)
	}
	return art
}

// 1. TS 插件端到端:编译 → 加载 → kernel 挂载 → apply(配置) → 日志 → 卸载。
func TestJSPluginRunsInKernel(t *testing.T) {
	art := buildArtifact(t, map[string]string{
		"plugin.ts": `import { greet } from "./util"
export enum Level { High = 3 }
export function apply(cfg: { name: string }) {
  __adaLog(greet(cfg.name))
  return { ok: true, level: Level.High }
}`,
		"util.ts": `export function greet(name: string): string { return "hello, " + name }`,
	})

	p, err := bridge.New(bridge.Options{Name: "js-demo", Artifact: art})
	if err != nil {
		t.Fatalf("构造插件失败: %v", err)
	}

	a := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	f, err := a.Plugin(p, kernel.NewConfig(map[string]any{"name": "ada"}))
	if err != nil {
		t.Fatalf("挂载失败: %v", err)
	}
	if f.State() != kernel.Active {
		t.Fatalf("JS 插件应 active,实际 %s", f.State())
	}
	if logs := p.Logs(); len(logs) != 1 || logs[0] != "hello, ada" {
		t.Fatalf("JS 日志不正确: %v", logs)
	}

	if err := f.Dispose(context.Background()); err != nil {
		t.Fatalf("卸载失败: %v", err)
	}
	if f.State() != kernel.Disposed {
		t.Fatalf("卸载后应为 disposed,实际 %s", f.State())
	}
}

// 2. 模块没有 apply 导出时,fiber 必须 failed,而不是静默「成功」。
func TestJSPluginWithoutApplyFails(t *testing.T) {
	art := buildArtifact(t, map[string]string{
		"plugin.ts": `export const x = 1`,
	})
	p, err := bridge.New(bridge.Options{Name: "no-apply", Artifact: art})
	if err != nil {
		t.Fatalf("构造插件失败: %v", err)
	}
	a := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	f, err := a.Plugin(p, kernel.NewConfig(nil))
	if err != nil {
		t.Fatalf("挂载本身不应失败: %v", err)
	}
	if f.State() != kernel.Failed {
		t.Fatalf("缺 apply 时应 failed,实际 %s", f.State())
	}
}
