// Package cli 是 host/cli 适配器:把 ada 的命令实现从 main 中分离出来,
// 以 io.Writer 注入输出,便于测试与嵌入其它宿主。
//
// 它只做平台映射(参数解析、退出码、文本输出),业务仍在内核/插件/基础设施里。
package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/adag/ada-go/v2/assemble"
	"github.com/adag/ada-go/v2/install"
	"github.com/adag/ada-go/v2/kernel"
	"github.com/adag/ada-go/v2/loader"
	"github.com/adag/ada-go/v2/plugin"
	"github.com/adag/ada-go/v2/preset"
)

// Run 执行一条命令并返回退出码:0 成功 / 1 计划或装配失败 / 2 用法错误。
func Run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		usage(errOut)
		return 2
	}
	switch args[0] {
	case "plan":
		return cmdPlan(args[1:], out, errOut)
	case "up":
		return cmdUp(args[1:], out, errOut)
	case "inspect":
		return cmdInspect(args[1:], out, errOut)
	case "verify":
		return cmdVerify(args[1:], out, errOut)
	case "install":
		return cmdInstall(args[1:], out, errOut)
	case "list":
		return cmdList(args[1:], out, errOut)
	default:
		usage(errOut)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `用法:
  ada plan    <插件目录|--preset 文件> [-json]
  ada up      <插件目录|--preset 文件>
  ada inspect <插件目录|--preset 文件> [-json]
  ada verify  <插件目录> [-keys 密钥文件] [-require-signature]
  ada install <插件目录> -dest <安装根> [-keys 密钥文件] [-require-signature] [-yes]
  ada list    -dest <安装根>

退出码:0=成功,1=计划不可执行/装配或校验失败,2=用法错误。
`)
}

type target struct {
	dir    string
	preset string
	json   bool
	bad    string
}

func parseTarget(args []string) target {
	var t target
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-json":
			t.json = true
		case a == "--preset" || a == "-preset":
			if i+1 >= len(args) {
				t.bad = "--preset 缺少文件参数"
				return t
			}
			t.preset = args[i+1]
			i++
		case strings.HasPrefix(a, "-"):
			t.bad = "未知参数 " + a
			return t
		default:
			if t.dir == "" {
				t.dir = a
			} else {
				t.bad = "多余的参数 " + a
				return t
			}
		}
	}
	if t.dir != "" && t.preset != "" {
		t.bad = "目录与 --preset 不能同时给出"
	}
	return t
}

func loadTarget(t target) ([]*plugin.Manifest, error) {
	switch {
	case t.preset != "":
		p, err := preset.Load(t.preset)
		if err != nil {
			return nil, err
		}
		return p.Expand()
	case t.dir != "":
		return plugin.LoadDir(t.dir)
	default:
		return nil, fmt.Errorf("需要给出插件目录或 --preset 文件")
	}
}

// mountAll 是 up 与 inspect 共用的挂载逻辑:失败时停掉半装的 app。
func mountAll(manifests []*plugin.Manifest, report *assemble.Report, out io.Writer) (*kernel.App, *assemble.UpReport, error) {
	logger := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelWarn}))
	app := kernel.New(kernel.Options{Logger: logger})
	lookup := loader.Lookup(loader.Options{
		// CLI 没有编译期的 Go 插件:kind=go 会在这里得到明确错误。
		Logf: func(format string, args ...any) {
			fmt.Fprintf(out, "  [js] %s\n", fmt.Sprintf(format, args...))
		},
	})
	up, err := assemble.Up(app, manifests, report, lookup)
	if err != nil {
		_ = app.Stop(context.Background())
		return nil, nil, err
	}
	return app, up, nil
}

