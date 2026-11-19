package shelllocal_test

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	shelllocal "github.com/adag/ada-go/v2/plugins/provider/shell-local"
	"github.com/adag/ada-go/v2/plugins/seam/seamerr"
	seamshell "github.com/adag/ada-go/v2/plugins/seam/shell"
)

func errCode(err error) seamerr.Code {
	var e *seamerr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func newShell(t *testing.T, opts shelllocal.Options) *shelllocal.Shell {
	t.Helper()
	if opts.Workspace == "" {
		opts.Workspace = t.TempDir()
	}
	s, err := shelllocal.New(opts)
	if err != nil {
		t.Fatalf("创建 shell-local 失败: %v", err)
	}
	return s
}

func echoCommand(args ...string) seamshell.Command {
	if runtime.GOOS == "windows" {
		return seamshell.Command{Program: "cmd", Args: append([]string{"/c", "echo"}, args...)}
	}
	return seamshell.Command{Program: "echo", Args: args}
}

func sleepCommand() seamshell.Command {
	// Windows 用 powershell 单进程 sleep:cmd /c ping 会留下子进程,
	// 子进程仍持有工作目录,导致测试的 TempDir 清理失败(也会暴露真实的杀树缺口)。
	if runtime.GOOS == "windows" {
		return seamshell.Command{Program: "powershell", Args: []string{"-NoProfile", "-Command", "Start-Sleep -Seconds 5"}}
	}
	return seamshell.Command{Program: "sleep", Args: []string{"20"}}
}

func exitCommand(code string) seamshell.Command {
	if runtime.GOOS == "windows" {
		return seamshell.Command{Program: "cmd", Args: []string{"/c", "exit /b " + code}}
	}
	return seamshell.Command{Program: "sh", Args: []string{"-c", "exit " + code}}
}

// 1. 基本执行:输出、退出码、耗时、Facts 可见。
func TestRunEcho(t *testing.T) {
	s := newShell(t, shelllocal.Options{})
	res, err := s.Run(context.Background(), echoCommand("hello"))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(string(res.Stdout), "hello") {
		t.Fatalf("输出不正确: %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Fatalf("退出码应为 0,实际 %d", res.ExitCode)
	}
	if res.Duration <= 0 {
		t.Fatalf("应记录耗时")
	}
	if res.Facts.Enforced {
		t.Fatalf("未接沙箱时 Facts.Enforced 应为 false(结果必须如实反映)")
	}
}

// 2. 环境净化:未白名单的变量不可见,注入也失败。
func TestEnvIsolation(t *testing.T) {
	s := newShell(t, shelllocal.Options{AllowedEnv: []string{"PATH"}})
	if _, ok := s.Env().Get("SECRET"); ok {
		t.Fatalf("SECRET 不应出现在受管环境")
	}
	if _, ok := s.Env().Get("PATH"); !ok {
		t.Fatalf("PATH 应在受管环境里")
	}
	if _, err := s.Run(context.Background(), seamshell.Command{Program: echoProgram(), Env: map[string]string{"SECRET": "x"}}); err == nil || errCode(err) != seamshell.CodeInvalid {
		t.Fatalf("注入未白名单变量应 EINVALID,实际 %v", err)
	}
}

// 3. Program 含空白:拒绝(防止被误当成命令串)。
func TestRejectsProgramWithSpace(t *testing.T) {
	s := newShell(t, shelllocal.Options{})
	if _, err := s.Run(context.Background(), seamshell.Command{Program: "echo; rm -rf /"}); err == nil || errCode(err) != seamshell.CodeInvalid {
		t.Fatalf("含空白的 Program 应拒绝,实际 %v", err)
	}
}

// 4. 超时:ETIMEOUT,且及时返回。
func TestTimeout(t *testing.T) {
	s := newShell(t, shelllocal.Options{})
	start := time.Now()
	_, err := s.Run(context.Background(), seamshell.Command{Program: sleepCommand().Program, Args: sleepCommand().Args, Timeout: 200 * time.Millisecond})
	if err == nil || errCode(err) != seamshell.CodeTimeout {
		t.Fatalf("超时应 ETIMEOUT,实际 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("超时没有及时返回: %s", elapsed)
	}
}

// 5. 输出上限:截断可见,命令被终止。
func TestOutputTruncation(t *testing.T) {
	s := newShell(t, shelllocal.Options{MaxOutput: 64})
	long := strings.Repeat("x", 200)
	res, err := s.Run(context.Background(), echoCommand(long))
	if err != nil {
		t.Fatalf("截断不应作为错误返回: %v", err)
	}
	if !res.Truncated {
		t.Fatalf("应标记 Truncated")
	}
	if int64(len(res.Stdout)) > 64 {
		t.Fatalf("输出不应超过上限,实际 %d", len(res.Stdout))
	}
}

// 6. Stream:分块回调 + 汇总结果。
func TestStream(t *testing.T) {
	s := newShell(t, shelllocal.Options{})
	var text strings.Builder
	chunks := 0
	res, err := s.Stream(context.Background(), echoCommand("hello"), func(c seamshell.Chunk) error {
		chunks++
		text.WriteString(c.Text)
		return nil
	})
	if err != nil {
		t.Fatalf("流式执行失败: %v", err)
	}
	if chunks == 0 {
		t.Fatalf("应至少收到一个 chunk")
	}
	if !strings.Contains(text.String(), "hello") {
		t.Fatalf("chunk 内容不正确: %q", text.String())
	}
	if !strings.Contains(string(res.Stdout), "hello") {
		t.Fatalf("汇总输出不正确: %q", res.Stdout)
	}
}

// 7. Shell=true 默认拒绝。
func TestShellModeDenied(t *testing.T) {
	s := newShell(t, shelllocal.Options{})
	if _, err := s.Run(context.Background(), seamshell.Command{Program: "echo", Shell: true}); err == nil || errCode(err) != seamshell.CodeDenied {
		t.Fatalf("Shell=true 默认应 EDENIED,实际 %v", err)
	}
}

// 8. 非零退出码不是 seam 错误,退出码就是结果。
func TestNonZeroExit(t *testing.T) {
	s := newShell(t, shelllocal.Options{})
	res, err := s.Run(context.Background(), exitCommand("3"))
	if err != nil {
		t.Fatalf("非零退出不应返回错误: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("退出码应为 3,实际 %d", res.ExitCode)
	}
}

// 9. 工作目录:虚拟路径越界拒绝。
func TestDirEscape(t *testing.T) {
	s := newShell(t, shelllocal.Options{})
	if _, err := s.Run(context.Background(), seamshell.Command{Program: echoProgram(), Dir: "/workspace/../etc"}); err == nil {
		t.Fatalf("越界 Dir 应拒绝")
	}
}

func echoProgram() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "echo"
}
