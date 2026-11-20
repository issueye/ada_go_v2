package netguard_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adag/ada-go/v2/platform/caps"
	"github.com/adag/ada-go/v2/platform/kernel"
	netguard "github.com/adag/ada-go/v2/plugins/policy/net-guard"
	capsstatic "github.com/adag/ada-go/v2/plugins/provider/caps-static"
	nethttp "github.com/adag/ada-go/v2/plugins/provider/net-http"
	seamapproval "github.com/adag/ada-go/v2/plugins/seam/approval"
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

type redirectClient struct {
	target string
	calls  int
	urls   []string
}

func (f *redirectClient) Do(_ context.Context, req seamnet.Request) (seamnet.Response, error) {
	f.calls++
	f.urls = append(f.urls, req.URL)
	if f.calls == 1 {
		return seamnet.Response{Status: 302, Headers: map[string]string{"Location": f.target}}, nil
	}
	return seamnet.Response{Status: 200, Body: []byte("ok"), URL: req.URL}, nil
}

// 10. 重定向到私网必须被逐跳拒绝,且不能发出第二次请求。
func TestRedirectPrivateDenied(t *testing.T) {
	f := &redirectClient{target: "http://10.0.0.1/secret"}
	b := brokerWith(t, map[string][]string{"p1": {"net.fetch.example.com"}})
	g := netguard.NewWithResolver(f, b, false, func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	})
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: "http://example.com/x"}); err == nil {
		t.Fatalf("重定向到私网应拒绝")
	}
	if f.calls != 1 {
		t.Fatalf("私网重定向不应继续请求,实际 %d 次", f.calls)
	}
}

// 11. 重定向到未授权公网 host 也必须拒绝。
func TestRedirectUnauthorizedDenied(t *testing.T) {
	f := &redirectClient{target: "http://other.example/x"}
	b := brokerWith(t, map[string][]string{"p1": {"net.fetch.example.com"}})
	g := netguard.New(f, b, true)
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: "http://example.com/x"}); err == nil {
		t.Fatalf("重定向到未授权 host 应拒绝")
	}
	if f.calls != 1 {
		t.Fatalf("未授权重定向不应继续请求,实际 %d 次", f.calls)
	}
}

// 12. 两跳都有授权时允许跟随,最终返回 200。
func TestRedirectAllowed(t *testing.T) {
	f := &redirectClient{target: "http://other.example/x"}
	b := brokerWith(t, map[string][]string{"p1": {
		"net.fetch.example.com", "net.fetch.other.example",
	}})
	g := netguard.New(f, b, true)
	resp, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: "http://example.com/x"})
	if err != nil {
		t.Fatalf("已授权重定向应放行: %v", err)
	}
	if resp.Status != 200 || f.calls != 2 {
		t.Fatalf("跟随结果不正确: status=%d calls=%d", resp.Status, f.calls)
	}
}

type fakeClient struct{ called bool }

func (f *fakeClient) Do(context.Context, seamnet.Request) (seamnet.Response, error) {
	f.called = true
	return seamnet.Response{Status: 200, Body: []byte("ok")}, nil
}

// 5. 域名解析到私网 → 拒绝。
func TestResolvedPrivateDenied(t *testing.T) {
	fake := &fakeClient{}
	b := brokerWith(t, map[string][]string{"p1": {"net.fetch.evil.example"}})
	g := netguard.NewWithResolver(fake, b, false, func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.1")}, nil
	})
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: "http://evil.example/x"}); err == nil {
		t.Fatalf("解析到私网应拒绝")
	}
	if fake.called {
		t.Fatalf("拒绝时不应调用底层")
	}
}

// 6. 域名解析到公网 → 放行。
func TestResolvedPublicAllowed(t *testing.T) {
	fake := &fakeClient{}
	b := brokerWith(t, map[string][]string{"p1": {"net.fetch.example.com"}})
	g := netguard.NewWithResolver(fake, b, false, func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	})
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: "http://example.com/x"}); err != nil {
		t.Fatalf("公网域名应放行: %v", err)
	}
	if !fake.called {
		t.Fatalf("放行时应调用底层")
	}
}

// 7. 解析失败 → 拒绝(fail-closed)。
func TestResolverErrorDenied(t *testing.T) {
	b := brokerWith(t, map[string][]string{"p1": {"net.fetch"}})
	g := netguard.NewWithResolver(&fakeClient{}, b, false, func(context.Context, string) ([]net.IP, error) {
		return nil, context.DeadlineExceeded
	})
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: "http://flaky.example/x"}); err == nil {
		t.Fatalf("解析失败应拒绝")
	}
}

type staticApprover struct {
	name  string
	allow bool
}

func (a *staticApprover) Name() string { return a.name }
func (a *staticApprover) Approve(context.Context, seamapproval.Request) (seamapproval.Decision, error) {
	return seamapproval.Decision{Allow: a.allow, Approver: a.name}, nil
}

func approvalRegistry(t *testing.T, allow bool) *seamapproval.Registry {
	t.Helper()
	reg := seamapproval.NewRegistry()
	if _, err := reg.Register(&staticApprover{name: "test", allow: allow}); err != nil {
		t.Fatalf("注册审批人失败: %v", err)
	}
	return reg
}

// 8. 未授权 host + escalate:批准后放行。
func TestEscalationAllow(t *testing.T) {
	fake := &fakeClient{}
	b := brokerWith(t, map[string][]string{})
	g := netguard.NewWithEscalation(fake, b, true, nil, approvalRegistry(t, true), true)
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), seamnet.Request{URL: "http://example.com/x"}); err != nil {
		t.Fatalf("批准后应放行: %v", err)
	}
	if !fake.called {
		t.Fatalf("放行时应调用底层")
	}
}

func TestEscalationTargetRedacted(t *testing.T) {
	reg := approvalRegistry(t, true)
	var got string
	reg.SetNotifier(func(_ context.Context, req seamapproval.Request) { got = req.Target })
	g := netguard.NewWithEscalation(&fakeClient{}, brokerWith(t, nil), true, nil, reg, true)
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"),
		seamnet.Request{URL: "http://example.com/x?token=SECRET"}); err != nil {
		t.Fatalf("批准后应放行: %v", err)
	}
	if strings.Contains(got, "SECRET") || !strings.Contains(got, "REDACTED") {
		t.Fatalf("审批 target 未脱敏: %q", got)
	}
}

// 9. 拒绝审批 / 未开启 escalate → 拒绝。
func TestEscalationDeny(t *testing.T) {
	req := seamnet.Request{URL: "http://example.com/x"}
	g := netguard.NewWithEscalation(&fakeClient{}, brokerWith(t, nil), true, nil, approvalRegistry(t, false), true)
	if _, err := g.Do(kernel.WithPrincipal(context.Background(), "p1"), req); err == nil {
		t.Fatalf("审批拒绝应拒绝")
	}
	g2 := netguard.NewWithEscalation(&fakeClient{}, brokerWith(t, nil), true, nil, approvalRegistry(t, true), false)
	if _, err := g2.Do(kernel.WithPrincipal(context.Background(), "p1"), req); err == nil {
		t.Fatalf("未开启 escalate 应拒绝")
	}
}
