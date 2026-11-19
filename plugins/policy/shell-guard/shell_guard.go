// Package shellguard 是 shell 的策略装饰器:先问 sandbox,再做白名单与 shell 门。
//
// 判定拆成三层:沙箱(操作是否允许)→ shell 门(是否显式授权 shell 模式)→
// 只读白名单(读操作允许哪些程序)。这样可以复用 sandbox 后端,而不是各写一套模式。
package shellguard

import (
	"context"
	"fmt"
	"strings"

	"github.com/adag/ada-go/v2/kernel"
	seamsandbox "github.com/adag/ada-go/v2/plugins/seam/sandbox"
	seamshell "github.com/adag/ada-go/v2/plugins/seam/shell"
)

// PolicyName 是插件 id。
const PolicyName = "shell-guard"

// Priority 是装饰器优先级(高者在外层)。
const Priority = 100

// New 构造装饰器。
func New(inner seamshell.Shell, registry *seamsandbox.Registry, backend string, allow []string, allowShell bool) seamshell.Shell {
	allowed := make(map[string]bool, len(allow))
	for _, p := range allow {
		allowed[p] = true
	}
	return &guard{inner: inner, registry: registry, backend: backend, allow: allowed, allowShell: allowShell}
}

type guard struct {
	inner      seamshell.Shell
	registry   *seamsandbox.Registry
	backend    string
	allow      map[string]bool
	allowShell bool
}

func (g *guard) Env() seamshell.Env { return g.inner.Env() }

func (g *guard) Run(ctx context.Context, cmd seamshell.Command) (seamshell.Result, error) {
	if err := g.check(ctx, cmd); err != nil {
		return seamshell.Result{}, err
	}
	return g.inner.Run(ctx, cmd)
}

func (g *guard) Stream(ctx context.Context, cmd seamshell.Command, onChunk func(seamshell.Chunk) error) (seamshell.Result, error) {
	if err := g.check(ctx, cmd); err != nil {
		return seamshell.Result{}, err
	}
	return g.inner.Stream(ctx, cmd, onChunk)
}

func (g *guard) check(ctx context.Context, cmd seamshell.Command) error {
	if g.registry == nil {
		return seamshell.Denied("sandbox", "sandbox 服务不可用")
	}
	sb, err := g.registry.Get(g.backend)
	if err != nil {
		return seamshell.Denied("sandbox", "没有可用的沙箱后端: "+err.Error())
	}
	decision, err := sb.Resolve(ctx, seamsandbox.Request{
		Op: seamsandbox.OpShellExec, Program: cmd.Program, Args: cmd.Args,
	})
	if err != nil {
		return err
	}
	if !decision.Allow {
		reason := strings.Join(decision.Reasons, "; ")
		if reason == "" {
			reason = "被沙箱拒绝"
		}
		return seamshell.Denied("sandbox", reason)
	}

	if cmd.Shell && !g.allowShell {
		return seamshell.Denied("sandbox", fmt.Sprintf("shell 模式未授权,拒绝执行 %q", cmd.Program))
	}
	if sb.Mode(ctx, "") == seamsandbox.ModeReadOnly && !g.allow[cmd.Program] {
		return seamshell.Denied("sandbox", fmt.Sprintf("只读模式只允许白名单命令,拒绝 %q", cmd.Program))
	}
	return nil
}

// Plugin 返回 shell-guard 插件:inject shell 与 sandbox,注册策略装饰器。
func Plugin() kernel.Plugin {
	return kernel.Func(PolicyName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamshell.Chain](ctx, "shell")
		if err != nil {
			return nil, err
		}
		registry, err := kernel.Service[*seamsandbox.Registry](ctx, "sandbox")
		if err != nil {
			return nil, err
		}
		backend := cfg.String("backend")
		return chain.Decorate(func(inner seamshell.Shell) seamshell.Shell {
			return New(inner, registry, backend, cfg.Strings("allow"), cfg.Bool("allowShell"))
		}, Priority)
	}).WithInject("shell", "sandbox")
}
