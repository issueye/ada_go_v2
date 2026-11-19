// Package approval 是 `seam/approval` 的契约层:升级请求、审批决定与审批人注册表。
//
// 契约来源:docs/seam-api.md §5(审批) 与 plugin-kernel-design §12.2(门禁)。
// 默认必须 fail-closed:没有审批人 / 审批人报错 / 超时都应拒绝。
package approval

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/adag/ada-go/v2/kernel"
	"github.com/adag/ada-go/v2/plugins/seam/seamerr"
)

// DefaultTimeout 是单次审批的默认等待时间。
const DefaultTimeout = 30 * time.Second

// Request 是一次升级审批请求。
type Request struct {
	ID      string
	Source  string // "fs" | "shell" | "policy"
	Op      string
	Target  string
	Reason  string
	Details map[string]any
	Timeout time.Duration // 0 = DefaultTimeout
}

// Decision 是审批结果。
type Decision struct {
	Allow    bool
	Reason   string
	Approver string
}

// Approver 是审批人(UI 弹窗、策略、或默认拒绝)。
type Approver interface {
	Name() string
	Approve(ctx context.Context, req Request) (Decision, error)
}

// Registry 是 seam 插件提供的服务值:审批人注册于此。
//
// 选择规则固定:默认取名字排序后的第一个审批人;没有审批人时 Get 返回错误,
// 调用方必须据此拒绝(fail-closed)。
type Registry struct {
	mu        sync.RWMutex
	approvers map[string]Approver
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{approvers: make(map[string]Approver)}
}

// Register 注册审批人;dispose 时注销。
func (r *Registry) Register(impl Approver) (kernel.Dispose, error) {
	if impl == nil || impl.Name() == "" {
		return nil, seamerr.New(seamerr.ErrInvalid, "approval", "审批人不能为空", false)
	}
	name := impl.Name()
	r.mu.Lock()
	if _, exists := r.approvers[name]; exists {
		r.mu.Unlock()
		return nil, seamerr.New(seamerr.ErrInvalid, name, "同名审批人已注册", false)
	}
	r.approvers[name] = impl
	r.mu.Unlock()
	return func(context.Context) error {
		r.mu.Lock()
		if cur, ok := r.approvers[name]; ok && cur == impl {
			delete(r.approvers, name)
		}
		r.mu.Unlock()
		return nil
	}, nil
}

// Names 返回已注册审批人(排序)。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.approvers))
	for name := range r.approvers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Get 按名取审批人;name 为空时取默认(名字排序第一个)。
func (r *Registry) Get(name string) (Approver, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name == "" {
		names := make([]string, 0, len(r.approvers))
		for n := range r.approvers {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return nil, seamerr.Unimplemented("approval", "没有配置审批人")
		}
		name = names[0]
	}
	impl, ok := r.approvers[name]
	if !ok {
		return nil, seamerr.New(seamerr.ErrUnimplemented, name, "审批人未注册", false)
	}
	return impl, nil
}

const pluginID = "seam-approval"

// Plugin 返回 seam/approval 插件:只提供 approval 服务(Registry)。
func Plugin() kernel.Plugin {
	return kernel.Func(pluginID, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		_, err := ctx.Provide("approval", NewRegistry(), kernel.WithVersion(1), kernel.WithKind("object"))
		return nil, err
	}).WithProvide("approval")
}

// Describe 便于日志。
func Describe(req Request) string {
	target := req.Target
	if target == "" {
		target = req.Op
	}
	return fmt.Sprintf("%s:%s(%s)", req.Source, target, req.Reason)
}
