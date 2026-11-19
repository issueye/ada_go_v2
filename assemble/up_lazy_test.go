package assemble_test

import (
	"context"
	"testing"

	"github.com/adag/ada-go/v2/assemble"
	"github.com/adag/ada-go/v2/kernel"
	"github.com/adag/ada-go/v2/plugin"
)

func lazyManifests() []*plugin.Manifest {
	a := mf("startup", []string{"startup-svc"}, nil, nil)
	b := mf("lazy", []string{"lazy-svc"}, nil, nil)
	b.Activation = "on-demand"
	return []*plugin.Manifest{a, b}
}

func lazyLookup(t *testing.T) assemble.Lookup {
	t.Helper()
	return func(m *plugin.Manifest) (kernel.Plugin, error) {
		md := m
		return kernel.Func(md.ID, func(ctx *kernel.Context, _ kernel.Config) (kernel.Dispose, error) {
			for _, name := range md.Provide {
				if _, err := ctx.Provide(name, md.ID); err != nil {
					return nil, err
				}
			}
			return nil, nil
		}), nil
	}
}

// 1. UpLazy 只挂载 startup;on-demand 留给 Deferred。
func TestUpLazyMountsStartupOnly(t *testing.T) {
	app := newApp(t)
	ms := lazyManifests()
	plan := assemble.Plan(ms)
	if !plan.OK() {
		t.Fatalf("计划应通过: %v", plan.Err())
	}
	report, deferred, err := assemble.UpLazy(app, ms, plan, lazyLookup(t))
	if err != nil {
		t.Fatalf("UpLazy 失败: %v", err)
	}
	if _, ok := report.Fibers["lazy"]; ok {
		t.Fatalf("on-demand 插件不应在启动时挂载: %+v", report.Fibers)
	}
	if _, ok := report.Fibers["startup"]; !ok {
		t.Fatalf("startup 插件应已挂载: %+v", report.Fibers)
	}
	if got := deferred.IDs(); len(got) != 1 || got[0] != "lazy" {
		t.Fatalf("Deferred.IDs 不正确: %v", got)
	}
	if got := len(app.Inspect().Fibers); got != 1 {
		t.Fatalf("启动后应只有 1 个 fiber,实际 %d", got)
	}
}

// 2. Ensure 挂载 on-demand 插件;重复调用幂等。
func TestEnsureMounts(t *testing.T) {
	app := newApp(t)
	ms := lazyManifests()
	plan := assemble.Plan(ms)
	_, deferred, err := assemble.UpLazy(app, ms, plan, lazyLookup(t))
	if err != nil {
		t.Fatalf("UpLazy 失败: %v", err)
	}

	f1, err := deferred.Ensure(context.Background(), "lazy")
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if f1.State() != kernel.Active {
		t.Fatalf("Ensure 后应 active,实际 %s", f1.State())
	}
	snap := app.Inspect()
	if len(snap.Fibers) != 2 {
		t.Fatalf("应有 2 个 fiber,实际 %d", len(snap.Fibers))
	}
	found := false
	for _, s := range snap.Services {
		if s.Name == "lazy-svc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lazy-svc 未出现: %+v", snap.Services)
	}

	f2, err := deferred.Ensure(context.Background(), "lazy")
	if err != nil {
		t.Fatalf("重复 Ensure 失败: %v", err)
	}
	if f2.ID() != f1.ID() {
		t.Fatalf("重复 Ensure 应返回同一 fiber: %s != %s", f2.ID(), f1.ID())
	}
	if got := len(app.Inspect().Fibers); got != 2 {
		t.Fatalf("重复 Ensure 不应重复挂载,实际 %d", got)
	}
}

// 3. Ensure 的错误:未知 id、startup 插件。
func TestEnsureErrors(t *testing.T) {
	app := newApp(t)
	ms := lazyManifests()
	plan := assemble.Plan(ms)
	_, deferred, err := assemble.UpLazy(app, ms, plan, lazyLookup(t))
	if err != nil {
		t.Fatalf("UpLazy 失败: %v", err)
	}
	if _, err := deferred.Ensure(context.Background(), "nope"); err == nil {
		t.Fatalf("未知延迟插件应报错")
	}
	if _, err := deferred.Ensure(context.Background(), "startup"); err == nil {
		t.Fatalf("非 on-demand 插件 Ensure 应报错")
	}
}