func cmdPlan(args []string, out, errOut io.Writer) int {
	t := parseTarget(args)
	if t.bad != "" {
		fmt.Fprintln(errOut, t.bad)
		usage(errOut)
		return 2
	}
	manifests, err := loadTarget(t)
	if err != nil {
		fmt.Fprintln(errOut, "加载失败:", err)
		return 1
	}
	report := assemble.Plan(manifests)

	if t.json {
		payload := struct {
			OK       bool               `json:"ok"`
			Plugins  []string           `json:"plugins"`
			Order    []string           `json:"order"`
			Warnings []string           `json:"warnings,omitempty"`
			Problems []assemble.Problem `json:"problems,omitempty"`
		}{OK: report.OK(), Order: report.Order, Warnings: report.Warnings, Problems: report.Problems}
		for _, m := range manifests {
			payload.Plugins = append(payload.Plugins, m.String())
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintln(errOut, "序列化失败:", err)
			return 1
		}
		fmt.Fprintln(out, string(b))
		if !report.OK() {
			return 1
		}
		return 0
	}

	names := make([]string, 0, len(manifests))
	for _, m := range manifests {
		names = append(names, m.String())
	}
	fmt.Fprintf(out, "清单(%d): %s\n", len(names), strings.Join(names, ", "))
	order := strings.Join(report.Order, " → ")
	if order == "" {
		order = "(空)"
	}
	fmt.Fprintln(out, "顺序:", order)
	for _, w := range report.Warnings {
		fmt.Fprintln(out, "警告:", w)
	}
	for _, p := range report.Problems {
		fmt.Fprintln(out, "问题:", p.String())
	}
	if !report.OK() {
		fmt.Fprintln(out, "结果: 不可装配")
		return 1
	}
	fmt.Fprintln(out, "结果: 可装配")
	return 0
}

func cmdUp(args []string, out, errOut io.Writer) int {
	t := parseTarget(args)
	if t.bad != "" {
		fmt.Fprintln(errOut, t.bad)
		usage(errOut)
		return 2
	}
	manifests, err := loadTarget(t)
	if err != nil {
		fmt.Fprintln(errOut, "加载失败:", err)
		return 1
	}
	report := assemble.Plan(manifests)
	if !report.OK() {
		for _, w := range report.Warnings {
			fmt.Fprintln(out, "警告:", w)
		}
		for _, p := range report.Problems {
			fmt.Fprintln(out, "问题:", p.String())
		}
		fmt.Fprintln(out, "结果: 不可装配")
		return 1
	}

	app, up, err := mountAll(manifests, report, out)
	if err != nil {
		fmt.Fprintln(errOut, "装配失败:", err)
		return 1
	}
	ctx := context.Background()

	fmt.Fprintln(out, "装配完成:", strings.Join(up.Order, " → "))
	snap := app.Inspect()
	for _, f := range snap.Fibers {
		fmt.Fprintf(out, "  fiber   %-12s %s\n", f.Plugin, f.State)
	}
	for _, s := range snap.Services {
		fmt.Fprintf(out, "  service %-12s provider=%s dependents=%d\n", s.Name, s.Provider, len(s.Dependents))
	}

	if err := app.Stop(ctx); err != nil {
		fmt.Fprintln(errOut, "停止时出错:", err)
		return 1
	}
	fmt.Fprintln(out, "已停止。")
	return 0
}

type jsonFiber struct {
	ID     string `json:"id"`
	Plugin string `json:"plugin"`
	State  string `json:"state"`
	Epoch  uint64 `json:"epoch"`
	Parent string `json:"parent,omitempty"`
}

type jsonService struct {
	Name       string   `json:"name"`
	Version    int      `json:"version"`
	Kind       string   `json:"kind"`
	Provider   string   `json:"provider"`
	Dependents []string `json:"dependents,omitempty"`
}

type jsonEffect struct {
	Fiber  string `json:"fiber"`
	Plugin string `json:"plugin"`
	Label  string `json:"label"`
}

