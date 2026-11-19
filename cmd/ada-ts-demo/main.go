// 命令 ada-ts-demo 演示 M2 的第一段闭环:
// 一份 TS 插件(带 enum、参数属性、跨文件导入)被 esbuild 降级、被 moejs 在进程内加载,
// 然后作为普通 kernel 插件装配进来——全程没有 Node,也没有第二个进程。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/adag/ada-go/v2/bridge"
	"github.com/adag/ada-go/v2/compile"
	"github.com/adag/ada-go/v2/kernel"
)

const pluginTS = `import { format } from "./src/util"
export enum Level { High = 3 }
export function apply(cfg: { name: string }) {
  __adaLog(format(cfg.name))
  return { ok: true, level: Level.High }
}`

const utilTS = `export function format(name: string): string { return "你好," + name }`

func main() {
	artifact, err := compile.Build("ts-demo", "plugin.ts", map[string]string{
		"plugin.ts":   pluginTS,
		"src/util.ts": utilTS,
	}, compile.Options{})
	if err != nil {
		fmt.Println("编译失败:", err)
		os.Exit(1)
	}
	fmt.Printf("Artifact: entry=%s files=%d digest=%s\n", artifact.Entry, len(artifact.Files), artifact.Digest[:19]+"…")

	plugin, err := bridge.New(bridge.Options{
		Name:     "ts-demo",
		Artifact: artifact,
		Logf:     func(format string, args ...any) { fmt.Printf("[js] "+format+"\n", args...) },
	})
	if err != nil {
		fmt.Println("构造插件失败:", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	app := kernel.New(kernel.Options{Logger: logger})
	defer func() { _ = app.Stop(context.Background()) }()

	fiber, err := app.Plugin(plugin, kernel.NewConfig(map[string]any{"name": "ada"}))
	if err != nil {
		fmt.Println("挂载失败:", err)
		os.Exit(1)
	}
	fmt.Println("fiber 状态:", fiber.State())
	if fiber.State() != kernel.Active {
		os.Exit(1)
	}

	if err := fiber.Dispose(context.Background()); err != nil {
		fmt.Println("卸载失败:", err)
	}
	fmt.Println("卸载后:", fiber.State())
}
