// Package bridge 把编译产物接成 kernel 插件:M2 的第一段桥。
//
// 核心约束:JS 跑在自己的 goroutine 上,而内核的生命周期操作是**单写者**
// (M1 §1 的并发假设)。因此 JS 侧不能直接调用 Provide/Emit/Plugin——
// 那会同时触碰服务表、事件总线与 fiber 表。这里的做法是:
//
//	JS ctx.provide / ctx.emit ──暂存──▶ stager ──apply 返回后──▶ 内核协程提交
//
// 暂存是按调用顺序的确定性队列;提交发生在 apply 返回之后、仍在调用 Apply 的
// 那个内核协程上,所以不引入跨协程写。
package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/adag/ada-go/v2/platform/compile"
	"github.com/adag/ada-go/v2/platform/jsrt"
	"github.com/adag/ada-go/v2/platform/kernel"
	"github.com/adag/ada-go/v2/platform/kernel/schema"
)

// applyTimeout 是装配预算。装配只做编译 + 加载 + 一次 apply,
// 不应该长时间占住内核的控制协程。
const applyTimeout = 10 * time.Second

// Options 描述一个 JS 插件。
type Options struct {
	Name     string
	Artifact *compile.Artifact
	// Logf 接收宿主判定的日志(JS 侧的 ctx.log);nil 表示丢弃。
	Logf func(format string, args ...any)
}

type provideReq struct {
	name  string
	value *jsrt.Service
}

type emitReq struct {
	event   string
	payload any
}

type onReq struct {
	event  string
	invoke func(ctx context.Context, payload any) (any, error)
}

type effectReq struct {
	label   string
	dispose func(ctx context.Context) error
}

type wfReq struct {
	event  string
	invoke func(ctx context.Context, input any, next func(any) (any, error)) (any, error)
}

// stager 收集 JS 侧 ctx.provide / ctx.emit 的请求,等回到内核协程再提交。
//
// 为什么必须暂存而不是「加锁直接写」:加锁只能消除数据竞争,消除不了语义竞争——
// 内核的 recompute 依赖「单协程推进」,第二个协程插进来会造成半新半旧的依赖图。
type stager struct {
	mu         sync.Mutex
	closed     bool
	ons        []onReq
	waterfalls []wfReq
	effects    []effectReq
	provides   []provideReq
	emits      []emitReq
}

func (s *stager) on(event string, invoke func(ctx context.Context, payload any) (any, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("ctx.on 只能在 apply 期间调用")
	}
	s.ons = append(s.ons, onReq{event: event, invoke: invoke})
	return nil
}

func (s *stager) waterfall(event string, invoke func(ctx context.Context, input any, next func(any) (any, error)) (any, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("ctx.waterfall 只能在 apply 期间调用")
	}
	s.waterfalls = append(s.waterfalls, wfReq{event: event, invoke: invoke})
	return nil
}

func (s *stager) effect(label string, dispose func(ctx context.Context) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("ctx.effect 只能在 apply 期间调用")
	}
	s.effects = append(s.effects, effectReq{label: label, dispose: dispose})
	return nil
}

func (s *stager) provide(name string, value *jsrt.Service) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("ctx.provide 只能在 apply 期间调用")
	}
	s.provides = append(s.provides, provideReq{name: name, value: value})
	return nil
}

func (s *stager) emit(event string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("ctx.emit 只能在 apply 期间调用")
	}
	s.emits = append(s.emits, emitReq{event: event, payload: payload})
	return nil
}

// drain 关闭入口并取出暂存请求。之后 JS 再调用会被拒绝:
// 没有提交点的调用必须报错,而不是静默丢失。
func (s *stager) drain() ([]wfReq, []onReq, []effectReq, []provideReq, []emitReq) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.waterfalls, s.ons, s.effects, s.provides, s.emits
}

func (s *stager) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// pendingEffects 复制已登记的清理函数,用于装配失败时的兜底回收。
func (s *stager) pendingEffects() []effectReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]effectReq, len(s.effects))
	copy(out, s.effects)
	return out
}

// runEffects 按逆序执行 JS 登记的清理函数(与内核 effect 栈同语义:
// 后创建的资源可能依赖先创建的,必须先回收)。
func runEffects(plugin string, effects []effectReq, ctx context.Context) []error {
	var errs []error
	for i := len(effects) - 1; i >= 0; i-- {
		if err := effects[i].dispose(ctx); err != nil {
			errs = append(errs, fmt.Errorf("bridge: 插件 %s 的 effect %q 清理失败: %w", plugin, effects[i].label, err))
		}
	}
	return errs
}

// Plugin 是 kernel.Plugin 的 JS 实现。
type Plugin struct {
	opts Options

	mu   sync.Mutex
	logs []string
	rt   *jsrt.Runtime
	kctx *kernel.Context // 运行期 emit 用;卸载时清空
}

// New 构造 JS 插件。它不装配运行时——装配发生在 Apply,失败时内核会把 fiber 置为 failed。
func New(opts Options) (*Plugin, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("bridge: 插件名不能为空")
	}
	if opts.Artifact == nil {
		return nil, fmt.Errorf("bridge: Artifact 不能为 nil")
	}
	return &Plugin{opts: opts}, nil
}

// Name 返回插件名。
func (p *Plugin) Name() string { return p.opts.Name }

