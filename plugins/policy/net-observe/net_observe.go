// Package netobserve 是 net 的观察策略:每次抓取后广播 net/observe。
//
// 观察不是门禁:失败/被拒绝的调用也记录。payload 不含 body/headers(避免泄露)。
// priority=250(最外层),能看到 net-guard 的拒绝结果。
package netobserve

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/adag/ada-go/v2/platform/kernel"
	seamnet "github.com/adag/ada-go/v2/plugins/seam/net"
)

// PolicyName 是插件 id。
const PolicyName = "net-observe"

// Priority 高于 net-guard(200)。
const Priority = 250

// Record 是一次抓取的观察记录。
type Record struct {
	Method     string
	Host       string
	Status     int
	Bytes      int64
	DurationMs int64
	Plugin     string
	Err        string
}

// Emitter 接收记录;插件实现里转成 net/observe 事件。
type Emitter func(context.Context, Record)

// New 构造观察装饰器;skipHosts 命中时只透传。
func New(inner seamnet.Client, emit Emitter, skipHosts []string) seamnet.Client {
	skip := make(map[string]bool, len(skipHosts))
	for _, h := range skipHosts {
		skip[h] = true
	}
	return &observer{inner: inner, emit: emit, skip: skip}
}

type observer struct {
	inner seamnet.Client
	emit  Emitter
	skip  map[string]bool
}

func (o *observer) Do(ctx context.Context, req seamnet.Request) (seamnet.Response, error) {
	start := time.Now()
	resp, err := o.inner.Do(ctx, req)
	host := ""
	if u, perr := url.Parse(req.URL); perr == nil {
		host = u.Hostname()
	}
	if o.emit == nil || o.skip[host] {
		return resp, err
	}
	rec := Record{
		Method: req.Method, Host: host, Status: resp.Status,
		Bytes: int64(len(resp.Body)), DurationMs: time.Since(start).Milliseconds(),
	}
	if id, ok := kernel.Principal(ctx); ok {
		rec.Plugin = id
	}
	if err != nil {
		rec.Err = scrubError(err.Error(), req.URL)
	}
	o.emit(ctx, rec)
	return resp, err
}

// scrubError 从错误文本里删掉 URL query(可能含 token)与 userinfo。
func scrubError(msg, rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return msg
	}
	if u.RawQuery != "" {
		msg = strings.ReplaceAll(msg, u.RawQuery, "REDACTED")
	}
	if u.User != nil && u.User.String() != "" {
		msg = strings.ReplaceAll(msg, u.User.String()+"@", "")
	}
	// 整段 URL 统一替换为不含 query/fragment/user 的形式。
	if u.Scheme != "" && u.Host != "" {
		safe := u.Scheme + "://" + u.Host + u.Path
		msg = strings.ReplaceAll(msg, rawURL, safe)
	}
	return msg
}

type config struct {
	SkipHosts []string `json:"skipHosts,omitempty"`
}

// Plugin 返回 net-observe 插件:inject net seam,注册观察装饰器。
func Plugin() kernel.Plugin {
	return kernel.Func(PolicyName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamnet.Chain](ctx, "net")
		if err != nil {
			return nil, err
		}
		var conf config
		if err := cfg.Decode(&conf); err != nil {
			return nil, kernel.Fail(kernel.CodeInvalid, "net-observe: 解析配置失败: %v", err)
		}
		emit := func(_ context.Context, rec Record) {
			payload := map[string]any{
				"method": rec.Method, "host": rec.Host, "status": rec.Status,
				"bytes": rec.Bytes, "durationMs": rec.DurationMs, "plugin": rec.Plugin,
			}
			if rec.Err != "" {
				payload["error"] = rec.Err
			}
			ctx.Emit(seamnet.EventObserve, payload)
		}
		return chain.Decorate(func(inner seamnet.Client) seamnet.Client {
			return New(inner, emit, conf.SkipHosts)
		}, Priority)
	}).WithOptional("net")
}
