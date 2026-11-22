package tui_test

import (
	"os"
	"strings"
	"testing"

	"github.com/issueye/ada_go_v2/platform/host/tui"
)

func runTUI(t *testing.T, input string, args ...string) (int, string, string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir("../../.."); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	var out, errOut strings.Builder
	code := tui.Run(args, strings.NewReader(input), &out, &errOut)
	return code, out.String(), errOut.String()
}

// 1. 回放 preset:输入一句话,输出最终文本;退出码 0。
func TestChatReplay(t *testing.T) {
	code, out, errOut := runTUI(t, "读取 a.txt 并总结\n/quit\n", "--preset", "examples/agent/agent.preset.json", "--plain")
	if code != 0 {
		t.Fatalf("TUI 应成功,code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "文件内容是 hello") {
		t.Fatalf("TUI 输出不正确:\n%s", out)
	}
	if !strings.Contains(out, "ada tui") || !strings.Contains(out, "/quit") {
		t.Fatalf("缺少标题或命令提示:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("--plain 不应输出 ANSI 转义:\n%q", out)
	}
}

// 2. 工作区启动绑定 + 会话内切换。
func TestWorkspaceAndSwitch(t *testing.T) {
	code, out, errOut := runTUI(t,
		"/workspace docs\n查询工作区并读取 a.txt\n/session\n/quit\n",
		"--preset", "examples/workspace/workspace.preset.json", "--plain")
	if code != 0 {
		t.Fatalf("TUI 应成功,code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "已切换到工作区 docs") || !strings.Contains(out, "工作区查询完成,文件内容是 hello") {
		t.Fatalf("切换工作区后输出不正确:\n%s", out)
	}
	if !strings.Contains(out, "工作区 docs") {
		t.Fatalf("/session 未显示绑定工作区:\n%s", out)
	}
}

// 3. 用法错误返回 2;不会开始装配。
func TestUsage(t *testing.T) {
	code, _, errOut := runTUI(t, "", "--workspace", "docs")
	if code != 2 || !strings.Contains(errOut, "需要给出插件目录或 --preset 文件") {
		t.Fatalf("用法错误应 code=2,实际 %d err=%s", code, errOut)
	}
}

// 4. 未提供 workspace 时 --workspace 明确失败,不静默起一个空会话。
func TestWorkspaceMissing(t *testing.T) {
	code, _, errOut := runTUI(t, "", "--preset", "examples/agent/agent.preset.json", "--workspace", "docs")
	if code != 1 || !strings.Contains(errOut, "preset 没有 workspace 服务") {
		t.Fatalf("缺少 workspace 服务应失败,code=%d err=%s", code, errOut)
	}
}
