// Package net 是 `seam/net` 的契约层:HTTP 抓取、响应限制与装饰器链。
//
// 契约来源:plugin-kernel-design §13(capability `net.fetch[.host]`)。seam 只放类型/错误/链,
// 实现由 provider 提供,策略(host 允许表、审批)由 policy 装饰器负责。
package net

import (
	"context"
	"sort"
	"sync"

	"github.com/adag/ada-go/v2/platform/kernel"
	"github.com/adag/ada-go/v2/plugins/seam/seamerr"
)

// Request 是一次抓取请求。
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// Response 是抓取结果。
type Response struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      []byte            `json:"body,omitempty"`
	URL       string            `json:"url"`
	Truncated bool              `json:"truncated,omitempty"`
}

// Client 是抓取契约。
type Client interface {
	Do(ctx context.Context, req Request) (Response, error)
}

type decoration struct {
	id       uint64
	fn       func(Client) Client
	priority int
}

// Chain 是装饰器链:provider Install 底座,policy Decorate 叠加,consumer Get。
type Chain struct {
	mu    sync.Mutex
	base  Client
	items []decoration
	seq   uint64
}

// Install 安装底座实现;重复安装报错。
func (c *Chain) Install(base Client) (kernel.Dispose, error) {
	if base == nil {
		return nil, Invalid("net: 底座实现不能为 nil")
	}
	c.mu.Lock()
	if c.base != nil {
		c.mu.Unlock()
		return nil, seamerr.New(seamerr.ErrInvalid, "net", "net 底座已安装", false)
	}
	c.base = base
	c.mu.Unlock()
	return func(context.Context) error {
		c.mu.Lock()
		c.base = nil
		c.items = nil
		c.mu.Unlock()
		return nil
	}, nil
}

// Decorate 叠加装饰器;priority 越大越靠外。
func (c *Chain) Decorate(fn func(Client) Client, priority int) (kernel.Dispose, error) {
	if fn == nil {
		return nil, Invalid("net: 装饰器不能为 nil")
	}
	c.mu.Lock()
	c.seq++
	d := decoration{id: c.seq, fn: fn, priority: priority}
	c.items = append(c.items, d)
	c.mu.Unlock()
	return func(context.Context) error {
		c.mu.Lock()
		kept := c.items[:0]
		for _, it := range c.items {
			if it.id != d.id {
				kept = append(kept, it)
			}
		}
		c.items = kept
		c.mu.Unlock()
		return nil
	}, nil
}

// Get 组装并返回最终 Client;没有底座时报未实现。
func (c *Chain) Get() (Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.base == nil {
		return nil, seamerr.New(seamerr.ErrUnimplemented, "net", "没有安装 net 实现", false)
	}
	items := make([]decoration, len(c.items))
	copy(items, c.items)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].priority < items[j].priority
	})
	out := c.base
	for _, it := range items {
		out = it.fn(out)
	}
	return out, nil
}

// Applied 返回当前装饰器优先级(外层到内层,诊断用)。
func (c *Chain) Applied() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]int, 0, len(c.items))
	for _, it := range c.items {
		out = append(out, it.priority)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// Invalid 构造参数错误。
func Invalid(msg string) error {
	return seamerr.New(seamerr.ErrInvalid, "net", msg, false)
}

// Denied 构造拒绝错误。
func Denied(reason string) error {
	return seamerr.New(seamerr.ErrDenied, "net", reason, false)
}

const pluginID = "seam-net"

// EventObserve 是 net 操作观察事件(观察不是门禁;payload 见 docs/seam-net.md)。
const EventObserve = "net/observe"

// Plugin 返回 seam/net 插件:只提供 net 服务(Chain)。
func Plugin() kernel.Plugin {
	return kernel.Func(pluginID, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		_, err := ctx.Provide("net", NewChain(), kernel.WithVersion(1), kernel.WithKind("object"))
		return nil, err
	}).WithProvide("net")
}

// NewChain 构造空链。
func NewChain() *Chain { return &Chain{} }
