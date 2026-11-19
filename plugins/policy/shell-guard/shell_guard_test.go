package shellguard_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/adag/ada-go/v2/kernel"
	shellguard "github.com/adag/ada-go/v2/plugins/policy/shell-guard"
	shelllocal "github.com/adag/ada-go/v2/plugins/provider/shell-local"
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

// 1. 只读模式:白名单内放行,白名单外拒绝。
func TestReadOnlyAllowlist(t *testing.T) {
	fake := &fakeShell{}
	g := shellguard.New(fake, shellguard.ReadOnly, []string{"echo"}, false)
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

// 2. workspace-write / danger 放行任意程序,但 shell 模式仍需显式授权。
func TestShellFlag(t *testing.T) {
	fake := &fakeShell{}
	for _, mode := range []shellguard.Mode{shellguard.WorkspaceWrite, shellguard.Danger} {
		g := shellguard.New(fake, mode, nil, false)
		if _, err := g.Run(context.Background(), seamshell.Command{Program: "any-program"}); err != nil {
			t.Fatalf("%s 应放行普通程序: %v", mode, err)
		}
		if _, err := g.Run(context.Background(), seamshell.Command{Program: "any-program", Shell: true}); err == nil {
			t.Fatalf("%s 下 shell=true 未授权应拒绝", mode)
		}
	}

	allowed := shellguard.New(fake, shellguard.WorkspaceWrite, nil, true)
	if _, err := allowed.Run(context.Background(), seamshell.Command{Program: "echo", Shell: true}); err != nil {
		t.Fatalf("显式授权后 shell=true 应放行: %v", err)
	}
}

// 3. 与内核装配:seam + provider + policy,消费者执行白名单命令。
func TestKernelIntegration(t *testing.T) {
	dir := t.TempDir()
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	for _, plugin := range []kernel.Plugin{
		seamshell.Plugin(),
		shelllocal.Plugin(),
		shellguard.Plugin(),
	} {
		if _, err := app.Plugin(plugin, kernel.NewConfig(map[string]any{
			"workspace": dir,
			"mode":      string(shellguard.ReadOnly),
			"allow":     allowPrograms(),
		})); err != nil {
			t.Fatalf("挂载插件失败: %v", err)
		}
	}

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

// 4. 无 provider 时 Get 返回 EUNIMPLEMENTED。
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

// 5. 版本守卫:未配置 workspace 时 provider 安装失败(明确错误)。
func TestProviderRequiresWorkspace(t *testing.T) {
	if _, err := shelllocal.New(shelllocal.Options{}); err == nil {
		t.Fatalf("缺少 workspace 应报错")
	}
}

// allowPrograms / echoCommand 让用例在 Windows 与 Unix 上跑同一条语义。
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
