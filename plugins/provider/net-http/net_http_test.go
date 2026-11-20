package nethttp_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/issueye/ada_go_v2/platform/kernel"
	nethttp "github.com/issueye/ada_go_v2/plugins/provider/net-http"
	seamnet "github.com/issueye/ada_go_v2/plugins/seam/net"
)

// 1. 200 响应:状态/头/体。
func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	c := nethttp.New(5*time.Second, 1024)
	resp, err := c.Do(context.Background(), seamnet.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	if resp.Status != 200 || string(resp.Body) != "hello" || resp.Headers["X-Test"] != "yes" {
		t.Fatalf("响应不正确: %+v", resp)
	}
}

// 1.1 NoRedirect 让 provider 原样返回 3xx,由外层策略逐跳校验。
func TestNoRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("target"))
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	c := nethttp.New(5*time.Second, 1024)
	resp, err := c.Do(context.Background(), seamnet.Request{URL: srv.URL, NoRedirect: true})
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	if resp.Status != http.StatusFound {
		t.Fatalf("NoRedirect 应返回 302,实际 %d", resp.Status)
	}
	if got := resp.Headers["Location"]; got != target.URL {
		t.Fatalf("Location 不正确: %q", got)
	}

	// 不带标记时仍按 provider 默认策略跟随。
	resp, err = c.Do(context.Background(), seamnet.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("跟随失败: %v", err)
	}
	if resp.Status != http.StatusOK || string(resp.Body) != "target" {
		t.Fatalf("默认跟随结果不正确: %+v", resp)
	}
}

// 1.2 DialCheck 在拨号前逐 IP 校验;拒绝则不建立连接。
func TestDialCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := nethttp.New(5*time.Second, 1024)

	_, err := c.Do(context.Background(), seamnet.Request{
		URL: srv.URL,
		DialCheck: func(string, string, net.IP) error {
			return seamnet.Denied("测试拒绝")
		},
	})
	if err == nil {
		t.Fatalf("DialCheck 拒绝时不应建立连接")
	}

	resp, err := c.Do(context.Background(), seamnet.Request{
		URL: srv.URL,
		DialCheck: func(string, string, net.IP) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("DialCheck 放行时应成功: %v", err)
	}
	if resp.Status != http.StatusOK || string(resp.Body) != "ok" {
		t.Fatalf("响应不正确: %+v", resp)
	}
}

// 2. 非 2xx 原样返回(不自动报错)。
func TestNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer srv.Close()
	c := nethttp.New(5*time.Second, 1024)
	resp, err := c.Do(context.Background(), seamnet.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	if resp.Status != 404 {
		t.Fatalf("应返回 404,实际 %d", resp.Status)
	}
}

// 3. 超过上限截断并标记。
func TestTruncate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()
	c := nethttp.New(5*time.Second, 10)
	resp, err := c.Do(context.Background(), seamnet.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	if !resp.Truncated || len(resp.Body) != 10 {
		t.Fatalf("截断不正确: truncated=%v len=%d", resp.Truncated, len(resp.Body))
	}
}

// 4. 超时失败。
func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()
	c := nethttp.New(20*time.Millisecond, 1024)
	if _, err := c.Do(context.Background(), seamnet.Request{URL: srv.URL}); err == nil {
		t.Fatalf("超时应失败")
	}
}

// 5. 非 http/https 拒绝。
func TestBadScheme(t *testing.T) {
	c := nethttp.New(time.Second, 1024)
	if _, err := c.Do(context.Background(), seamnet.Request{URL: "ftp://example.com/x"}); err == nil {
		t.Fatalf("非法 scheme 应拒绝")
	}
}

// 6. 内核集成:seam/net + net-http + consumer。
func TestKernelIntegration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if _, err := app.Plugin(seamnet.Plugin(), kernel.NewConfig(nil)); err != nil {
		t.Fatalf("挂载 seam/net 失败: %v", err)
	}
	if _, err := app.Plugin(nethttp.Plugin(), kernel.NewConfig(nil)); err != nil {
		t.Fatalf("挂载 net-http 失败: %v", err)
	}
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
		t.Fatalf("挂载消费者失败: %v", err)
	}
	if body != "ok" {
		t.Fatalf("抓取内容不正确: %q", body)
	}
}
