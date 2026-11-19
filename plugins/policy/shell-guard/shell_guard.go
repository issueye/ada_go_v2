// Package shellguard 是 shell 的策略装饰器:只读模式白名单、shell 模式默认拒绝。
//
// 与 fs-sandbox 同构:安全判定必须在装饰器里同步发生,不能依赖事件。
package shellguard

import (
	"context"
	"fmt"

	"github.com/adag/ada-go/v2/kernel"
	"github.com/adag/ada-go/v2/plugins/seam/seamerr"
	seamshell "github.com/adag/ada-go/v2/plugins/seam/shell"
)

// PolicyName 是插件 id。
const PolicyName = "shell-guard"

// Priority 是装饰器优先级(高者在外层)。
const Priority = 100

// Mode 是会话沙箱模式。
type Mode string

const (
	ReadOnly       Mode = "read-only"
	WorkspaceWrite Mode = "workspace-write"
	Danger         Mode = "danger-full-access"
)

// New 构造装饰器(测试可直接调用)。
func New(inner seamshell.Shell, mode Mode, allow []string, allowShell bool) seamshell.Shell {
	allowed := make(map[string]bool, len(allow))
	for _, p := range allow {
		allowed[p] = true
	}
	return &guard{inner: inner, mode: mode, allow: allowed, allowShell: allowShell}
}

type guard struct {
	inner      seamshell.Shell
	mode       Mode
	allow      map[string]bool
	allowShell bool
}

func (g *guard) Env() seamshell.Env { return g.inner.Env() }

func (g *guard) Run(ctx context.Context, cmd seamshell.Command) (seamshell.Result, error) {
	if err := g.check(cmd); err != nil {
		return seamshell.Result{}, err
	}
	return g.inner.Run(ctx, cmd)
}

func (g *guard) Stream(ctx context.Context, cmd seamshell.Command, onChunk func(seamshell.Chunk) error) (seamshell.Result, error) {
	if err := g.check(cmd); err != nil {
		return seamshell.Result{}, err
	}
	return g.inner.Stream(ctx, cmd, onChunk)
}

func (g *guard) check(cmd seamshell.Command) error {
	if cmd.Shell && !g.allowShell {
		return seamshell.Denied("sandbox", fmt.Sprintf("shell 模式未授权,拒绝执行 %q", cmd.Program))
	}
	switch g.mode {
	case ReadOnly:
		if !g.allow[cmd.Program] {
			return seamshell.Denied("sandbox", fmt.Sprintf("只读模式只允许白名单命令,拒绝 %q", cmd.Program))
		}
		return nil
	case WorkspaceWrite, Danger:
		return nil
	default:
		return seamshell.Denied("sandbox", fmt.Sprintf("未知沙箱模式 %q,按拒绝处理", g.mode))
	}
}

// Plugin 返回 shell-guard 插件:inject shell seam,注册一个策略装饰器。
func Plugin() kernel.Plugin {
	return kernel.Func(PolicyName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamshell.Chain](ctx, "shell")
		if err != nil {
			return nil, err
		}
		mode := Mode(cfg.String("mode"))
		if mode == "" {
			mode = ReadOnly
		}
		switch mode {
		case ReadOnly, WorkspaceWrite, Danger:
		default:
			return nil, seamerr.New(seamerr.ErrInvalid, PolicyName, fmt.Sprintf("未知 mode %q", mode), false)
		}
		return chain.Decorate(func(inner seamshell.Shell) seamshell.Shell {
			return New(inner, mode, cfg.Strings("allow"), cfg.Bool("allowShell"))
		}, Priority)
	}).WithInject("shell")
}
