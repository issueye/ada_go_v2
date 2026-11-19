// 命令 ada 是 M3 的装配工具。当前只有 plan;install / inspect 属于 M5。
//
// 它只读清单、算计划,不挂载任何插件——装配执行在 assemble.Up,由宿主调用。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/adag/ada-go/v2/assemble"
	"github.com/adag/ada-go/v2/plugin"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "plan":
		os.Exit(cmdPlan(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `用法:
  ada plan <插件目录> [-json]

扫描目录下每个子目录里的 plugin.json,输出装配顺序、警告与问题。
退出码:0=可装配,1=不可装配或加载失败,2=用法错误。
`)
}

func cmdPlan(args []string) int {
	jsonOut := false
	var dir string
	for _, a := range args {
		if a == "-json" {
			jsonOut = true
			continue
		}
		if dir == "" {
			dir = a
		}
	}
	if dir == "" {
		usage()
		return 2
	}

	manifests, err := plugin.LoadDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载清单失败:", err)
		return 1
	}
	report := assemble.Plan(manifests)

	if jsonOut {
		payload := struct {
			OK       bool               `json:"ok"`
			Plugins  []string           `json:"plugins"`
			Order    []string           `json:"order"`
			Warnings []string           `json:"warnings,omitempty"`
			Problems []assemble.Problem `json:"problems,omitempty"`
		}{
			OK: report.OK(), Order: report.Order,
			Warnings: report.Warnings, Problems: report.Problems,
		}
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
