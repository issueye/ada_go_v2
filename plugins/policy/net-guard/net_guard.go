// Package netguard 是 net 的授权策略:principal + net.fetch.<host> + 私网段拒绝。
//
// 门禁(装饰器),不是广播。priority=200:在其它 net 装饰器之外。
package netguard

import (
	"context"
	"net"
	"net/url"
	"strings"

	"github.com/adag/ada-go/v2/caps"
	"github.com/adag/ada-go/v2/kernel"
	seamnet "github.com/adag/ada-go/v2/plugins/seam/net"
)

// PolicyName 是插件 id。
const PolicyName = "net-guard"

// Priority 是装饰器优先级。
const Priority = 200

// New 构造授权装饰器(测试可直接使用)。
func New(inner seamnet.Client, broker *caps.Broker, allowPrivate bool) seamnet.Client {
	return &guard{inner: inner, broker: broker, allowPrivate: allowPrivate}
}

type guard struct {
	inner        seamnet.Client
	broker       *caps.Broker
	allowPrivate bool
}

func (g *guard) Do(ctx context.Context, req seamnet.Request) (seamnet.Response, error) {
	u, err := url.Parse(req.URL)
	if err != nil || u.Hostname() == "" {
		return seamnet.Response{}, seamnet.Invalid("URL 非法: " + req.URL)
	}
	host := strings.ToLower(u.Hostname())
	if !g.allowPrivate && isPrivateHost(host) {
		return seamnet.Response{}, seamnet.Denied("禁止访问私有/本地地址: " + host)
	}
	id, ok := kernel.Principal(ctx)
	if !ok {
		return seamnet.Response{}, seamnet.Denied("缺少调用者身份:请使用 ctx.Context()")
	}
	if err := g.broker.Authorize(ctx, id, "net.fetch."+host, req.URL); err != nil {
		return seamnet.Response{}, seamnet.Denied(err.Error())
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

type config struct {
	Grants       map[string][]string `json:"grants,omitempty"`
	AllowPrivate bool                `json:"allowPrivate,omitempty"`
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
		return chain.Decorate(func(inner seamnet.Client) seamnet.Client {
			return New(inner, broker, conf.AllowPrivate)
		}, Priority)
	}).WithOptional("caps", "net")
}
