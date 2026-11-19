package assemble

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adag/ada-go/v2/kernel"
	"github.com/adag/ada-go/v2/plugin"
)

// CodeUp 是装配执行层的错误码。
const CodeUp = "EASSEMBLEUP"

// readyTimeout 是单个插件从挂载到 active 的上限。
// 装配是启动路径,不能因为一个插件卡住就无限等待。
const readyTimeout = 10 * time.Second

// Lookup 按清单返回可挂载的 kernel 插件。
// go 插件从内存注册表取;ts 插件由 bridge 从 Artifact 构造(M3 后续)。
type Lookup func(m *plugin.Manifest) (kernel.Plugin, error)

// UpReport 是一次装配的结果。
type UpReport struct {
	Order  []string
	Fibers map[string]string // 插件 id → fiber id
}

// UpError 是装配失败的错误:带插件名、原因与回滚情况。
type UpError struct {
	Code         string
	Plugin       string
	Cause        error
	RolledBack   int
	RollbackErrs []error
}

func (e *UpError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: 插件 %s 装配失败: %v", e.Code, e.Plugin, e.Cause)
	if e.RolledBack > 0 {
		fmt.Fprintf(&b, "(已回滚 %d 个插件", e.RolledBack)
		if len(e.RollbackErrs) > 0 {
			fmt.Fprintf(&b, ",回滚错误: %v", errors.Join(e.RollbackErrs...))
		}
		b.WriteString(")")
	}
	return b.String()
}

func (e *UpError) Unwrap() error { return e.Cause }

// Up 按计划顺序挂载插件。
//
// 事务语义:任一插件挂载或就绪失败,已挂载的插件**按逆序卸载**,不留半装状态。
// 为什么逆序:与内核 effect 栈一致——后装的插件可能依赖先装的,先拆后面的。
func Up(app *kernel.App, manifests []*plugin.Manifest, plan *Report, lookup Lookup) (*UpReport, error) {
	if app == nil {
		return nil, &UpError{Code: CodeUp, Plugin: "-", Cause: errors.New("app 不能为 nil")}
	}
	if plan == nil || !plan.OK() {
		if plan == nil {
			return nil, &UpError{Code: CodeUp, Plugin: "-", Cause: errors.New("缺少装配计划")}
		}
		return nil, plan.Err()
	}
	byID := make(map[string]*plugin.Manifest, len(manifests))
	for _, m := range manifests {
		byID[m.ID] = m
	}

	report := &UpReport{Order: append([]string{}, plan.Order...), Fibers: map[string]string{}}
	mounted := make([]*kernel.Fiber, 0, len(plan.Order))

	rollback := func(id string, cause error) (*UpReport, error) {
		var rollbackErrs []error
		for i := len(mounted) - 1; i >= 0; i-- {
			if err := mounted[i].Dispose(context.Background()); err != nil {
				rollbackErrs = append(rollbackErrs, err)
			}
		}
		return report, &UpError{
			Code: CodeUp, Plugin: id, Cause: cause,
			RolledBack: len(mounted), RollbackErrs: rollbackErrs,
		}
	}

	for _, id := range plan.Order {
		m := byID[id]
		if m == nil {
			return rollback(id, fmt.Errorf("计划里的插件在清单集合中不存在"))
		}
		if lookup == nil {
			return rollback(id, fmt.Errorf("没有为插件类型 %q 提供 Lookup", m.Kind))
		}
		p, err := lookup(m)
		if err != nil {
			return rollback(id, fmt.Errorf("解析实现失败: %w", err))
		}
		fiber, err := app.Plugin(p, kernel.NewConfig(m.Config))
		if err != nil {
			return rollback(id, fmt.Errorf("挂载失败: %w", err))
		}
		mounted = append(mounted, fiber)

		rctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
		readyErr := fiber.Ready(rctx)
		cancel()
		if readyErr != nil {
			return rollback(id, fmt.Errorf("未就绪(状态 %s): %w", fiber.State(), readyErr))
		}
		report.Fibers[id] = fiber.ID()
	}
	return report, nil
}
