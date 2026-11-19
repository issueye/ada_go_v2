package registry_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adag/ada-go/v2/host/registry"
)

func setup(t *testing.T) (http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "repo.json"), []byte(`{"entries":[]}`), 0o644); err != nil {
		t.Fatalf("写索引失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "repo.sig.json"), []byte(`{"keyId":"k1"}`), 0o644); err != nil {
		t.Fatalf("写签名失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "p.zip"), []byte("zipdata"), 0o644); err != nil {
		t.Fatalf("写 artifact 失败: %v", err)
	}
	h, err := registry.NewHandler(registry.Options{Root: root})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
		t.Fatal(err)
	}
	return h, root
}

// 1. 索引/签名/artifact 可读取。
func TestServe(t *testing.T) {
	h, _ := setup(t)
	for _, tc := range []struct{ path, want string }{
		{"/repo.json", `{"entries":[]}`},
		{"/repo.sig.json", `{"keyId":"k1"}`},
		{"/artifacts/p.zip", "zipdata"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != tc.want {
			t.Fatalf("%s 返回不正确: %d %q", tc.path, rec.Code, rec.Body.String())
		}
	}
}

// 2. Token 认证。
func TestAuth(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "repo.json"), []byte(`{"entries":[]}`), 0o644); err != nil {
		t.Fatalf("写索引失败: %v", err)
	}
	h, err := registry.NewHandler(registry.Options{Root: root, Token: "secret"})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	noAuth := httptest.NewRequest(http.MethodGet, "/repo.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, noAuth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺 token 应 401,实际 %d", rec.Code)
	}
	withAuth := httptest.NewRequest(http.MethodGet, "/repo.json", nil)
	withAuth.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, withAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确 token 应 200,实际 %d", rec.Code)
	}
}

// 3. Serve:监听 :0 + 请求 + ctx 取消优雅关闭。
func TestServeGraceful(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "repo.json"), []byte(`{"entries":[]}`), 0o644); err != nil {
		t.Fatalf("写索引失败: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- registry.Serve(ctx, registry.Options{Root: root}, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/repo.json")
	if err != nil {
		cancel()
		t.Fatalf("请求失败: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("状态码不正确: %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("优雅关闭应返回 nil: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Serve 未在超时内退出")
	}
}
