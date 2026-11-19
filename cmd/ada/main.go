// 命令 ada 是 M3 的装配工具。
//
//	ada plan <目录>              读清单、算依赖图、输出顺序与问题(不挂载)
//	ada plan --preset <文件>      按 preset(多目录 + disabled + config patch)算计划
//	ada up   <目录|--preset ...>  真的挂载一遍(Go 插件需宿主注册;TS 插件现场编译),打印后停止
//
// install / 签名 / capability 属于 M5;inspect 尚未实现。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/adag/ada-go/v2/assemble"
	"github.com/adag/ada-go/v2/kernel"
	"github.com/adag/ada-go/v2/loader"
	"github.com/adag/ada-go/v2/plugin"
	"github.com/adag/ada-go/v2/preset"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "plan":
		os.Exit(cmdPlan(os.Args[2:]))
	case "up":
		os.Exit(cmdUp(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `用法:
  ada plan <插件目录> [-json]          读清单→依赖图→装配顺序与问题(不挂载)
  ada plan --preset <文件> [-json]     按 preset 装配(多目录 + disabled + config patch)
  ada up   <插件目录|--preset 文件>     挂载全部插件,打印 fiber/服务后停止

退出码:0=成功,1=计划不可执行或装配失败,2=用法错误。
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

func cmdPlan(args []string) int {
	t := parseTarget(args)
	if t.bad != "" {
		fmt.Fprintln(os.Stderr, t.bad)
		usage()
		return 2
	}
	manifests, err := loadTarget(t)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载失败:", err)
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
			fmt.Fprintln(os.Stderr, "序列化失败:", err)
			return 1
		}
		fmt.Println(string(b))
		if !report.OK() {
			return 1
		}
		return 0
	}

	names := make([]string, 0, len(manifests))
	for _, m := range manifests {
		names = append(names, m.String())
	}
	fmt.Printf("清单(%d): %s\n", len(names), strings.Join(names, ", "))
	order := strings.Join(report.Order, " → ")
	if order == "" {
		order = "(空)"
	}
	fmt.Println("顺序:", order)
	for _, w := range report.Warnings {
		fmt.Println("警告:", w)
	}
	for _, p := range report.Problems {
		fmt.Println("问题:", p.String())
	}
	if !report.OK() {
		fmt.Println("结果: 不可装配")
		return 1
	}
	fmt.Println("结果: 可装配")
	return 0
}

func cmdUp(args []string) int {
	t := parseTarget(args)
	if t.bad != "" {
		fmt.Fprintln(os.Stderr, t.bad)
		usage()
		return 2
	}
	manifests, err := loadTarget(t)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载失败:", err)
		return 1
	}
	report := assemble.Plan(manifests)
	if !report.OK() {
		for _, w := range report.Warnings {
			fmt.Println("警告:", w)
		}
		for _, p := range report.Problems {
			fmt.Println("问题:", p.String())
		}
		fmt.Println("结果: 不可装配")
		return 1
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	app := kernel.New(kernel.Options{Logger: logger})
	ctx := context.Background()

	lookup := loader.Lookup(loader.Options{
		// CLI 没有编译期的 Go 插件:kind=go 会在这里得到明确错误。
		Logf: func(format string, args ...any) {
			fmt.Printf("  [js] %s\n", fmt.Sprintf(format, args...))
		},
	})

	up, err := assemble.Up(app, manifests, report, lookup)
	if err != nil {
		fmt.Fprintln(os.Stderr, "装配失败:", err)
		_ = app.Stop(ctx)
		return 1
	}

	fmt.Println("装配完成:", strings.Join(up.Order, " → "))
	snap := app.Inspect()
	for _, f := range snap.Fibers {
		fmt.Printf("  fiber   %-12s %s\n", f.Plugin, f.State)
	}
	for _, s := range snap.Services {
		fmt.Printf("  service %-12s provider=%s dependents=%d\n", s.Name, s.Provider, len(s.Dependents))
	}

	if err := app.Stop(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "停止时出错:", err)
		return 1
	}
	fmt.Println("已停止。")
	return 0
}
