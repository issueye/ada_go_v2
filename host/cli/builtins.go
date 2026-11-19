package cli

import (
	"github.com/adag/ada-go/v2/assemble"
	"github.com/adag/ada-go/v2/caps"
	"github.com/adag/ada-go/v2/kernel"
	"github.com/adag/ada-go/v2/plugin"
	capsgate "github.com/adag/ada-go/v2/plugins/policy/caps-gate"
	fsobserve "github.com/adag/ada-go/v2/plugins/policy/fs-observe"
	fsredact "github.com/adag/ada-go/v2/plugins/policy/fs-redact"
	fssandbox "github.com/adag/ada-go/v2/plugins/policy/fs-sandbox"
	shellguard "github.com/adag/ada-go/v2/plugins/policy/shell-guard"
	shellobserve "github.com/adag/ada-go/v2/plugins/policy/shell-observe"
	shellredact "github.com/adag/ada-go/v2/plugins/policy/shell-redact"
	approvaldeny "github.com/adag/ada-go/v2/plugins/provider/approval-deny"
	capsstatic "github.com/adag/ada-go/v2/plugins/provider/caps-static"
	fslocal "github.com/adag/ada-go/v2/plugins/provider/fs-local"
	sandboxnone "github.com/adag/ada-go/v2/plugins/provider/sandbox-none"
	memory "github.com/adag/ada-go/v2/plugins/provider/session-memory"
	shelllocal "github.com/adag/ada-go/v2/plugins/provider/shell-local"
	seamapproval "github.com/adag/ada-go/v2/plugins/seam/approval"
	seamfs "github.com/adag/ada-go/v2/plugins/seam/fs"
	seamsandbox "github.com/adag/ada-go/v2/plugins/seam/sandbox"
	seamsession "github.com/adag/ada-go/v2/plugins/seam/session"
	seamshell "github.com/adag/ada-go/v2/plugins/seam/shell"
)

// builtinGoPlugins 是编译进 CLI 的 Go 插件注册表。
//
// 这是「CLI 也能挂安全栈」的最小集合:seam 契约 + 本地实现 + 策略 + 授权。
// 第三方 Go 插件仍需宿主编译时注册;这里只放仓库内置的参考实现。
func builtinGoPlugins() map[string]func() kernel.Plugin {
	return map[string]func() kernel.Plugin{
		"seam-fs":        seamfs.Plugin,
		"fs-local":       fslocal.Plugin,
		"fs-sandbox":     fssandbox.Plugin,
		"seam-shell":     seamshell.Plugin,
		"shell-local":    shelllocal.Plugin,
		"shell-guard":    shellguard.Plugin,
		"seam-sandbox":   seamsandbox.Plugin,
		"sandbox-none":   sandboxnone.Plugin,
		"seam-approval":  seamapproval.Plugin,
		"approval-deny":  approvaldeny.Plugin,
		"seam-session":   seamsession.Plugin,
		"session-memory": memory.Plugin,
		"caps-static":    capsstatic.Plugin,
		"caps-gate":      capsgate.Plugin,
		"fs-redact":      fsredact.Plugin,
		"fs-observe":     fsobserve.Plugin,
		"shell-redact":   shellredact.Plugin,
		"shell-observe":  shellobserve.Plugin,
	}
}

// autoGrants 从清单 caps/trust 生成授权表(失败即返回错误)。
func autoGrants(manifests []*plugin.Manifest) (map[string][]string, error) {
	broker := caps.NewBroker(nil)
	if err := assemble.GrantManifests(broker, manifests); err != nil {
		return nil, err
	}
	return broker.Snapshot(), nil
}

// applyAutoGrants 把清单能力灌进提供 "caps" 服务的插件配置。
//
// 这样 caps-gate 会优先用这个注入的 Broker;prset 里手写的 grants 仍会保留(并集)。
func applyAutoGrants(manifests []*plugin.Manifest) error {
	grants, err := autoGrants(manifests)
	if err != nil {
		return err
	}
	for _, m := range manifests {
		provides := false
		for _, name := range m.Provide {
			if name == "caps" {
				provides = true
				break
			}
		}
		if !provides {
			continue
		}
		if m.Config == nil {
			m.Config = map[string]any{}
		}
		merged := map[string][]string{}
		if existing, ok := m.Config["grants"].(map[string][]string); ok {
			for k, v := range existing {
				merged[k] = append(merged[k], v...)
			}
		}
		for plugin, list := range grants {
			merged[plugin] = append(merged[plugin], list...)
		}
		m.Config["grants"] = merged
	}
	return nil
}
