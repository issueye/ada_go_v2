package builtins_test

import (
	"testing"

	"github.com/adag/ada-go/v2/host/builtins"
	"github.com/adag/ada-go/v2/plugin"
)

// 内置 Go 插件注册表包含安全栈关键 id。
func TestBuiltinGoPlugins(t *testing.T) {
	got := builtins.Plugins()
	for _, id := range []string{"seam-fs", "fs-local", "caps-static", "caps-gate", "shell-local", "seam-approval"} {
		if _, ok := got[id]; !ok {
			t.Fatalf("内置注册表缺少 %q", id)
		}
	}
}

// applyAutoGrants 把清单 caps 灌进提供 caps 服务的插件配置。
func TestApplyAutoGrants(t *testing.T) {
	capsPlugin := &plugin.Manifest{ID: "caps-static", Kind: "go", Provide: []string{"caps"}}
	consumer := &plugin.Manifest{ID: "consumer", Kind: "go", Trust: "third-party", Caps: []string{"fs.read"}}
	manifests := []*plugin.Manifest{capsPlugin, consumer}
	if err := builtins.ApplyAutoGrants(manifests); err != nil {
		t.Fatalf("灌入失败: %v", err)
	}
	grants, ok := capsPlugin.Config["grants"].(map[string][]string)
	if !ok {
		t.Fatalf("caps-static 配置缺少 grants: %+v", capsPlugin.Config)
	}
	if len(grants["consumer"]) != 1 || grants["consumer"][0] != "fs.read" {
		t.Fatalf("授权表不正确: %+v", grants)
	}

	// 既有 config grants 应保留(并集)。
	capsPlugin.Config = map[string]any{"grants": map[string][]string{"other": {"exec"}}}
	if err := builtins.ApplyAutoGrants(manifests); err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	merged := capsPlugin.Config["grants"].(map[string][]string)
	if len(merged["other"]) != 1 || len(merged["consumer"]) != 1 {
		t.Fatalf("并集不正确: %+v", merged)
	}
}

// 非法 caps 应使灌入失败(不静默)。
func TestApplyAutoGrantsInvalid(t *testing.T) {
	bad := &plugin.Manifest{ID: "bad", Kind: "go", Trust: "third-party", Caps: []string{"fs.write"}}
	if err := builtins.ApplyAutoGrants([]*plugin.Manifest{bad}); err == nil {
		t.Fatalf("trust 不足应报错")
	}
}