func cmdInspect(args []string, out, errOut io.Writer) int {
	t := parseTarget(args)
	if t.bad != "" {
		fmt.Fprintln(errOut, t.bad)
		usage(errOut)
		return 2
	}
	manifests, err := loadTarget(t)
	if err != nil {
		fmt.Fprintln(errOut, "加载失败:", err)
		return 1
	}
	report := assemble.Plan(manifests)
	if !report.OK() {
		for _, w := range report.Warnings {
			fmt.Fprintln(out, "警告:", w)
		}
		for _, p := range report.Problems {
			fmt.Fprintln(out, "问题:", p.String())
		}
		fmt.Fprintln(out, "结果: 不可装配")
		return 1
	}

	app, up, err := mountAll(manifests, report, out)
	if err != nil {
		fmt.Fprintln(errOut, "装配失败:", err)
		return 1
	}
	defer func() { _ = app.Stop(context.Background()) }()
	snap := app.Inspect()

	if t.json {
		payload := struct {
			Order    []string      `json:"order"`
			Fibers   []jsonFiber   `json:"fibers"`
			Services []jsonService `json:"services"`
			Effects  []jsonEffect  `json:"effects"`
			Events   []string      `json:"events"`
		}{Order: up.Order, Events: snap.Events}
		for _, f := range snap.Fibers {
			payload.Fibers = append(payload.Fibers, jsonFiber{ID: f.ID, Plugin: f.Plugin, State: f.State, Epoch: f.Epoch, Parent: f.Parent})
		}
		for _, s := range snap.Services {
			payload.Services = append(payload.Services, jsonService{Name: s.Name, Version: s.Version, Kind: s.Kind, Provider: s.Provider, Dependents: s.Dependents})
		}
		for _, e := range snap.Effects {
			payload.Effects = append(payload.Effects, jsonEffect{Fiber: e.Fiber, Plugin: e.Plugin, Label: e.Label})
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintln(errOut, "序列化失败:", err)
			return 1
		}
		fmt.Fprintln(out, string(b))
		return 0
	}

	fmt.Fprintln(out, "顺序:", strings.Join(up.Order, " → "))
	fmt.Fprintln(out, "fibers:")
	for _, f := range snap.Fibers {
		parent := f.Parent
		if parent == "" {
			parent = "-"
		}
		fmt.Fprintf(out, "  %-12s %-10s epoch=%-8d parent=%s\n", f.Plugin, f.State, f.Epoch, parent)
	}
	fmt.Fprintln(out, "services:")
	for _, s := range snap.Services {
		fmt.Fprintf(out, "  %-12s v%d provider=%s dependents=%d\n", s.Name, s.Version, s.Provider, len(s.Dependents))
	}
	fmt.Fprintln(out, "effects:")
	for _, e := range snap.Effects {
		fmt.Fprintf(out, "  %-12s %s\n", e.Plugin, e.Label)
	}
	fmt.Fprintln(out, "events:", strings.Join(snap.Events, ", "))
	return 0
}

type keyFileEntry struct {
	Public string `json:"public"`
	Trust  string `json:"trust"`
}

func loadKeyRing(path string) (map[string]install.Key, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]keyFileEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]install.Key, len(raw))
	for id, entry := range raw {
		pub, err := base64.StdEncoding.DecodeString(entry.Public)
		if err != nil {
			return nil, fmt.Errorf("密钥 %q 不是合法 base64: %w", id, err)
		}
		trust, err := install.TrustFromKey(entry.Trust)
		if err != nil {
			return nil, err
		}
		out[id] = install.Key{Public: ed25519.PublicKey(pub), Trust: trust}
	}
	return out, nil
}

func parseVerifyArgs(args []string) (dir, keys string, require bool, bad string) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-keys":
			if i+1 >= len(args) {
				return "", "", false, "-keys 缺少文件参数"
			}
			keys = args[i+1]
			i++
		case a == "-require-signature":
			require = true
		case strings.HasPrefix(a, "-"):
			return "", "", false, "未知参数 " + a
		default:
			if dir != "" {
				return "", "", false, "多余的参数 " + a
			}
			dir = a
		}
	}
	if dir == "" {
		return "", "", false, "需要给出插件目录"
	}
	return dir, keys, require, ""
}

func cmdVerify(args []string, out, errOut io.Writer) int {
	dir, keysPath, require, bad := parseVerifyArgs(args)
	if bad != "" {
		fmt.Fprintln(errOut, bad)
		usage(errOut)
		return 2
	}
	keys, err := loadKeyRing(keysPath)
	if err != nil {
		fmt.Fprintln(errOut, "加载密钥失败:", err)
		return 1
	}
	info, err := install.VerifyDir(dir, install.Options{Keys: keys, RequireSignature: require})
	if err != nil {
		fmt.Fprintln(errOut, "校验失败:", err)
		return 1
	}
	capList := strings.Join(info.Manifest.Caps, ", ")
	if capList == "" {
		capList = "(无)"
	}
	fmt.Fprintf(out, "插件: %s@%s(%s)\n摘要: %s\n信任: %s\n已签名: %v\n能力: %s\n",
		info.ID, info.Version, info.Kind, info.Digest, info.Trust, info.Signed, capList)
	return 0
}

