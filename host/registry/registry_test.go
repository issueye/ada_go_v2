package registry_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