// 4. startup 插件失败时回滚已挂载的 startup。
func TestUpLazyStartupFailureRollsBack(t *testing.T) {
	app := newApp(t)
	good := mf("good", []string{"good-svc"}, nil, nil)
	bad := mf("bad", nil, nil, nil)
	plan := assemble.Plan([]*plugin.Manifest{good, bad})
	lookup := func(m *plugin.Manifest) (kernel.Plugin, error) {
		if m.ID == "bad" {
			return kernel.Func("bad", func(*kernel.Context, kernel.Config) (kernel.Dispose, error) {
				return nil, kernel.Fail(kernel.CodeInvalid, "故意失败")
			}), nil
		}
		return kernel.Func("good", func(ctx *kernel.Context, _ kernel.Config) (kernel.Dispose, error) {
			_, err := ctx.Provide("good-svc", "good")
			return nil, err
		}), nil
	}
	if _, _, err := assemble.UpLazy(app, []*plugin.Manifest{good, bad}, plan, lookup); err == nil {
		t.Fatalf("startup 失败应报错")
	}
	if got := len(app.Inspect().Fibers); got != 0 {
		t.Fatalf("失败后应回滚干净,实际 %d", got)
	}
}

// 5. EnsureForService:按 provide 的服务名触发挂载;未知服务不动作。
func TestEnsureForService(t *testing.T) {
	app := newApp(t)
	ms := lazyManifests()
	plan := assemble.Plan(ms)
	_, deferred, err := assemble.UpLazy(app, ms, plan, lazyLookup(t))
	if err != nil {
		t.Fatalf("UpLazy 失败: %v", err)
	}

	f, found, err := deferred.EnsureForService(context.Background(), "lazy-svc")
	if err != nil || !found || f == nil {
		t.Fatalf("服务触发失败: f=%v found=%v err=%v", f, found, err)
	}
	snap := app.Inspect()
	ok := false
	for _, s := range snap.Services {
		if s.Name == "lazy-svc" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("lazy-svc 未出现: %+v", snap.Services)
	}

	f2, found2, err := deferred.EnsureForService(context.Background(), "lazy-svc")
	if err != nil || !found2 || f2.ID() != f.ID() {
		t.Fatalf("重复触发应幂等: %v %v %v", f2, found2, err)
	}
	if f3, found3, err := deferred.EnsureForService(context.Background(), "nope"); err != nil || found3 || f3 != nil {
		t.Fatalf("未知服务应不动作: %v %v %v", f3, found3, err)
	}
}

// 6. EnsureAll:按 id 排序挂载全部 on-demand。
func TestEnsureAll(t *testing.T) {
	app := newApp(t)
	a := mf("startup", []string{"s1"}, nil, nil)
	b := mf("lazy-b", []string{"sb"}, nil, nil)
	b.Activation = "on-demand"
	c := mf("lazy-c", []string{"sc"}, nil, nil)
	c.Activation = "on-demand"
	ms := []*plugin.Manifest{a, b, c}
	plan := assemble.Plan(ms)
	_, deferred, err := assemble.UpLazy(app, ms, plan, lazyLookup(t))
	if err != nil {
		t.Fatalf("UpLazy 失败: %v", err)
	}
	mounted, err := deferred.EnsureAll(context.Background())
	if err != nil {
		t.Fatalf("EnsureAll 失败: %v", err)
	}
	if len(mounted) != 2 || mounted[0] != "lazy-b" || mounted[1] != "lazy-c" {
		t.Fatalf("EnsureAll 顺序不正确: %v", mounted)
	}
	if got := len(app.Inspect().Fibers); got != 3 {
		t.Fatalf("应挂载 3 个插件,实际 %d", got)
	}
}
