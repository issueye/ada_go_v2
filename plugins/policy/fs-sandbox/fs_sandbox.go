// Package fssandbox 是 fs 的沙箱策略装饰器:按会话模式拒绝写/越界。
//
// 它属于 policy(装饰器)而不是 provider:安全闸必须同步、可排序、可拒;
// 用事件做强制会在监听器 panic/不调 next 时失效(seam-api-security §1.4)。
package fssandbox

import (
	"context"
	"fmt"
	"strings"

	"github.com/adag/ada-go/v2/kernel"
	seamfs "github.com/adag/ada-go/v2/plugins/seam/fs"
)

// PolicyName 是插件 id。
const PolicyName = "fs-sandbox"

// Mode 是会话沙箱模式。
type Mode string

const (
	// ReadOnly 只读:允许读,拒绝写。
	ReadOnly Mode = "read-only"
	// WorkspaceWrite 只允许写 /workspace 与 /tmp。
	WorkspaceWrite Mode = "workspace-write"
	// Danger 允许写(插件目录仍受保护)。
	Danger Mode = "danger-full-access"
)

// Priority 是装饰器优先级:高者在外层(先判)。
const Priority = 100

// New 构造装饰器(测试可直接调用)。
func New(inner seamfs.FS, mode Mode) seamfs.FS {
	return &sandbox{inner: inner, mode: mode}
}

type sandbox struct {
	inner seamfs.FS
	mode  Mode
}

func (s *sandbox) Stat(ctx context.Context, p seamfs.Path) (seamfs.FileInfo, error) {
	return s.inner.Stat(ctx, p)
}

func (s *sandbox) List(ctx context.Context, req seamfs.ListRequest) ([]seamfs.DirEntry, error) {
	return s.inner.List(ctx, req)
}

func (s *sandbox) Read(ctx context.Context, req seamfs.ReadRequest) (seamfs.ReadResult, error) {
	return s.inner.Read(ctx, req)
}

func (s *sandbox) Search(ctx context.Context, req seamfs.SearchRequest) (seamfs.SearchResult, error) {
	return s.inner.Search(ctx, req)
}

func (s *sandbox) Write(ctx context.Context, req seamfs.WriteRequest) (seamfs.WriteResult, error) {
	if err := s.check(req.Path); err != nil {
		return seamfs.WriteResult{}, err
	}
	return s.inner.Write(ctx, req)
}

func (s *sandbox) Edit(ctx context.Context, req seamfs.EditRequest) (seamfs.EditResult, error) {
	if err := s.check(req.Path); err != nil {
		return seamfs.EditResult{}, err
	}
	return s.inner.Edit(ctx, req)
}

// check 是写路径的唯一判定点。
func (s *sandbox) check(p seamfs.Path) error {
	path := string(p)
	switch s.mode {
	case ReadOnly:
		return seamfs.Denied("sandbox", fmt.Sprintf("当前会话只读,拒绝写 %s", path))
	case WorkspaceWrite:
		if strings.HasPrefix(path, "/workspace/") || path == "/workspace" ||
			strings.HasPrefix(path, "/tmp/") || path == "/tmp" {
			return nil
		}
		return seamfs.Denied("sandbox", fmt.Sprintf("workspace-write 只允许写 /workspace 与 /tmp,拒绝 %s", path))
	case Danger:
		// 即使危险模式,已安装插件目录仍受保护(§1.3)。
		if strings.HasPrefix(path, "/plugin/") || path == "/plugin" {
			return seamfs.Denied("sandbox", fmt.Sprintf("插件目录受保护,拒绝写 %s", path))
		}
		return nil
	default:
		return seamfs.Denied("sandbox", fmt.Sprintf("未知沙箱模式 %q,按拒绝处理", s.mode))
	}
}

// Plugin 返回 fs-sandbox 插件:inject fs seam,注册一个沙箱装饰器。
func Plugin() kernel.Plugin {
	return kernel.Func(PolicyName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamfs.Chain](ctx, "fs")
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
			return nil, seamfs.Invalid(fmt.Sprintf("fs-sandbox: 未知 mode %q", mode))
		}
		return chain.Decorate(func(inner seamfs.FS) seamfs.FS {
			return New(inner, mode)
		}, Priority)
	}).WithInject("fs")
}
