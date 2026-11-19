package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/v2/host/cli"
)

func run(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := cli.Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// 1. plan 文本输出与退出码。
func TestPlanText(t *testing.T) {
	code, out, errOut := run("plan", "../../examples/plugins")
	if code != 0 {
		t.Fatalf("plan 应成功,code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "顺序: greeter → upper → presenter") {
		t.Fatalf("plan 输出不正确:\n%s", out)
	}
	if errOut != "" {
		t.Fatalf("成功时不应写 stderr: %s", errOut)
	}
}

// 2. plan -json:输出是合法 JSON 且 ok=true。
func TestPlanJSON(t *testing.T) {
	code, out, _ := run("plan", "../../examples/plugins", "-json")
	if code != 0 {
		t.Fatalf("plan -json 应成功,code=%d", code)
	}
	var payload struct {
		OK    bool     `json:"ok"`
		Order []string `json:"order"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("输出不是合法 JSON: %v\n%s", err, out)
	}
	if !payload.OK || len(payload.Order) != 3 {
		t.Fatalf("JSON 内容不正确: %+v", payload)
	}
}

// 3. 加载失败 → 退出码 1 且写 stderr。
func TestPlanBadDir(t *testing.T) {
	code, _, errOut := run("plan", "../../examples/nope")
	if code != 1 || !strings.Contains(errOut, "加载失败") {
		t.Fatalf("应返回 1 并报错: code=%d err=%s", code, errOut)
	}
}

// 4. 无参数 → 用法错误(2)。
func TestUsage(t *testing.T) {
	code, _, errOut := run()
	if code != 2 || !strings.Contains(errOut, "用法") {
		t.Fatalf("应返回 2 并打印用法: code=%d err=%s", code, errOut)
	}
}

// 5. verify:未签名 ts 插件是 third-party。
func TestVerify(t *testing.T) {
	code, out, errOut := run("verify", "../../examples/plugins-ts/hello-ts")
	if code != 0 {
		t.Fatalf("verify 应成功,code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "信任: third-party") {
		t.Fatalf("verify 输出不正确:\n%s", out)
	}
}

// 6. inspect:挂载并输出快照。
func TestInspect(t *testing.T) {
	code, out, errOut := run("inspect", "--preset", "../../examples/presets/ts-only.preset.json")
	if code != 0 {
		t.Fatalf("inspect 应成功,code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "fibers:") || !strings.Contains(out, "services:") {
		t.Fatalf("inspect 输出不正确:\n%s", out)
	}
}

// 7. up:挂载后停止。
func TestUp(t *testing.T) {
	code, out, errOut := run("up", "--preset", "../../examples/presets/ts-only.preset.json")
	if code != 0 {
		t.Fatalf("up 应成功,code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "已停止。") {
		t.Fatalf("up 输出不正确:\n%s", out)
	}
}

// 8. install:安装到临时目录。
func TestInstall(t *testing.T) {
	dest := t.TempDir()
	code, out, errOut := run("install", "../../examples/plugins-ts/hello-ts", "-dest", dest)
	if code != 0 {
		t.Fatalf("install 应成功,code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "已安装") {
		t.Fatalf("install 输出不正确:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dest, "hello-ts@0.1.0", "plugin.ts")); err != nil {
		t.Fatalf("安装目录缺少插件文件: %v", err)
	}
}

// 9. 未知命令 → 用法错误。
func TestUnknownCommand(t *testing.T) {
	code, _, errOut := run("frobnicate")
	if code != 2 || !strings.Contains(errOut, "用法") {
		t.Fatalf("未知命令应返回 2: code=%d err=%s", code, errOut)
	}
}

// 10. verify 输出能力清单。
func TestVerifyCaps(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"id":"caps-demo","version":"0.1.0","kind":"go","caps":["fs.read","exec"]}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	code, out, errOut := run("verify", dir)
	if code != 0 {
		t.Fatalf("verify 应成功,code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "能力: fs.read, exec") {
		t.Fatalf("verify 应输出能力清单:\n%s", out)
	}
}

// 11. install 同意:声明能力的插件没有 -yes 时拒绝;-yes 后安装。
func TestInstallConsent(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"id":"caps-demo","version":"0.1.0","kind":"go","caps":["fs.read"]}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	dest := t.TempDir()

	code, out, errOut := run("install", dir, "-dest", dest)
	if code != 2 || !strings.Contains(errOut, "需要 -yes") {
		t.Fatalf("未确认应拒绝安装: code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "能力: fs.read") {
		t.Fatalf("拒绝前应展示能力:\n%s", out)
	}

	code, out, errOut = run("install", dir, "-dest", dest, "-yes")
	if code != 0 || !strings.Contains(out, "已安装") {
		t.Fatalf("确认后应安装成功: code=%d err=%s out=%s", code, errOut, out)
	}
	if _, err := os.Stat(filepath.Join(dest, "caps-demo@0.1.0", "plugin.json")); err != nil {
		t.Fatalf("安装产物缺失: %v", err)
	}
}
