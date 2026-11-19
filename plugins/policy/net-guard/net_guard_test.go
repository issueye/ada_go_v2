package netguard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adag/ada-go/v2/caps"
	"github.com/adag/ada-go/v2/kernel"
	netguard "github.com/adag/ada-go/v2/plugins/policy/net-guard"
	capsstatic "github.com/adag/ada-go/v2/plugins/provider/caps-static"
	nethttp "github.com/adag/ada-go/v2/plugins/provider/net-http"
	seamnet "github.com/adag/ada-go/v2/plugins/seam/net"
)

func brokerWith(t *testing.T, grants map[string][]string) *caps.Broker {
	t.Helper()
	b := caps.NewBroker(nil)
	for plugin, list := range grants {
		if err := b.Grant(plugin, list...); err != nil {
			t.Fatalf("授予失败: %v", err)
		}
	}
	return b
}

func server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 1. 授权具体 host 放行;通配 net.fetch 也可。
func TestAllowed(t *testing.T) {
	srv := server(t)
	inner := nethttp.New(0, 0)

	b := brokerWith(t, map[string][]string{"p1": {"net.fetch.127.0.0.1"}})
	g := netguard.New(inner, b, true)
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: srv.URL}); err != nil {
		t.Fatalf("host 授权应放行: %v", err)
	}

	b2 := brokerWith(t, map[string][]string{"p1": {"net.fetch"}})
	g2 := netguard.New(inner, b2, true)
	if _, err := g2.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: srv.URL}); err != nil {
		t.Fatalf("通配授权应放行: %v", err)
	}
}

// 2. 未授权 host / 无身份拒绝。
func TestDenied(t *testing.T) {
	srv := server(t)
	b := brokerWith(t, map[string][]string{"p1": {"net.fetch.example.com"}})
	g := netguard.New(nethttp.New(0, 0), b, true)
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: srv.URL}); err == nil {
		t.Fatalf("未授权 host 应拒绝")
	}
	if _, err := g.Do(context.Background(), seamnet.Request{URL: srv.URL}); err == nil {
		t.Fatalf("无身份应拒绝")
	}
}

// 3. 默认拒绝私有/本地地址。
func TestPrivateDenied(t *testing.T) {
	srv := server(t)
	b := brokerWith(t, map[string][]string{"p1": {"net.fetch.127.0.0.1"}})
	g := netguard.New(nethttp.New(0, 0), b, false)
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: srv.URL}); err == nil {
		t.Fatalf("默认应拒绝本地地址")
	}
}

// 4. 内核集成:优先使用注入的 caps 服务(caps-static)。
func TestServiceBroker(t *testing.T) {
	srv := server(t)
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	mount := func(name string, p kernel.Plugin, cfg map[string]any) {
		t.Helper()
		if _, err := app.Plugin(p, kernel.NewConfig(cfg)); err != nil {
			t.Fatalf("挂载 %s 失败: %v", name, err)
		}
	}
	mount("seam/net", seamnet.Plugin(), nil)
	mount("net-http", nethttp.Plugin(), nil)
	mount("caps-static", capsstatic.Plugin(), map[string]any{
		"grants": map[string][]string{"consumer": {"net.fetch.127.0.0.1"}},
	})
	mount("net-guard", netguard.Plugin(), map[string]any{"allowPrivate": true})

	var body string
	consumer := kernel.Func("consumer", func(ctx *kernel.Context, _ kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamnet.Chain](ctx, "net")
		if err != nil {
			return nil, err
		}
		c, err := chain.Get()
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(ctx.Context(), seamnet.Request{URL: srv.URL})
		if err != nil {
			return nil, err
		}
		body = string(resp.Body)
		return nil, nil
	}).WithInject("net")
	if _, err := app.Plugin(consumer, kernel.NewConfig(nil)); err != nil {
		t.Fatalf("消费者应通过 caps 服务授权: %v", err)
	}
	if body != "ok" {
		t.Fatalf("抓取内容不正确: %q", body)
	}
}