// parseInstallArgs 解析 install 参数;-dest 是 install 专属,不能走 verify 的参数解析。
// cmdList 列出安装根目录的索引。
func cmdList(args []string, out, errOut io.Writer) int {
	dest := ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-dest":
			if i+1 >= len(args) {
				fmt.Fprintln(errOut, "-dest 缺少目录参数")
				usage(errOut)
				return 2
			}
			dest = args[i+1]
			i++
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(errOut, "未知参数 "+a)
			usage(errOut)
			return 2
		default:
			fmt.Fprintln(errOut, "多余的参数 "+a)
			usage(errOut)
			return 2
		}
	}
	if dest == "" {
		fmt.Fprintln(errOut, "缺少 -dest 安装根目录")
		usage(errOut)
		return 2
	}
	entries, err := install.List(dest)
	if err != nil {
		fmt.Fprintln(errOut, "读取索引失败:", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "已安装插件: (空)")
		return 0
	}
	fmt.Fprintf(out, "已安装插件(%d):\n", len(entries))
	for _, e := range entries {
		capsList := strings.Join(e.Caps, ", ")
		if capsList == "" {
			capsList = "(无)"
		}
		fmt.Fprintf(out, "  %s@%s trust=%s signed=%v caps=%s\n", e.ID, e.Version, e.Trust, e.Signed, capsList)
	}
	return 0
}

func parseInstallArgs(args []string) (dir, dest, keys string, require, yes bool, bad string) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-dest":
			if i+1 >= len(args) {
				return "", "", "", false, false, "-dest 缺少目录参数"
			}
			dest = args[i+1]
			i++
		case a == "-keys":
			if i+1 >= len(args) {
				return "", "", "", false, false, "-keys 缺少文件参数"
			}
			keys = args[i+1]
			i++
		case a == "-require-signature":
			require = true
		case a == "-yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return "", "", "", false, false, "未知参数 " + a
		default:
			if dir != "" {
				return "", "", "", false, false, "多余的参数 " + a
			}
			dir = a
		}
	}
	if dir == "" {
		return "", "", "", false, false, "需要给出插件目录"
	}
	if dest == "" {
		return "", "", "", false, false, "缺少 -dest 安装根目录"
	}
	return dir, dest, keys, require, yes, ""
}

func cmdInstall(args []string, out, errOut io.Writer) int {
	dir, dest, keysPath, require, yes, bad := parseInstallArgs(args)
	if bad != "" {
		fmt.Fprintln(errOut, bad)
		usage(errOut)
		return 2
	}
	keys, err := loadKeyRing(keysPath)
	if err != nil {
		fmt.Fprintln(errOut, "加载密钥失败:", err)
		return 1
	}
	// 安装同意:先只校验(不落盘),展示能力;没有 -yes 就拒绝。
	preview, err := install.VerifyDir(dir, install.Options{Keys: keys, RequireSignature: require})
	if err != nil {
		fmt.Fprintln(errOut, "校验失败:", err)
		return 1
	}
	if len(preview.Manifest.Caps) > 0 && !yes {
		fmt.Fprintf(out, "插件: %s@%s(信任 %s,已签名 %v)\n能力: %s\n",
			preview.ID, preview.Version, preview.Trust, preview.Signed, strings.Join(preview.Manifest.Caps, ", "))
		fmt.Fprintln(errOut, "该插件声明了能力,需要 -yes 确认安装")
		return 2
	}
	info, err := install.Install(dir, dest, install.Options{Keys: keys, RequireSignature: require})
	if err != nil {
		fmt.Fprintln(errOut, "安装失败:", err)
		return 1
	}
	fmt.Fprintf(out, "已安装: %s@%s → %s(信任 %s,已签名 %v)\n", info.ID, info.Version, info.Dir, info.Trust, info.Signed)
	return 0
}
