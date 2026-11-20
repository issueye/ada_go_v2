package netobserve_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/issueye/ada_go_v2/platform/kernel"
	netobserve "github.com/issueye/ada_go_v2/plugins/policy/net-observe"
	nethttp "github.com/issueye/ada_go_v2/plugins/provider/net-http"
	seamnet "github.com/issueye/ada_go_v2/plugins/seam/net"
)

type fakeClient struct {
	resp seamnet.Response
	err  error
}

func (f *fakeClient) Do(context.Context, seamnet.Request) (seamnet.Response, error) {
	return f.resp, f.err
}

// 1. 成功记录 host/status/bytes/身份。
func TestRecordsSuccess(t *testing.T) {
	var rec netobserve.Record
	c := netobserve.New(&fakeClient{resp: seamnet.Response{Status: 200, Body: []byte("ok")}},
		func(_ context.Context, r netobserve.Record) { rec = r }, nil)
	ctx := kernel.WithPrincipal(context.Background(), "p1")
	if _, err := c.Do(ctx, seamnet.Request{URL: "http://example.com/x"}); err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	if rec.Host != "example.com" || rec.Status != 200 || rec.Bytes != 2 || rec.Plugin != "p1" {
		t.Fatalf("记录不正确: %+v", rec)
	}
}

// 2. 失败也记录 error。
func TestRecordsError(t *testing.T) {
	var rec netobserve.Record
	c := netobserve.New(&fakeClient{err: errors.New("boom")},
		func(_ context.Context, r netobserve.Record) { rec = r }, nil)
	if _, err := c.Do(context.Background(), seamnet.Request{URL: "http://example.com/x"}); err == nil {
		t.Fatalf("应返回错误")
	}
	if rec.Err != "boom" {
		t.Fatalf("失败记录不正确: %+v", rec)
	}
}

// 3. 内核集成:事件带调用者身份。
func TestKernelIntegration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
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
	mount("net-observe", netobserve.Plugin(), nil)
	var payload map[string]any
	mount("listener", kernel.Func("listener", func(ctx *kernel.Context, _ kernel.Config) (kernel.Dispose, error) {
		ctx.On(seamnet.EventObserve, func(_ context.Context, _ *kernel.Context, args []any) (any, error) {
			if len(args) > 0 {
				if m, ok := args[0].(map[string]any); ok {
					payload = m
				}
			}
			return nil, nil
		})
		return nil, nil
	}), nil)
	mount("consumer", kernel.Func("consumer", func(ctx *kernel.Context, _ kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamnet.Chain](ctx, "net")
		if err != nil {
			return nil, err
		}
		c, err := chain.Get()
		if err != nil {
			return nil, err
		}
		_, err = c.Do(ctx.Context(), seamnet.Request{URL: srv.URL})
		return nil, err
	}).WithInject("net"), nil)
	if payload == nil || payload["host"] != "127.0.0.1" || payload["plugin"] != "consumer" {
		t.Fatalf("事件 payload 不正确: %+v", payload)
	}
}

// 4. 错误文本里的 URL query 被脱敏。
func TestScrubsError(t *testing.T) {
	const raw = "http://example.com/x?token=secret"
	var rec netobserve.Record
	c := netobserve.New(&fakeClient{err: errors.New("请求失败: " + raw)},
		func(_ context.Context, r netobserve.Record) { rec = r }, nil)
	if _, err := c.Do(context.Background(), seamnet.Request{URL: raw}); err == nil {
		t.Fatalf("应返回错误")
	}
	if strings.Contains(rec.Err, "secret") {
		t.Fatalf("错误文本泄露 token: %s", rec.Err)
	}
	if !strings.Contains(rec.Err, "REDACTED") && !strings.Contains(rec.Err, "http://example.com/x") {
		t.Fatalf("脱敏结果不正确: %s", rec.Err)
	}
}
