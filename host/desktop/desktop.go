// Package desktop 是 host/desktop 适配器的映射骨架:把内核快照映射成宿主视图。
//
// 它不引入任何窗口/UI 依赖,只定义 Renderer 接口;真正的 mygo 窗口由宿主实现。
package desktop

import (
	"errors"

	"github.com/adag/ada-go/v2/kernel"
)

// Renderer 由桌面宿主实现:把 View 画到窗口。
type Renderer interface {
	Render(View) error
}

// FiberRow 是视图里的一个 fiber。
type FiberRow struct {
	ID     string
	Plugin string
	State  string
	Parent string
}

// ServiceRow 是视图里的一个服务。
type ServiceRow struct {
	Name       string
	Provider   string
	Version    int
	Dependents []string
}

// EffectRow 是一条副作用注册。
type EffectRow struct {
	Plugin string
	Label  string
}

// View 是渲染所需的只读视图模型。
type View struct {
	Fibers   []FiberRow
	Services []ServiceRow
	Effects  []EffectRow
	Events   []string
}

// Host 持有内核 App 与渲染器。
type Host struct {
	app      *kernel.App
	renderer Renderer
}

// New 构造桌面宿主映射;app 与 renderer 都不能为 nil。
func New(app *kernel.App, renderer Renderer) (*Host, error) {
	if app == nil {
		return nil, errors.New("desktop: app 不能为 nil")
	}
	if renderer == nil {
		return nil, errors.New("desktop: renderer 不能为 nil")
	}
	return &Host{app: app, renderer: renderer}, nil
}

// Refresh 把 app.Inspect() 映射成 View 并交给渲染器;返回映射出的视图。
func (h *Host) Refresh() (*View, error) {
	snap := h.app.Inspect()
	view := &View{Events: snap.Events}
	for _, f := range snap.Fibers {
		view.Fibers = append(view.Fibers, FiberRow{ID: f.ID, Plugin: f.Plugin, State: f.State, Parent: f.Parent})
	}
	for _, s := range snap.Services {
		view.Services = append(view.Services, ServiceRow{
			Name: s.Name, Provider: s.Provider, Version: s.Version, Dependents: s.Dependents,
		})
	}
	for _, e := range snap.Effects {
		view.Effects = append(view.Effects, EffectRow{Plugin: e.Plugin, Label: e.Label})
	}
	if err := h.renderer.Render(*view); err != nil {
		return view, err
	}
	return view, nil
}