// Inject 声明无依赖:M2 第一段桥不做 JS 侧的服务注入。
func (p *Plugin) Inject() kernel.Inject { return kernel.Inject{} }

// Provide 声明不提供服务:实际提供什么由 JS 在 apply 期间通过 ctx.provide 决定,
// 装配期的预检(M3)会在加载 Artifact 后根据运行结果补上。
func (p *Plugin) Provide() []string { return nil }

// Config 返回 nil:JS 插件的配置 schema 由插件目录里的 config.schema.json 提供,
// 加载器尚未实现(M3)。
func (p *Plugin) Config() *schema.Schema { return nil }

// Apply 装配 JS 运行时,调用入口模块的 apply(ctx, cfg),再提交暂存的 provide/emit。
func (p *Plugin) Apply(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
	// kernel.Apply 的签名没有 context(M1 冻结),这里用一个有界预算兜底。
	actx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	stage := &stager{}
	rt, err := jsrt.New(actx, jsrt.Options{
		Artifact: p.opts.Artifact,
		Logf:     p.record,
		Host: jsrt.Host{
			Provide:   stage.provide,
			Emit:      func(event string, payload any) error { return p.emitTo(stage, event, payload) },
			On:        stage.on,
			Effect:    stage.effect,
			Waterfall: stage.waterfall,
		},
	})
	if err != nil {
		return nil, kernel.Wrap(kernel.CodeInvalid, err, "JS 插件 %s 装配失败", p.opts.Name)
	}

	// AGENTS §6.2 的入口约定:apply(ctx, config)。
	res, err := rt.CallWithHost(actx, "apply", cfg.Raw())
	if err != nil {
		stage.close()
		// apply 失败前登记的 effect 也要清理:它们可能已经建了外部资源。
		runEffects(p.opts.Name, stage.pendingEffects(), context.Background())
		_ = rt.Close(context.Background())
		return nil, kernel.Wrap(kernel.CodeInvalid, err, "JS 插件 %s 的 apply 失败", p.opts.Name)
	}

	waterfalls, ons, effects, provides, emits := stage.drain()

	// waterfall 先于普通监听器注册:中间件通常要包住整条链的其余部分。
	for _, req := range waterfalls {
		req := req
		ctx.OnWaterfall(req.event, func(lctx context.Context, _ *kernel.Context, input any, next kernel.Next) (any, error) {
			return req.invoke(lctx, input, next)
		})
	}

	// 提交顺序 = JS 调用顺序语义:先挂监听器,再注册服务,最后派发事件。
	// 反过来会让 JS 在 apply 里 emit 的事件打不到自己刚注册的处理器。
	for _, req := range ons {
		req := req
		ctx.On(req.event, func(lctx context.Context, _ *kernel.Context, args []any) (any, error) {
			var payload any
			switch len(args) {
			case 0:
			case 1:
				payload = args[0]
			default:
				payload = args
			}
			if _, err := req.invoke(lctx, payload); err != nil {
				return nil, err
			}
			return nil, nil
		})
	}
	for _, req := range provides {
		if _, perr := ctx.Provide(req.name, req.value); perr != nil {
			runEffects(p.opts.Name, effects, context.Background())
			_ = rt.Close(context.Background())
			return nil, kernel.Wrap(kernel.CodeInvalid, perr, "JS 插件 %s 提供服务 %q 失败", p.opts.Name, req.name)
		}
	}
	for _, req := range emits {
		ctx.EmitContext(actx, req.event, req.payload)
	}

	p.mu.Lock()
	p.rt = rt
	p.kctx = ctx
	p.mu.Unlock()

	ctx.Log().Info("JS 插件已装配",
		"exports", rt.Exports(), "ons", len(ons), "waterfalls", len(waterfalls), "effects", len(effects),
		"provides", len(provides), "emits", len(emits), "result", res)
	return func(dctx context.Context) error {
		p.mu.Lock()
		r := p.rt
		p.rt = nil
		p.kctx = nil
		p.mu.Unlock()
		if r == nil {
			return nil
		}
		// JS 的清理必须先于运行时关闭,否则清理函数已经无处可跑。
		errs := runEffects(p.opts.Name, effects, dctx)
		if cerr := r.Close(dctx); cerr != nil {
			errs = append(errs, cerr)
		}
		return errors.Join(errs...)
	}, nil
}

// Logs 返回 JS 侧 ctx.log 的输出(测试与诊断用)。
func (p *Plugin) Logs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.logs))
	copy(out, p.logs)
	return out
}

// emitTo 处理 ctx.emit:apply 期间进暂存队列;运行期直接派发。
//
// 为什么运行期可以直接派发:事件是广播,总线自带锁、每个监听器有独立超时;
// 而 provide/on/effect/waterfall 会改服务表与监听器表,仍只允许在 apply 期间提交。
func (p *Plugin) emitTo(stage *stager, event string, payload any) error {
	if err := stage.emit(event, payload); err == nil {
		return nil
	}
	p.mu.Lock()
	ctx := p.kctx
	p.mu.Unlock()
	if ctx == nil {
		return fmt.Errorf("ctx.emit: 插件已卸载,事件未派发")
	}
	ctx.EmitContext(context.Background(), event, payload)
	return nil
}

func (p *Plugin) record(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	p.mu.Lock()
	p.logs = append(p.logs, msg)
	p.mu.Unlock()
	if p.opts.Logf != nil {
		p.opts.Logf("%s", msg)
	}
}
