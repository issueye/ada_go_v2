package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/v2/host/cli"
	"github.com/adag/ada-go/v2/install"
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

// 12. list:安装后能看到索引条目。
func TestList(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "plugin.json"),
		[]byte(`{"id":"list-demo","version":"0.1.0","kind":"go","caps":["fs.read"]}`), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	dest := t.TempDir()
	if code, _, errOut := run("install", src, "-dest", dest, "-yes"); code != 0 {
		t.Fatalf("安装失败: code=%d err=%s", code, errOut)
	}
	code, out, errOut := run("list", "-dest", dest)
	if code != 0 {
		t.Fatalf("list 应成功,code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "list-demo@0.1.0") || !strings.Contains(out, "trust=builtin") || !strings.Contains(out, "caps=fs.read") {
		t.Fatalf("list 输出不正确:\n%s", out)
	}
}

// 13. list:空安装根目录。
func TestListEmpty(t *testing.T) {
	code, out, errOut := run("list", "-dest", t.TempDir())
	if code != 0 || !strings.Contains(out, "(空)") {
		t.Fatalf("空列表输出不正确: code=%d err=%s out=%s", code, errOut, out)
	}
}

// 14. uninstall:卸载后 list 为空。
func TestUninstall(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "plugin.json"),
		[]byte(`{"id":"un-demo","version":"0.1.0","kind":"go"}`), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	dest := t.TempDir()
	if code, _, errOut := run("install", src, "-dest", dest); code != 0 {
		t.Fatalf("安装失败: code=%d err=%s", code, errOut)
	}
	code, out, errOut := run("uninstall", "un-demo", "-dest", dest)
	if code != 0 || !strings.Contains(out, "已卸载: un-demo") {
		t.Fatalf("卸载输出不正确: code=%d err=%s out=%s", code, errOut, out)
	}
	code, out, _ = run("list", "-dest", dest)
	if code != 0 || !strings.Contains(out, "(空)") {
		t.Fatalf("卸载后 list 应为空: code=%d out=%s", code, out)
	}
}

// 15. uninstall:未知 id → 退出码 1。
func TestUninstallUnknown(t *testing.T) {
	code, _, errOut := run("uninstall", "nope", "-dest", t.TempDir())
	if code != 1 || !strings.Contains(errOut, "卸载失败") {
		t.Fatalf("未知 id 应返回 1: code=%d err=%s", code, errOut)
	}
}

// 16. upgrade:安装 0.1 后升级 0.2,旧目录被清理。
func TestUpgrade(t *testing.T) {
	dest := t.TempDir()
	for _, ver := range []string{"0.1.0", "0.2.0"} {
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "plugin.json"),
			[]byte(`{"id":"up-demo","version":"`+ver+`","kind":"go"}`), 0o644); err != nil {
			t.Fatalf("写清单失败: %v", err)
		}
		args := []string{"install", src, "-dest", dest}
		if ver == "0.2.0" {
			args = []string{"upgrade", src, "-dest", dest}
		}
		code, out, errOut := run(args...)
		if code != 0 {
			t.Fatalf("%s 失败: code=%d err=%s", ver, code, errOut)
		}
		if ver == "0.2.0" && !strings.Contains(out, "已升级") {
			t.Fatalf("upgrade 输出不正确: %s", out)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "up-demo@0.1.0")); err == nil {
		t.Fatalf("旧版本目录应被清理")
	}
	code, out, _ := run("list", "-dest", dest)
	if code != 0 || !strings.Contains(out, "up-demo@0.2.0") || strings.Contains(out, "0.1.0") {
		t.Fatalf("list 应只显示最新版本:\n%s", out)
	}
}

// 17. upgrade -keep 后 rollback:回到旧版本并清理新版本。
func TestRollback(t *testing.T) {
	dest := t.TempDir()
	for _, ver := range []string{"0.1.0", "0.2.0"} {
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "plugin.json"),
			[]byte(`{"id":"rb-demo","version":"`+ver+`","kind":"go"}`), 0o644); err != nil {
			t.Fatalf("写清单失败: %v", err)
		}
		args := []string{"install", src, "-dest", dest}
		if ver == "0.2.0" {
			args = []string{"upgrade", src, "-dest", dest, "-keep"}
		}
		if code, _, errOut := run(args...); code != 0 {
			t.Fatalf("%s 失败: code=%d err=%s", ver, code, errOut)
		}
	}
	code, out, errOut := run("rollback", "rb-demo", "-dest", dest)
	if code != 0 || !strings.Contains(out, "已回滚: rb-demo → 0.1.0") {
		t.Fatalf("回滚输出不正确: code=%d err=%s out=%s", code, errOut, out)
	}
	if _, err := os.Stat(filepath.Join(dest, "rb-demo@0.2.0")); err == nil {
		t.Fatalf("新版本目录应被删除")
	}
	code, out, _ = run("list", "-dest", dest)
	if code != 0 || !strings.Contains(out, "rb-demo@0.1.0") || strings.Contains(out, "0.2.0") {
		t.Fatalf("回滚后 list 应只显示 0.1.0:\n%s", out)
	}
}

// 18. rollback:没有旧版本 → 退出码 1。
func TestRollbackNoPrevious(t *testing.T) {
	dest := t.TempDir()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "plugin.json"),
		[]byte(`{"id":"rb-demo","version":"0.1.0","kind":"go"}`), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	if code, _, errOut := run("install", src, "-dest", dest); code != 0 {
		t.Fatalf("安装失败: code=%d err=%s", code, errOut)
	}
	code, _, errOut := run("rollback", "rb-demo", "-dest", dest)
	if code != 1 || !strings.Contains(errOut, "回滚失败") {
		t.Fatalf("单版本回滚应返回 1: code=%d err=%s", code, errOut)
	}
}

