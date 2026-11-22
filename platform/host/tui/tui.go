// Package tui 是终端交互宿主适配器:装配 agent 栈,用行式输入驱动同一轮 agent 循环。
//
// 为什么不是插件:TUI 是终端的窗口/输入映射(AGENTS §2.2 的宿主适配器),
// 不定义 agent 语义;业务仍由 preset 里的 core/provider/policy 插件承担。
package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/issueye/ada_go_v2/platform/assemble"
	"github.com/issueye/ada_go_v2/platform/host/builtins"
	"github.com/issueye/ada_go_v2/platform/install"
	"github.com/issueye/ada_go_v2/platform/kernel"
	"github.com/issueye/ada_go_v2/platform/loader"
	"github.com/issueye/ada_go_v2/platform/plugin"
	"github.com/issueye/ada_go_v2/platform/preset"
	agentloop "github.com/issueye/ada_go_v2/plugins/core/agent-loop"
	seamsession "github.com/issueye/ada_go_v2/plugins/seam/session"
	"github.com/issueye/ada_go_v2/plugins/seam/workspace"
)

// 退出码与 CLI 一致:0 成功 / 1 装配或运行失败 / 2 用法错误。
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

type options struct {
	dir       string
	preset    string
	workspace string
	plain     bool
}

// Run 启动 TUI。in/out/errOut 注入是为了测试;真实入口用 os.Stdin/Stdout/Stderr。
func Run(args []string, in io.Reader, out, errOut io.Writer) int {
	opts, bad := parseArgs(args)
	if bad != "" {
		fmt.Fprintln(errOut, bad)
		fmt.Fprint(errOut, usage)
		return exitUsage
	}
	manifests, err := loadManifests(opts)
	if err != nil {
		fmt.Fprintln(errOut, "加载失败:", err)
		return exitFail
	}
	report := assemble.Plan(manifests)
	if !report.OK() {
		for _, p := range report.Problems {
			fmt.Fprintln(errOut, "问题:", p.String())
		}
		return exitFail
	}
	app, err := mount(manifests, report)
	if err != nil {
		fmt.Fprintln(errOut, "装配失败:", err)
		return exitFail
	}
	defer func() { _ = app.Stop(context.Background()) }()

	ctx := context.Background()
	raw, err := app.Root().Service("agent")
	if err != nil {
		fmt.Fprintln(errOut, "没有 agent 服务:", err)
		return exitFail
	}
	ag, ok := raw.(*agentloop.Agent)
	if !ok {
		fmt.Fprintln(errOut, "agent 服务类型不正确")
		return exitFail
	}

	meta := seamsession.Meta{Title: "tui"}
	chain := workspaceChain(app)
	if opts.workspace != "" {
		if err := validateWorkspace(ctx, chain, opts.workspace); err != nil {
			fmt.Fprintln(errOut, "工作区不可用:", err)
			return exitFail
		}
		meta.Workspace = opts.workspace
	}
	sess, err := ag.Open(ctx, meta)
	if err != nil {
		fmt.Fprintln(errOut, "打开会话失败:", err)
		return exitFail
	}

	v := view{out: out, plain: opts.plain}
	v.clear()
	fmt.Fprintln(out, v.bold("ada tui"))
	fmt.Fprintln(out, "命令: /help  /workspace <id>  /session  /clear  /quit")
	fmt.Fprintln(out, "工作区:", workspaceLabel(ctx, chain, sess))
	fmt.Fprintln(out)
	fmt.Fprint(out, v.prompt())

	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
		case line == "/quit" || line == "/exit":
			fmt.Fprintln(out, "bye")
			return exitOK
		case line == "/help":
			fmt.Fprintln(out, "\n命令: /workspace <id> 切换工作区并开新会话; /session 显示会话; /clear 清屏; /quit 退出")
		case line == "/clear":
			v.clear()
			fmt.Fprintln(out, v.bold("ada tui"))
		case line == "/session":
			fmt.Fprintf(out, "\n会话 %s,工作区 %s\n", sess.ID(), workspaceLabel(ctx, chain, sess))
		case line == "/workspace" || strings.HasPrefix(line, "/workspace "):
			if !strings.HasPrefix(line, "/workspace ") {
				fmt.Fprintf(out, "\n工作区: %s\n", workspaceLabel(ctx, chain, sess))
				break
			}
			id := strings.TrimSpace(strings.TrimPrefix(line, "/workspace "))
			if err := validateWorkspace(ctx, chain, id); err != nil {
				fmt.Fprintln(out, "工作区不可用:", err)
				break
			}
			next, err := ag.Open(ctx, seamsession.Meta{Title: "tui", Workspace: id})
			if err != nil {
				fmt.Fprintln(out, "打开会话失败:", err)
				break
			}
			sess = next
			fmt.Fprintf(out, "\n已切换到工作区 %s,新会话 %s\n", id, sess.ID())
		case strings.HasPrefix(line, "/"):
			fmt.Fprintln(out, "未知命令:", line)
		default:
			res, err := ag.Run(ctx, sess, line)
			if err != nil {
				fmt.Fprintln(errOut, "运行失败:", err)
			} else {
				fmt.Fprintf(out, "\n%s\n", res.Text)
			}
		}
		fmt.Fprint(out, v.prompt())
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(errOut, "读取输入失败:", err)
		return exitFail
	}
	return exitOK
}

