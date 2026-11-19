package desktop_test

import (
	"context"
	"testing"

	"github.com/adag/ada-go/v2/host/desktop"
	"github.com/adag/ada-go/v2/kernel"
)

type fakeRenderer struct {
	views []desktop.View
}

func (f *fakeRenderer) Render(v desktop.View) error {
	f.views = append(f.views, v)
	return nil
}

func mountDemo(t *testing.T, app *kernel.App) {
	t.Helper()
	if _, err := app.Plugin(kernel.Func("demo", func(ctx *kernel.Context, _ kernel.Config) (kernel.Dispose, error) {
		if _, err := ctx.Provide("svc", "demo"); err != nil {
			return nil, err
		}
		_, err := ctx.Effect("demo-effect", func() (kernel.Dispose, error) { return nil, nil })
		return nil, err
	}), kernel.NewConfig(nil)); err != nil {
		t.Fatalf("挂载失败: %v", err)
	}
}

// 1. 映射 fiber/service/effect 并交给渲染器。
func TestRefresh(t *testing.T) {
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	mountDemo(t, app)
	r := &fakeRenderer{}
	h, err := desktop.New(app, r)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	view, err := h.Refresh()
	if err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	if len(view.Fibers) != 1 || view.Fibers[0].Plugin != "demo" {
		t.Fatalf("fibers 映射不正确: %+v", view.Fibers)
	}
	if len(view.Services) != 1 || view.Services[0].Name != "svc" {
		t.Fatalf("services 映射不正确: %+v", view.Services)
	}
	if len(view.Effects) < 1 || view.Effects[0].Plugin != "demo" {
		t.Fatalf("effects 映射不正确: %+v", view.Effects)
	}
	if len(r.views) != 1 {
		t.Fatalf("渲染器应被调用一次,实际 %d", len(r.views))
	}

	// 重复刷新幂等(视图内容一致)。
	if _, err := h.Refresh(); err != nil {
		t.Fatalf("第二次刷新失败: %v", err)
	}
	if len(r.views) != 2 {
		t.Fatalf("渲染器应被调用两次,实际 %d", len(r.views))
	}
}

// 2. 构造参数校验。
func TestNewValidation(t *testing.T) {
	if _, err := desktop.New(nil, &fakeRenderer{}); err == nil {
		t.Fatalf("nil app 应报错")
	}
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if _, err := desktop.New(app, nil); err == nil {
		t.Fatalf("nil renderer 应报错")
	}
}