// 19. caps:无能力清单可授权。
func TestCapsNone(t *testing.T) {
	code, out, errOut := run("caps", "../../examples/plugins")
	if code != 0 {
		t.Fatalf("caps 应成功: code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "(无)") || !strings.Contains(out, "结果: 可授权") {
		t.Fatalf("caps 输出不正确:\n%s", out)
	}
}

// 20. caps:能力与 trust 合法。
func TestCapsValid(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"id":"cap-demo","version":"0.1.0","kind":"go","trust":"third-party","caps":["fs.read"]}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	code, out, errOut := run("caps", dir)
	if code != 0 {
		t.Fatalf("caps 应成功: code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "caps=fs.read") || !strings.Contains(out, "trust=third-party") {
		t.Fatalf("caps 输出不正确:\n%s", out)
	}
}

// 21. caps:trust 不足 → 退出码 1。
func TestCapsTrustDenied(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"id":"cap-demo","version":"0.1.0","kind":"go","trust":"third-party","caps":["fs.write"]}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	code, out, errOut := run("caps", dir)
	if code != 1 || !strings.Contains(errOut, "授权失败") {
		t.Fatalf("trust 不足应返回 1: code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "结果: 不可授权") {
		t.Fatalf("应提示不可授权:\n%s", out)
	}
}

// 22. caps -json:结构化输出。
func TestCapsJSON(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"id":"cap-demo","version":"0.1.0","kind":"go","trust":"third-party","caps":["fs.read"]}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	code, out, _ := run("caps", dir, "-json")
	if code != 0 {
		t.Fatalf("caps -json 应成功: code=%d", code)
	}
	var payload struct {
		OK      bool `json:"ok"`
		Plugins []struct {
			ID   string   `json:"id"`
			Caps []string `json:"caps"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("输出不是合法 JSON: %v\n%s", err, out)
	}
	if !payload.OK || len(payload.Plugins) != 1 || payload.Plugins[0].ID != "cap-demo" {
		t.Fatalf("JSON 内容不正确: %+v", payload)
	}
}

// 23. up:内置 Go 安全栈(seam/fs + fs-local + caps-static + caps-gate)能挂载。
func TestUpBuiltinSecurityStack(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	writeManifest := func(id, body string) {
		t.Helper()
		sub := filepath.Join(dir, id)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sub, "plugin.json"), []byte(body), 0o644); err != nil {
			t.Fatalf("写清单失败: %v", err)
		}
	}
	writeManifest("seam-fs", `{"id":"seam-fs","version":"0.1.0","kind":"go","provide":["fs"]}`)
	writeManifest("fs-local", `{"id":"fs-local","version":"0.1.0","kind":"go","inject":{"required":["fs"]},"config":{"workspace":"`+strings.ReplaceAll(workspace, `\`, `\\`)+`"}}`)
	writeManifest("caps-static", `{"id":"caps-static","version":"0.1.0","kind":"go","provide":["caps"]}`)
	writeManifest("caps-gate", `{"id":"caps-gate","version":"0.1.0","kind":"go","inject":{"required":["fs"],"optional":["caps"]}}`)

	code, out, errOut := run("up", dir)
	if code != 0 {
		t.Fatalf("内置安全栈应装配成功: code=%d err=%s out=%s", code, errOut, out)
	}
	if !strings.Contains(out, "已停止。") {
		t.Fatalf("up 输出不正确:\n%s", out)
	}
}

// 24. update:从离线仓库更新到更高版本;再次更新提示已是最新。
func TestUpdateFromRepo(t *testing.T) {
	dest := t.TempDir()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "plugin.json"),
		[]byte(`{"id":"up-demo","version":"0.1.0","kind":"go"}`), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	if code, _, errOut := run("install", src, "-dest", dest); code != 0 {
		t.Fatalf("安装失败: %s", errOut)
	}

	repo := t.TempDir()
	rel := filepath.Join("plugins", "up-demo@0.2.0")
	abs := filepath.Join(repo, rel)
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(abs, "plugin.json"),
		[]byte(`{"id":"up-demo","version":"0.2.0","kind":"go"}`), 0o644); err != nil {
		t.Fatalf("写仓库清单失败: %v", err)
	}
	info, err := install.VerifyDir(abs, install.Options{})
	if err != nil {
		t.Fatalf("校验仓库插件失败: %v", err)
	}
	index := map[string]any{"entries": []install.RepoEntry{
		{ID: "up-demo", Version: "0.2.0", Dir: rel, Digest: info.Digest},
	}}
	data, _ := json.Marshal(index)
	if err := os.WriteFile(filepath.Join(repo, install.RepoFile), data, 0o644); err != nil {
		t.Fatalf("写仓库索引失败: %v", err)
	}

	code, out, errOut := run("update", "up-demo", "-repo", repo, "-dest", dest)
	if code != 0 || !strings.Contains(out, "已更新: up-demo@0.2.0") {
		t.Fatalf("更新失败: code=%d err=%s out=%s", code, errOut, out)
	}
	code, out, _ = run("list", "-dest", dest)
	if code != 0 || !strings.Contains(out, "up-demo@0.2.0") || strings.Contains(out, "0.1.0") {
		t.Fatalf("更新后 list 不正确:\n%s", out)
	}

	code, _, errOut = run("update", "up-demo", "-repo", repo, "-dest", dest)
	if code != 1 || !strings.Contains(errOut, "更新失败") {
		t.Fatalf("已是最新应失败: code=%d err=%s", code, errOut)
	}
}