// parseArgs 解析 `ada-tui <目录|--preset 文件> [--workspace id] [--plain]`。
func parseArgs(args []string) (options, string) {
	var o options
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--preset" || a == "-preset":
			if i+1 >= len(args) {
				return o, "--preset 缺少文件参数"
			}
			o.preset = args[i+1]
			i++
		case a == "--workspace" || a == "-workspace":
			if i+1 >= len(args) {
				return o, "--workspace 缺少工作区 id"
			}
			o.workspace = args[i+1]
			i++
		case a == "--plain" || a == "-plain":
			o.plain = true
		case strings.HasPrefix(a, "-"):
			return o, "未知参数 " + a
		default:
			if o.dir != "" {
				return o, "多余的参数 " + a
			}
			o.dir = a
		}
	}
	if o.dir != "" && o.preset != "" {
		return o, "目录与 --preset 不能同时给出"
	}
	if o.dir == "" && o.preset == "" {
		return o, "需要给出插件目录或 --preset 文件"
	}
	return o, ""
}

func loadManifests(o options) ([]*plugin.Manifest, error) {
	if o.preset != "" {
		p, err := preset.Load(o.preset)
		if err != nil {
			return nil, err
		}
		return p.Expand()
	}
	if _, err := os.Stat(filepath.Join(o.dir, install.IndexFile)); err == nil {
		return install.LoadDir(o.dir)
	}
	return plugin.LoadDir(o.dir)
}

func mount(manifests []*plugin.Manifest, report *assemble.Report) (*kernel.App, error) {
	if err := builtins.ApplyAutoGrants(manifests); err != nil {
		return nil, err
	}
	app := kernel.New(kernel.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	lookup := loader.Lookup(loader.Options{
		Go:   builtins.Plugins(),
		Logf: func(string, ...any) {},
	})
	if _, err := assemble.Up(app, manifests, report, lookup); err != nil {
		_ = app.Stop(context.Background())
		return nil, err
	}
	return app, nil
}

func workspaceChain(app *kernel.App) *workspace.Chain {
	v, ok := app.Root().TryService(workspace.ServiceName)
	if !ok {
		return nil
	}
	c, _ := v.(*workspace.Chain)
	return c
}

func validateWorkspace(ctx context.Context, chain *workspace.Chain, id string) error {
	if chain == nil {
		return fmt.Errorf("preset 没有 workspace 服务")
	}
	svc, err := chain.Get()
	if err != nil {
		return err
	}
	_, err = svc.Get(ctx, workspace.ID(id))
	return err
}

func workspaceLabel(ctx context.Context, chain *workspace.Chain, sess seamsession.Session) string {
	id := sess.Meta().Workspace
	if id != "" {
		return id
	}
	if chain == nil {
		return "-"
	}
	svc, err := chain.Get()
	if err != nil {
		return "-"
	}
	cur, err := svc.Current(ctx)
	if err != nil {
		return "-"
	}
	return string(cur.ID)
}

// view 只做终端样式;布局与文案仍由宿主掌握。
type view struct {
	out   io.Writer
	plain bool
}

func (v view) clear() {
	if !v.plain {
		fmt.Fprint(v.out, "\x1b[2J\x1b[H")
	}
}

func (v view) bold(s string) string {
	if v.plain {
		return s
	}
	return "\x1b[1;36m" + s + "\x1b[0m"
}

func (v view) prompt() string {
	if v.plain {
		return "> "
	}
	return "\x1b[32m> \x1b[0m"
}

const usage = `用法:
  ada-tui <插件目录|--preset 文件> [--workspace <id>] [--plain]
`
