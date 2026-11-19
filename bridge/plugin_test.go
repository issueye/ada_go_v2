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

// 1. TS 插件端到端:编译 → 加载 → apply(ctx, cfg) → 提供服务 + 派发事件 → 卸载。
func TestJSPluginRunsInKernel(t *testing.T) {
	art := buildArtifact(t, map[string]string{
		"plugin.ts": `import { greet } from "./util"
export enum Level { High = 3 }
type Ctx = { log: (m: string) => void; provide: (n: string, v: unknown) => void; emit: (e: string, p: unknown) => void }
export function apply(ctx: Ctx, cfg: { name: string }) {
  ctx.log(greet(cfg.name))
  ctx.provide("ts.greeting", { text: greet(cfg.name), level: Level.High })
  ctx.emit("ts/ready", { name: cfg.name })
}`,
		"util.ts": `export function greet(name: string): string { return "hello, " + name }`,
	})

	p, err := bridge.New(bridge.Options{Name: "js-demo", Artifact: art})
	if err != nil {
		t.Fatalf("构造插件失败: %v", err)
	}

	a := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	// 消费者先挂:依赖 JS 插件提供的服务,应当在 JS 提供后自动激活。
	var got any
	consumer := kernel.Func("ts-consumer", func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		v, err := ctx.Service("ts.greeting")
		if err != nil {
			return nil, err
		}
		got = v
		return nil, nil
	}).WithInject("ts.greeting")
	cf, err := a.Plugin(consumer, kernel.NewConfig(nil))
	if err != nil {
		t.Fatalf("挂载消费者失败: %v", err)
	}
	if cf.State() != kernel.Pending {
		t.Fatalf("消费者应先 pending,实际 %s", cf.State())
	}

	// 事件在根作用域监听:JS 侧 emit 的提交点是 apply 之后的内核协程。
	var eventPayload any
	a.Root().On("ts/ready", func(_ context.Context, _ *kernel.Context, args []any) (any, error) {
		if len(args) > 0 {
			eventPayload = args[0]
		}
		return nil, nil
	})

	f, err := a.Plugin(p, kernel.NewConfig(map[string]any{"name": "ada"}))
	if err != nil {
		t.Fatalf("挂载失败: %v", err)
	}
	if f.State() != kernel.Active {
		t.Fatalf("JS 插件应 active,实际 %s", f.State())
	}
	if cf.State() != kernel.Active {
		t.Fatalf("依赖 JS 服务的消费者应被激活,实际 %s", cf.State())
	}
	m, ok := got.(map[string]any)
	if !ok || m["text"] != "hello, ada" {
		t.Fatalf("消费者拿到的服务值不正确: %v(%T)", got, got)
	}
	if eventPayload == nil {
		t.Fatalf("JS 侧 emit 的事件没有派发")
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
