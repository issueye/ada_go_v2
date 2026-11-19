package shellguard_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/adag/ada-go/v2/kernel"
	shellguard "github.com/adag/ada-go/v2/plugins/policy/shell-guard"
	sandboxnone "github.com/adag/ada-go/v2/plugins/provider/sandbox-none"
	shelllocal "github.com/adag/ada-go/v2/plugins/provider/shell-local"
	seamsandbox "github.com/adag/ada-go/v2/plugins/seam/sandbox"
	seamshell "github.com/adag/ada-go/v2/plugins/seam/shell"
)

type mapEnv map[string]string

func (e mapEnv) Get(name string) (string, bool) { v, ok := e[name]; return v, ok }
func (e mapEnv) Snapshot() map[string]string    { return map[string]string(e) }

type fakeShell struct {
	runs []seamshell.Command
}

func (f *fakeShell) Run(_ context.Context, cmd seamshell.Command) (seamshell.Result, error) {
	f.runs = append(f.runs, cmd)
	return seamshell.Result{Stdout: []byte("ok"), Facts: seamshell.Facts{Enforced: true}}, nil
}
func (f *fakeShell) Stream(_ context.Context, cmd seamshell.Command, onChunk func(seamshell.Chunk) error) (seamshell.Result, error) {
	f.runs = append(f.runs, cmd)
	_ = onChunk(seamshell.Chunk{Stream: "stdout", Text: "ok", Seq: 1})
	return seamshell.Result{Stdout: []byte("ok")}, nil
}
func (f *fakeShell) Env() seamshell.Env { return mapEnv{"PATH": "/usr/bin"} }

func registryWith(t *testing.T, mode seamsandbox.Mode) *seamsandbox.Registry {
	t.Helper()
	impl, err := sandboxnone.New(mode)
	if err != nil {
		t.Fatalf("创建 sandbox-none 失败: %v", err)
	}
	registry := seamsandbox.NewRegistry()
	if _, err := registry.Register("none", impl); err != nil {
		t.Fatalf("注册后端失败: %v", err)
	}
	return registry
}

// 1. 只读模式:白名单内放行,白名单外拒绝。
func TestReadOnlyAllowlist(t *testing.T) {
	fake := &fakeShell{}
	g := shellguard.New(fake, registryWith(t, seamsandbox.ModeReadOnly), "", []string{"echo"}, false)
	if _, err := g.Run(context.Background(), seamshell.Command{Program: "echo", Args: []string{"hi"}}); err != nil {
		t.Fatalf("白名单内应放行: %v", err)
	}
	if _, err := g.Run(context.Background(), seamshell.Command{Program: "whoami"}); err == nil {
		t.Fatalf("白名单外应拒绝")
	}
	if len(fake.runs) != 1 {
		t.Fatalf("被拒绝的命令不应触底,实际 %d 次", len(fake.runs))
	}
}

// 2. sandbox 允许执行,但 shell 模式仍需显式授权。
func TestShellFlag(t *testing.T) {
	fake := &fakeShell{}
	g := shellguard.New(fake, registryWith(t, seamsandbox.ModeWorkspaceWrite), "", nil, false)
	if _, err := g.Run(context.Background(), seamshell.Command{Program: "any-program"}); err != nil {
		t.Fatalf("普通程序应放行: %v", err)
	}
	if _, err := g.Run(context.Background(), seamshell.Command{Program: "any-program", Shell: true}); err == nil {
		t.Fatalf("shell=true 未授权应拒绝")
	}

	allowed := shellguard.New(fake, registryWith(t, seamsandbox.ModeWorkspaceWrite), "", nil, true)
	if _, err := allowed.Run(context.Background(), seamshell.Command{Program: "echo", Shell: true}); err != nil {
		t.Fatalf("显式授权后 shell=true 应放行: %v", err)
	}
}

// 3. 沙箱后端不可用时 fail-closed。
func TestNoSandboxBackendFailsClosed(t *testing.T) {
	fake := &fakeShell{}
	g := shellguard.New(fake, seamsandbox.NewRegistry(), "", nil, true)
	if _, err := g.Run(context.Background(), seamshell.Command{Program: "echo"}); err == nil {
		t.Fatalf("没有沙箱后端时命令必须失败")
	}
}

// 4. 与内核装配:seam/sandbox + sandbox-none + seam/shell + shell-local + shell-guard。
func TestKernelIntegration(t *testing.T) {
	dir := t.TempDir()
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	mount := func(name string, p kernel.Plugin, cfg map[string]any) {
		t.Helper()
		if _, err := app.Plugin(p, kernel.NewConfig(cfg)); err != nil {
			t.Fatalf("挂载 %s 失败: %v", name, err)
		}
	}
	mount("seam/shell", seamshell.Plugin(), nil)
	mount("seam/sandbox", seamsandbox.Plugin(), nil)
	mount("shell-local", shelllocal.Plugin(), map[string]any{"workspace": dir})
	mount("sandbox-none", sandboxnone.Plugin(), map[string]any{"mode": string(seamsandbox.ModeReadOnly)})
	mount("shell-guard", shellguard.Plugin(), map[string]any{"allow": allowPrograms()})

	var (
		output    string
		deniedErr error
	)
	consumer := kernel.Func("consumer", func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamshell.Chain](ctx, "shell")
		if err != nil {
			return nil, err
		}
		sh, err := chain.Get()
		if err != nil {
			return nil, err
		}
		res, err := sh.Run(context.Background(), echoCommand())
		if err != nil {
			return nil, err
		}
		output = string(res.Stdout)
		_, deniedErr = sh.Run(context.Background(), seamshell.Command{Program: "forbidden-program"})
		return nil, nil
	}).WithInject("shell")
	if _, err := app.Plugin(consumer, kernel.NewConfig(nil)); err != nil {
		t.Fatalf("挂载消费者失败: %v", err)
	}

	if output == "" {
		t.Fatalf("白名单命令没有输出")
	}
	if deniedErr == nil {
		t.Fatalf("非白名单命令应被拒绝")
	}
}

// 5. 无 provider 时 Get 返回 EUNIMPLEMENTED。
func TestKernelWithoutProvider(t *testing.T) {
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if _, err := app.Plugin(seamshell.Plugin(), kernel.NewConfig(nil)); err != nil {
		t.Fatalf("挂载 seam 失败: %v", err)
	}
	chain, err := kernel.Service[*seamshell.Chain](app.Root(), "shell")
	if err != nil {
		t.Fatalf("取服务失败: %v", err)
	}
	if _, err := chain.Get(); err == nil {
		t.Fatalf("没有 provider 时 Get 应失败")
	}
}

// 6. provider 缺少 workspace 时明确报错。
func TestProviderRequiresWorkspace(t *testing.T) {
	if _, err := shelllocal.New(shelllocal.Options{}); err == nil {
		t.Fatalf("缺少 workspace 应报错")
	}
}

func allowPrograms() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd"}
	}
	return []string{"echo"}
}

func echoCommand() seamshell.Command {
	if runtime.GOOS == "windows" {
		return seamshell.Command{Program: "cmd", Args: []string{"/c", "echo", "hello"}}
	}
	return seamshell.Command{Program: "echo", Args: []string{"hello"}}
}
