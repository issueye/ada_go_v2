// Package nethttp 是 `provider/net-http`:基于 net/http 的抓取实现。
//
// 只做「一次抓取」:没有重试、审批、host 允许表(这些属于 policy 装饰器)。
package nethttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/adag/ada-go/v2/platform/kernel"
	seamnet "github.com/adag/ada-go/v2/plugins/seam/net"
)

// ProviderName 是插件 id。
const ProviderName = "net-http"

// DefaultMaxBytes 是响应体上限(8 MiB)。
const DefaultMaxBytes int64 = 8 << 20

// DefaultTimeout 是单次抓取默认超时。
const DefaultTimeout = 30 * time.Second

type config struct {
	TimeoutMs int   `json:"timeoutMs,omitempty"`
	MaxBytes  int64 `json:"maxBytes,omitempty"`
}

type client struct {
	http     *http.Client
	timeout  time.Duration
	maxBytes int64
}

// New 构造抓取实现(测试可直接使用)。
func New(timeout time.Duration, maxBytes int64) seamnet.Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &client{
		timeout: timeout, maxBytes: maxBytes,
		http: &http.Client{
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
}

func (c *client) Do(ctx context.Context, req seamnet.Request) (seamnet.Response, error) {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		return seamnet.Response{}, seamnet.Invalid("URL 非法: " + err.Error())
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return seamnet.Response{}, seamnet.Invalid("只支持 http/https: " + u.Scheme)
	}

	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var body io.Reader
	if len(req.Body) > 0 {
		body = strings.NewReader(string(req.Body))
	}
	hreq, err := http.NewRequestWithContext(actx, method, u.String(), body)
	if err != nil {
		return seamnet.Response{}, seamnet.Invalid("构造请求失败: " + err.Error())
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}

	resp, err := c.http.Do(hreq)
	if err != nil {
		return seamnet.Response{}, fmt.Errorf("net-http: 抓取失败: %w", err)
	}
	defer resp.Body.Close()

	limit := c.maxBytes
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return seamnet.Response{}, fmt.Errorf("net-http: 读取响应失败: %w", err)
	}
	truncated := false
	if int64(len(data)) > limit {
		data = data[:limit]
		truncated = true
	}

	headers := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		headers[k] = strings.Join(vs, ", ")
	}
	urlStr := ""
	if resp.Request != nil && resp.Request.URL != nil {
		urlStr = resp.Request.URL.String()
	}
	return seamnet.Response{
		Status: resp.StatusCode, Headers: headers, Body: data,
		URL: urlStr, Truncated: truncated,
	}, nil
}

// Plugin 返回 net-http 插件:inject net seam,Install 底座实现。
func Plugin() kernel.Plugin {
	return kernel.Func(ProviderName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamnet.Chain](ctx, "net")
		if err != nil {
			return nil, err
		}
		var conf config
		if err := cfg.Decode(&conf); err != nil {
			return nil, kernel.Fail(kernel.CodeInvalid, "net-http: 解析配置失败: %v", err)
		}
		timeout := time.Duration(conf.TimeoutMs) * time.Millisecond
		if conf.TimeoutMs <= 0 {
			timeout = DefaultTimeout
		}
		return chain.Install(New(timeout, conf.MaxBytes))
	}).WithInject("net")
}
