// Package netguard 是 net 的授权策略:principal + net.fetch.<host> + 私网段拒绝。
//
// 门禁(装饰器),不是广播。priority=200:在其它 net 装饰器之外。
package netguard

import (
	"context"
	"net"
	"net/url"
	"strings"

	"github.com/adag/ada-go/v2/platform/caps"
	"github.com/adag/ada-go/v2/platform/kernel"
	seamapproval "github.com/adag/ada-go/v2/plugins/seam/approval"
	seamnet "github.com/adag/ada-go/v2/plugins/seam/net"
)

// PolicyName 是插件 id。
const PolicyName = "net-guard"

// Priority 是装饰器优先级。
const Priority = 200

// Resolver 解析域名到 IP(测试可注入);返回错误即视为解析失败。
type Resolver func(ctx context.Context, host string) ([]net.IP, error)

// defaultResolver 用标准解析器。
func defaultResolver(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// New 构造授权装饰器(使用标准解析器)。
func New(inner seamnet.Client, broker *caps.Broker, allowPrivate bool) seamnet.Client {
	return NewWithResolver(inner, broker, allowPrivate, defaultResolver)
}

// NewWithResolver 构造授权装饰器,并注入解析器。
func NewWithResolver(inner seamnet.Client, broker *caps.Broker, allowPrivate bool, resolver Resolver) seamnet.Client {
	if resolver == nil {
		resolver = defaultResolver
	}
	return &guard{inner: inner, broker: broker, allowPrivate: allowPrivate, resolver: resolver}
}

// NewWithEscalation 在授权失败时可走 approval 升级(获批一次后放行本次请求)。
func NewWithEscalation(inner seamnet.Client, broker *caps.Broker, allowPrivate bool, resolver Resolver, reg *seamapproval.Registry, escalate bool) seamnet.Client {
	g := NewWithResolver(inner, broker, allowPrivate, resolver).(*guard)
	g.approval = reg
	g.escalate = escalate
	return g
}

type guard struct {
	inner        seamnet.Client
	broker       *caps.Broker
	allowPrivate bool
	resolver     Resolver
	approval     *seamapproval.Registry
	escalate     bool
}

func (g *guard) Do(ctx context.Context, req seamnet.Request) (seamnet.Response, error) {
	u, err := url.Parse(req.URL)
	if err != nil || u.Hostname() == "" {
		return seamnet.Response{}, seamnet.Invalid("URL 非法: " + req.URL)
	}
	host := strings.ToLower(u.Hostname())
	if !g.allowPrivate {
		if isPrivateHost(host) {
			return seamnet.Response{}, seamnet.Denied("禁止访问私有/本地地址: " + host)
		}
		// 域名可能解析到私网:解析后逐个检查;解析失败按拒绝处理(fail-closed)。
		if net.ParseIP(host) == nil {
			ips, err := g.resolver(ctx, host)
			if err != nil {
				return seamnet.Response{}, seamnet.Denied("域名解析失败: " + host)
			}
			for _, ip := range ips {
				if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
					return seamnet.Response{}, seamnet.Denied("域名解析到私有/本地地址: " + host)
				}
			}
		}
	}
	id, ok := kernel.Principal(ctx)
	if !ok {
		return seamnet.Response{}, seamnet.Denied("缺少调用者身份:请使用 ctx.Context()")
	}
	if err := g.broker.Authorize(ctx, id, "net.fetch."+host, req.URL); err != nil {
		if !g.escalate || g.approval == nil {
			return seamnet.Response{}, seamnet.Denied(err.Error())
		}
		dec, aerr := seamapproval.Ask(ctx, g.approval, "", seamapproval.Request{
			ID: "net:" + req.Method + ":" + host, Source: "net", Op: req.Method,
			Target: redactTarget(req.URL), Reason: "未授权的 net.fetch host: " + host,
		})
		if aerr != nil || !dec.Allow {
			return seamnet.Response{}, seamnet.Denied("审批未通过: " + host)
		}
	}
	return g.inner.Do(ctx, req)
}

// isPrivateHost 判断 host 是否是本地/私有/链路本地地址。
func isPrivateHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// redactTarget 去掉审批目标里的 userinfo 与 query,避免 token 进入审批提示/审计。
func redactTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	u.Fragment = ""
	if u.RawQuery != "" {
		u.RawQuery = "REDACTED"
	}
	return u.String()
}

type config struct {
	Grants       map[string][]string `json:"grants,omitempty"`
	AllowPrivate bool                `json:"allowPrivate,omitempty"`
	Escalate     bool                `json:"escalate,omitempty"`
}

// Plugin 返回 net-guard 插件:inject net(可选 caps 服务),注册授权装饰器。
func Plugin() kernel.Plugin {
	return kernel.Func(PolicyName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamnet.Chain](ctx, "net")
		if err != nil {
			return nil, err
		}
		var conf config
		if err := cfg.Decode(&conf); err != nil {
			return nil, kernel.Fail(kernel.CodeInvalid, "net-guard: 解析配置失败: %v", err)
		}
		broker := caps.NewBroker(nil)
		fromService := false
		if v, ok := ctx.TryService("caps"); ok {
			if b, ok := v.(*caps.Broker); ok {
				broker = b
				fromService = true
			}
		}
		if !fromService {
			for plugin, list := range conf.Grants {
				if err := broker.Grant(plugin, list...); err != nil {
					return nil, kernel.Fail(kernel.CodeInvalid, "net-guard: 授予 %q 失败: %v", plugin, err)
				}
			}
		}
		var approvalReg *seamapproval.Registry
		if v, ok := ctx.TryService("approval"); ok {
			approvalReg, _ = v.(*seamapproval.Registry)
		}
		return chain.Decorate(func(inner seamnet.Client) seamnet.Client {
			return NewWithEscalation(inner, broker, conf.AllowPrivate, nil, approvalReg, conf.Escalate)
		}, Priority)
	}).WithOptional("caps", "net")
}
