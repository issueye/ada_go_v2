package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/v2/host/server"
	"github.com/adag/ada-go/v2/install"
)

func newHandler(t *testing.T, root string) http.Handler {
	t.Helper()
	h, err := server.NewHandler(server.Options{Root: root})
	if err != nil {
		t.Fatalf("构造 handler 失败: %v", err)
	}
	return h
}

func get(t *testing.T, h http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}

// 1. plan:合法目录返回顺序。
func TestPlan(t *testing.T) {
	h := newHandler(t, "../../examples")
	rec := get(t, h, "/api/plan?path=plugins")
	if rec.Code != http.StatusOK {
		t.Fatalf("plan 应 200,实际 %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK    bool     `json:"ok"`
		Order []string `json:"order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if !payload.OK || len(payload.Order) != 3 {
		t.Fatalf("plan 内容不正确: %+v", payload)
	}
}

// 2. caps:静态授权检查。
func TestCaps(t *testing.T) {
	h := newHandler(t, "../../examples")
	rec := get(t, h, "/api/caps?path=plugins")
	if rec.Code != http.StatusOK {
		t.Fatalf("caps 应 200,实际 %d", rec.Code)
	}
	var payload struct {
		OK      bool `json:"ok"`
		Plugins []struct {
			ID string `json:"id"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if !payload.OK || len(payload.Plugins) != 3 {
		t.Fatalf("caps 内容不正确: %+v", payload)
	}
}

// 3. 路径逃逸 → 400。
func TestEscape(t *testing.T) {
	h := newHandler(t, "../../examples")
	for _, path := range []string{"..", "../..", "/etc"} {
		rec := get(t, h, "/api/plan?path="+path)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("path=%q 应 400,实际 %d", path, rec.Code)
		}
	}
}

// 4. 非 GET → 405。
func TestMethod(t *testing.T) {
	h := newHandler(t, "../../examples")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plan?path=plugins", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应 405,实际 %d", rec.Code)
	}
}

// 5. list:读取安装索引。
func TestList(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "plugin.json"),
		[]byte(`{"id":"srv-demo","version":"0.1.0","kind":"go"}`), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	if _, err := install.Install(src, filepath.Join(root, "dest"), install.Options{}); err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	h := newHandler(t, root)
	rec := get(t, h, "/api/list?dest=dest")
	if rec.Code != http.StatusOK {
		t.Fatalf("list 应 200,实际 %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "srv-demo") {
		t.Fatalf("list 输出缺少条目: %s", rec.Body.String())
	}
}

// 6. 根路由列出可用接口。
func TestIndex(t *testing.T) {
	h := newHandler(t, "../../examples")
	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("index 应 200,实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/api/plan") {
		t.Fatalf("index 输出不正确: %s", rec.Body.String())
	}
}
