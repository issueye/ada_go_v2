// Package jsrt 是 L2 执行层:每个插件一个 moejs Runtime、一个专属 goroutine。
//
// 边界写死(design §11.3):
//   - Runtime 只能在其专属 goroutine 上使用;只有 Interrupt/ClearInterrupt 可跨协程;
//   - 跨边界只传 JSON,不传函数、Promise、生成器;
//   - 字节码不落盘、不缓存,装配失败只能重新编译加载。
package jsrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/issueye/ada_go_v2/platform/compile"
)

// interruptGrace 是取消后等待 worker 收尾的上限。
// 超过它说明 JS 没有响应中断(例如卡在宿主函数里),运行时视为损坏。
const interruptGrace = 2 * time.Second

var (
	// ErrClosed 表示运行时已关闭或尚未就绪。
	ErrClosed = errors.New("jsrt: 运行时已关闭")
	// ErrBroken 表示运行时在中断后未能收尾,不应再被复用。
	ErrBroken = errors.New("jsrt: 运行时已损坏")
)

// Service 是 JS 侧 ctx.provide 的产物,也是 Go 侧拿到的服务值。
//
// 对象上的函数成员变成方法(调用转发到 JS worker),其余成员按 JSON 转成 Data;
// 非对象值(标量/数组)放在 Value 里。方法调用与普通调用共用中断/超时语义。
type Service struct {
	rt      *Runtime
	name    string
	methods []string
	data    map[string]any
	value   any
	ids     map[string]uint64
}

// Name 返回服务名。
func (s *Service) Name() string { return s.name }

// Methods 返回方法名(排序后的副本)。
func (s *Service) Methods() []string {
	out := make([]string, len(s.methods))
	copy(out, s.methods)
	return out
}

// Data 返回非函数成员。返回值只读,调用方不得修改。
func (s *Service) Data() map[string]any { return s.data }

// Value 返回非对象服务的原始值;对象服务返回 nil。
func (s *Service) Value() any { return s.value }

// Call 调用服务方法。参数与返回值只走 JSON,与内核调用 JS 的其他入口一致。
func (s *Service) Call(ctx context.Context, method string, args ...any) (any, error) {
	id, ok := s.ids[method]
	if !ok {
		return nil, fmt.Errorf("jsrt: 服务 %q 没有方法 %q", s.name, method)
	}
	payload := make([][]byte, len(args))
	for i, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			return nil, fmt.Errorf("jsrt: 方法 %q 的参数 %d 无法序列化: %w", method, i, err)
		}
		payload[i] = b
	}
	return s.rt.invokeMethod(ctx, id, payload)
}

// handlerEntry 是登记在 worker 上的一次 JS 函数调用:函数本身与 this。
type handlerEntry struct {
	fn   moejs.Value
	this moejs.Value
}

// Host 是 JS 侧 ctx 对象能调到的宿主能力。
//
// 回调在 JS 的 worker goroutine 上执行,所以实现方必须自己保证线程安全,
// 并且**不得**直接操作内核状态——桥接层负责把请求暂存,回到内核协程再提交。
// 回调返回 error 会变成 JS 异常;nil 表示该能力未启用。
type Host struct {
	Provide func(name string, svc *Service) error
	Emit    func(event string, payload any) error
	// On 注册「内核 → JS」的回调。invoke 可以在任意 goroutine 上调用:
	// 它会把请求投递到 worker 并在 ctx 取消时中断 JS。
	On func(event string, invoke func(ctx context.Context, payload any) (any, error)) error
	// Effect 登记一个 JS 清理函数;桥接层负责在卸载时按逆序调用 dispose。
	Effect func(label string, dispose func(ctx context.Context) error) error
	// Waterfall 注册一个可改写/否决的中间件。invoke 接收内核的 next,
	// JS 侧通过同步调用 next 继续下游并拿到结果。
	Waterfall func(event string, invoke func(ctx context.Context, input any, next func(any) (any, error)) (any, error)) error
	// Shell 是 JS 侧 ctx.shell / 注入模块的宿主实现。
	Shell func(ctx context.Context, program string, args []string, dir string) (string, error)
	// Tool 是 JS 侧 ctx.tool(spec, handler) 的宿主实现;invoke 在工具被调用时执行 JS handler。
	Tool func(name, summary string, input map[string]any, readOnly bool, invoke func(ctx context.Context, args any) (any, error)) error
}

// ErrReentrant 表示同一运行时在自己的回调链里被再次调用。
// 此时 worker 正阻塞等待宿主响应 next,再投递任务必然死锁,所以直接失败。
var ErrReentrant = errors.New("jsrt: 同一运行时的嵌套调用会死锁")

// Options 描述一次运行时装配。
type Options struct {
	Artifact *compile.Artifact
	// MemoryLimit 是 JS 堆上限(字节),0 表示不限。
	MemoryLimit int64
	// DisableDynamicCode 关闭 eval 与 Function 构造器。
	DisableDynamicCode bool
	// Logf 接收 JS 侧 ctx.log / __adaLog 的日志;nil 表示丢弃。
	Logf func(format string, args ...any)
	// Host 提供 ctx.provide / ctx.emit 的落地实现。
	Host Host
	// Modules 是宿主注入的 moejs 模块(如 "ada:shell");Link 时优先于相对路径。
	Modules map[string]*moejs.Module
	// Globals 是加载前注入的全局变量(宿主能力对象)。
	Globals map[string]any
}

// Runtime 是一个插件实例的 JS 世界。
type Runtime struct {
	art  *compile.Artifact
	opts Options

	jobs    chan func()
	quit    chan struct{}
	stopped chan struct{}
	quitOne sync.Once

	mu     sync.Mutex
	raw    *moejs.Runtime
	hooks  map[string]moejs.Hook
	host   moejs.Value // 传给 apply 的 ctx 对象;只在 worker goroutine 上使用
	broken bool
	closed bool

	// handlers 是 ctx.on 与 JS 服务方法共用的函数表。只在 worker goroutine 上读写:
	// 注册与调用都发生在 worker 上。
	handlers map[uint64]handlerEntry
	nextID   uint64
	// inWaterfall 记录「宿主正在等本运行时的 waterfall 处理器」。
	// 大于 0 时禁止再次调用本运行时,否则 worker 会在等待宿主响应 next 时死锁。
	inWaterfall int
	// inHost 记录「worker 正在回调宿主」。此时宿主要求本运行时执行回调,
	// 会绕回正在忙碌的 worker 而死锁,所以必须快速失败。
	inHost int
}

// enterHost/leaveHost 标记 worker 正在进入宿主回调。
func (r *Runtime) enterHost() {
	r.mu.Lock()
	r.inHost++
	r.mu.Unlock()
}

func (r *Runtime) leaveHost() {
	r.mu.Lock()
	r.inHost--
	r.mu.Unlock()
}

// reentrant 报告「当前调用是否来自本运行时自己的宿主回调」。
func (r *Runtime) reentrant() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inHost > 0
}

// New 装配并加载 Artifact,返回可调用的运行时。
//
// ctx 只约束装配本身;调用期的取消请用 Call 的 ctx。
func New(ctx context.Context, opts Options) (*Runtime, error) {
	if opts.Artifact == nil {
		return nil, fmt.Errorf("jsrt: Artifact 不能为 nil")
	}
	if err := opts.Artifact.Verify(); err != nil {
		return nil, err
	}
	r := &Runtime{
		art:      opts.Artifact,
		opts:     opts,
		jobs:     make(chan func()),
		quit:     make(chan struct{}),
		stopped:  make(chan struct{}),
		handlers: make(map[uint64]handlerEntry),
	}
	go r.worker()

	ready := make(chan error, 1)
	if err := r.enqueue(ctx, func() { ready <- r.load() }); err != nil {
		r.shutdown()
		return nil, err
	}
	select {
	case err := <-ready:
		if err != nil {
			r.shutdown()
			return nil, err
		}
		return r, nil
	case <-ctx.Done():
		r.shutdown()
		return nil, fmt.Errorf("jsrt: 装配被取消: %w", ctx.Err())
	}
}

// worker 是唯一接触 moejs.Runtime 的 goroutine。
func (r *Runtime) worker() {
	defer close(r.stopped)
	for {
		select {
		case job := <-r.jobs:
			job()
		case <-r.quit:
			return
		}
	}
}

func (r *Runtime) enqueue(ctx context.Context, job func()) error {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return ErrClosed
	}
	select {
	case r.jobs <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.stopped:
		return ErrClosed
	}
}

// load 在 worker 上执行:编译 Artifact 的每个模块 → Link → Load。
func (r *Runtime) load() error {
	raw := moejs.NewRuntime(moejs.Options{
		MemoryLimit:        r.opts.MemoryLimit,
		DisableDynamicCode: r.opts.DisableDynamicCode,
	})
	// 宿主能力必须在 Load 之前注入:模块顶层就可能调用它们。
	// 裸 Runtime 没有 console(C4),所以日志必须显式给。
	//
	// 这里直接写 Realm 的 SetProp 而不是 SetGlobal:SetGlobal 会走 FromGo,
	// 而 FromGo 不认 engine.Value(函数值),结果会把它变成空对象。
	realm := raw.Realm()
	ctxObj := realm.NewObject()
	setFn := func(name string, fn moejs.NativeFunc) error {
		return ctxObj.SetProp(realm, realm.KeyFromGoString(name), raw.Function(name, 2, fn))
	}
	if err := setFn("log", r.nativeLog(raw)); err != nil {
		return fmt.Errorf("jsrt: 注入 ctx.log 失败: %w", err)
	}
	if err := setFn("provide", r.nativeProvide(raw)); err != nil {
		return fmt.Errorf("jsrt: 注入 ctx.provide 失败: %w", err)
	}
	if err := setFn("emit", r.nativeEmit(raw)); err != nil {
		return fmt.Errorf("jsrt: 注入 ctx.emit 失败: %w", err)
	}
	if err := setFn("on", r.nativeOn(raw)); err != nil {
		return fmt.Errorf("jsrt: 注入 ctx.on 失败: %w", err)
	}
	if err := setFn("effect", r.nativeEffect(raw)); err != nil {
		return fmt.Errorf("jsrt: 注入 ctx.effect 失败: %w", err)
	}
	if err := setFn("waterfall", r.nativeWaterfall(raw)); err != nil {
		return fmt.Errorf("jsrt: 注入 ctx.waterfall 失败: %w", err)
	}
	if err := setFn("tool", r.nativeTool(raw)); err != nil {
		return fmt.Errorf("jsrt: 注入 ctx.tool 失败: %w", err)
	}
	for name, val := range r.opts.Globals {
		if err := raw.SetGlobal(name, val); err != nil {
			return fmt.Errorf("jsrt: 注入全局 %s 失败: %w", name, err)
		}
	}
	host := engine.ObjectValue(ctxObj)
	// __adaHost 是注入 moejs 模块可用的宿主能力对象(当前只有 shell)。
	hostObj := realm.NewObject()
	if r.opts.Host.Shell != nil {
		if err := hostObj.SetProp(realm, realm.KeyFromGoString("shell"), raw.Function("shell", 3, r.nativeShell(raw))); err != nil {
			return fmt.Errorf("jsrt: 注入 __adaHost.shell 失败: %w", err)
		}
	}
	if err := realm.Global.SetProp(realm, realm.KeyFromGoString("__adaHost"), engine.ObjectValue(hostObj)); err != nil {
		return fmt.Errorf("jsrt: 注入 __adaHost 失败: %w", err)
	}
	// __adaCtx 只是调试入口;插件作者应该用 apply(ctx, config) 的第一个参数。
	if err := realm.Global.SetProp(realm, realm.KeyFromGoString("__adaCtx"), host); err != nil {
		return fmt.Errorf("jsrt: 注入 __adaCtx 失败: %w", err)
	}
	if err := realm.Global.SetProp(realm, realm.KeyFromGoString("__adaLog"), raw.Function("__adaLog", 2, r.nativeLog(raw))); err != nil {
		return fmt.Errorf("jsrt: 注入 __adaLog 失败: %w", err)
	}
	r.host = host

	mods := make(map[string]*moejs.Module, len(r.art.Files))
	for name, code := range r.art.Files {
		m, err := moejs.Compile(name, code)
		if err != nil {
			return fmt.Errorf("jsrt: 编译 %s 失败: %w", name, err)
		}
		mods[name] = m
	}
	entry, ok := mods[r.art.Entry]
	if !ok {
		return fmt.Errorf("jsrt: 入口 %q 不存在", r.art.Entry)
	}
	linked, err := moejs.Link(entry, func(ref moejs.Referrer, spec string) (*moejs.Module, error) {
		if m, ok := r.opts.Modules[spec]; ok {
			return m, nil
		}
		name, ok := r.art.Resolve(ref.Name(), spec)
		if !ok {
			return nil, fmt.Errorf("jsrt: 无法解析 %q(来自 %s);只允许相对导入或宿主注入模块", spec, ref.Name())
		}
		m, ok := mods[name]
		if !ok {
			return nil, fmt.Errorf("jsrt: 解析到不存在的模块 %q", name)
		}
		return m, nil
	})
	if err != nil {
		return fmt.Errorf("jsrt: 链接失败: %w", err)
	}
	if err := raw.Load(linked); err != nil {
		return fmt.Errorf("jsrt: 加载失败: %w", err)
	}

	hooks := make(map[string]moejs.Hook, 8)
	for _, name := range linked.Exports() {
		if h, err := linked.Hook(name); err == nil {
			hooks[name] = h
		}
	}
	r.mu.Lock()
	r.raw = raw
	r.hooks = hooks
	r.mu.Unlock()
	return nil
}

// nativeLog 把 JS 的 ctx.log(...args) 转成宿主日志。
func (r *Runtime) nativeLog(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if r.opts.Logf == nil || len(args) == 0 {
			return engine.Undefined(), nil
		}
		parts := make([]any, 0, len(args))
		for _, a := range args {
			v, err := raw.ToGo(a)
			if err != nil {
				v = "<无法转换>"
			}
			parts = append(parts, v)
		}
		if len(parts) == 1 {
			r.opts.Logf("%v", parts[0])
		} else {
			r.opts.Logf("%v", parts)
		}
		return engine.Undefined(), nil
	}
}

// nativeProvide 实现 ctx.provide(name, value):值经 JSON 转成 Go 值后交给桥接层。
func (r *Runtime) nativeProvide(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if r.opts.Host.Provide == nil {
			return engine.Undefined(), fmt.Errorf("ctx.provide 未启用")
		}
		if len(args) < 2 {
			return engine.Undefined(), fmt.Errorf("ctx.provide(name, value) 需要两个参数")
		}
		rawName, err := raw.ToGo(args[0])
		if err != nil {
			return engine.Undefined(), fmt.Errorf("ctx.provide 的服务名无法转换: %w", err)
		}
		name, ok := rawName.(string)
		if !ok || name == "" {
			return engine.Undefined(), fmt.Errorf("ctx.provide 的服务名必须是非空字符串")
		}
		svc, err := r.describeService(raw, name, args[1])
		if err != nil {
			return engine.Undefined(), err
		}
		if err := r.opts.Host.Provide(name, svc); err != nil {
			return engine.Undefined(), err
		}
		return engine.Undefined(), nil
	}
}

// nativeShell 实现宿主注入模块用的 __adaHost.shell(program, args, dir)。
//
// 返回值是 stdout 字符串;非零退出与 stderr 由宿主实现合并后返回。
func (r *Runtime) nativeShell(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if r.opts.Host.Shell == nil {
			return engine.Undefined(), fmt.Errorf("shell 未启用")
		}
		if len(args) < 1 {
			return engine.Undefined(), fmt.Errorf("shell(program, args, dir) 至少需要 program")
		}
		program, err := toString(raw, args[0])
		if err != nil || program == "" {
			return engine.Undefined(), fmt.Errorf("shell program 必须是非空字符串")
		}
		var argv []string
		if len(args) > 1 {
			v, convErr := raw.ToGo(args[1])
			if convErr != nil {
				return engine.Undefined(), fmt.Errorf("shell args 无法转换: %w", convErr)
			}
			list, ok := v.([]any)
			if !ok {
				return engine.Undefined(), fmt.Errorf("shell args 必须是数组")
			}
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return engine.Undefined(), fmt.Errorf("shell args 只能包含字符串")
				}
				argv = append(argv, s)
			}
		}
		dir := "/workspace"
		if len(args) > 2 {
			if s, convErr := toString(raw, args[2]); convErr == nil && s != "" {
				dir = s
			}
		}
		// 工具调用链路上的取消由 Go 侧 tools 超时兜底;JS 无法把 ctx 传回原生函数。
		out, err := r.opts.Host.Shell(context.Background(), program, argv, dir)
		if err != nil {
			return engine.Undefined(), err
		}
		return raw.FromGo(out)
	}
}

// nativeTool 实现 ctx.tool(spec, handler):handler 是 JS 函数,工具调用时回调。
func (r *Runtime) nativeTool(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if r.opts.Host.Tool == nil {
			return engine.Undefined(), fmt.Errorf("ctx.tool 未启用")
		}
		if len(args) < 2 {
			return engine.Undefined(), fmt.Errorf("ctx.tool(spec, handler) 需要两个参数")
		}
		if !engine.IsCallable(args[1]) {
			return engine.Undefined(), fmt.Errorf("ctx.tool 的第二个参数必须是函数")
		}
		spec, err := raw.ToGo(args[0])
		if err != nil {
			return engine.Undefined(), fmt.Errorf("ctx.tool 的 spec 无法转换: %w", err)
		}
		obj, ok := spec.(map[string]any)
		if !ok {
			return engine.Undefined(), fmt.Errorf("ctx.tool 的 spec 必须是对象")
		}
		name, _ := obj["name"].(string)
		summary, _ := obj["summary"].(string)
		input, _ := obj["input"].(map[string]any)
		readOnly, _ := obj["readOnly"].(bool)
		if name == "" {
			return engine.Undefined(), fmt.Errorf("ctx.tool 需要非空 name")
		}
		r.nextID++
		id := r.nextID
		r.handlers[id] = handlerEntry{fn: args[1], this: engine.Undefined()}
		invoke := func(ctx context.Context, a any) (any, error) {
			return r.invokeHandler(ctx, id, a)
		}
		if err := r.opts.Host.Tool(name, summary, input, readOnly, invoke); err != nil {
			delete(r.handlers, id)
			return engine.Undefined(), err
		}
		return engine.Undefined(), nil
	}
}

func toString(raw *moejs.Runtime, v engine.Value) (string, error) {
	got, err := raw.ToGo(v)
	if err != nil {
		return "", err
	}
	s, ok := got.(string)
	if !ok {
		return "", fmt.Errorf("不是字符串")
	}
	return s, nil
}

// nativeEmit 实现 ctx.emit(event, payload):桥接层负责把它排到内核协程上派发。
func (r *Runtime) nativeEmit(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if r.opts.Host.Emit == nil {
			return engine.Undefined(), fmt.Errorf("ctx.emit 未启用")
		}
		if len(args) < 1 {
			return engine.Undefined(), fmt.Errorf("ctx.emit(event, payload) 至少需要一个参数")
		}
		rawEvent, err := raw.ToGo(args[0])
		if err != nil {
			return engine.Undefined(), fmt.Errorf("ctx.emit 的事件名无法转换: %w", err)
		}
		event, ok := rawEvent.(string)
		if !ok || event == "" {
			return engine.Undefined(), fmt.Errorf("ctx.emit 的事件名必须是非空字符串")
		}
		var payload any
		if len(args) > 1 {
			v, err := raw.ToGo(args[1])
			if err != nil {
				return engine.Undefined(), fmt.Errorf("ctx.emit 的载荷无法转换: %w", err)
			}
			payload = v
		}
		// 宿主可能同步派发事件;若监听器又调回本运行时,靠 inHost 快速失败而不是死锁。
		r.enterHost()
		err = r.opts.Host.Emit(event, payload)
		r.leaveHost()
		if err != nil {
			return engine.Undefined(), err
		}
		return engine.Undefined(), nil
	}
}

// CallResult 是 Call 的返回值容器,避免与调用方共享变量。
type callResult struct {
	value any
	err   error
}

// Call 调用入口模块导出的函数。args 逐个经 JSON 编解码,跨边界不传引用。
//
// ctx 取消时会 Interrupt 正在运行的 JS;若 JS 在 interruptGrace 内仍未收尾,
// 运行时被标记为损坏,后续调用直接失败——把可能半执行的运行时继续复用更危险。
func (r *Runtime) Call(ctx context.Context, export string, args ...any) (any, error) {
	return r.call(ctx, export, false, args...)
}

// CallWithHost 在参数最前面插入 ctx 对象。
// 插件入口按 AGENTS §6.2 的约定写成 apply(ctx, config)。
func (r *Runtime) CallWithHost(ctx context.Context, export string, args ...any) (any, error) {
	return r.call(ctx, export, true, args...)
}

func (r *Runtime) call(ctx context.Context, export string, withHost bool, args ...any) (any, error) {
	payload := make([][]byte, len(args))
	for i, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			return nil, fmt.Errorf("jsrt: 参数 %d 无法序列化: %w", i, err)
		}
		payload[i] = b
	}

	res := make(chan callResult, 1)
	job := func() { res <- r.callOnWorker(export, payload, withHost) }
	if err := r.enqueue(ctx, job); err != nil {
		return nil, err
	}

	select {
	case got := <-res:
		return got.value, got.err
	case <-ctx.Done():
		r.interrupt(ctx.Err())
		select {
		case got := <-res:
			// 拿到被中断调用的结局后清掉中断标志,保证运行时还能继续用。
			r.clearInterrupt()
			if got.err != nil {
				return nil, got.err
			}
			return got.value, fmt.Errorf("jsrt: 调用 %q 已取消: %w", export, ctx.Err())
		case <-time.After(interruptGrace):
			r.mu.Lock()
			r.broken = true
			r.mu.Unlock()
			return nil, fmt.Errorf("%w: 调用 %q 在 %s 内未响应取消", ErrBroken, export, interruptGrace)
		}
	}
}

// callOnWorker 只在 worker goroutine 上执行。
func (r *Runtime) callOnWorker(export string, payload [][]byte, withHost bool) callResult {
	r.mu.Lock()
	raw, hooks, broken := r.raw, r.hooks, r.broken
	r.mu.Unlock()
	switch {
	case broken:
		return callResult{err: ErrBroken}
	case raw == nil:
		return callResult{err: ErrClosed}
	}
	h, ok := hooks[export]
	if !ok {
		return callResult{err: fmt.Errorf("jsrt: 模块未导出 %q", export)}
	}
	vals := make([]moejs.Value, 0, len(payload)+1)
	if withHost {
		vals = append(vals, r.host)
	}
	for i, b := range payload {
		v, err := raw.ParseJSON(b)
		if err != nil {
			return callResult{err: fmt.Errorf("jsrt: 参数 %d 不是合法 JSON: %w", i, err)}
		}
		vals = append(vals, v)
	}
	out, err := raw.Call(h, vals...)
	if err != nil {
		return callResult{err: r.wrap(raw, err)}
	}
	v, err := raw.ToGo(out)
	if err != nil {
		return callResult{err: fmt.Errorf("jsrt: 返回值无法转成 Go 值: %w", err)}
	}
	return callResult{value: v}
}

// wrap 把 moejs 的错误翻译成带堆栈的宿主错误。
func (r *Runtime) wrap(raw *moejs.Runtime, err error) error {
	var interrupted *moejs.InterruptedError
	if errors.As(err, &interrupted) {
		return fmt.Errorf("jsrt: JS 执行被中断: %w", err)
	}
	var exc *moejs.Exception
	if errors.As(err, &exc) {
		return fmt.Errorf("jsrt: JS 异常: %s", raw.StackTrace(exc))
	}
	return fmt.Errorf("jsrt: JS 调用失败: %w", err)
}

func (r *Runtime) interrupt(reason error) {
	r.mu.Lock()
	raw := r.raw
	r.mu.Unlock()
	if raw != nil {
		raw.Interrupt(reason)
	}
}

func (r *Runtime) clearInterrupt() {
	r.mu.Lock()
	raw := r.raw
	r.mu.Unlock()
	if raw != nil {
		raw.ClearInterrupt()
	}
}

// nativeOn 实现 ctx.on(event, handler):把 JS 函数登记进 handlers,
// 并把「如何调用它」交给桥接层。登记发生在 worker 上,所以 handlers 无需加锁。
func (r *Runtime) nativeOn(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if r.opts.Host.On == nil {
			return engine.Undefined(), fmt.Errorf("ctx.on 未启用")
		}
		if len(args) < 2 {
			return engine.Undefined(), fmt.Errorf("ctx.on(event, handler) 需要两个参数")
		}
		rawEvent, err := raw.ToGo(args[0])
		if err != nil {
			return engine.Undefined(), fmt.Errorf("ctx.on 的事件名无法转换: %w", err)
		}
		event, ok := rawEvent.(string)
		if !ok || event == "" {
			return engine.Undefined(), fmt.Errorf("ctx.on 的事件名必须是非空字符串")
		}
		if !engine.IsCallable(args[1]) {
			return engine.Undefined(), fmt.Errorf("ctx.on 的第二个参数必须是函数")
		}
		r.nextID++
		id := r.nextID
		r.handlers[id] = handlerEntry{fn: args[1], this: engine.Undefined()}
		if err := r.opts.Host.On(event, func(ctx context.Context, payload any) (any, error) {
			return r.invokeHandler(ctx, id, payload)
		}); err != nil {
			delete(r.handlers, id)
			return engine.Undefined(), err
		}
		return engine.Undefined(), nil
	}
}

// nativeEffect 实现 ctx.effect(label, cleanup):登记清理函数,卸载时按逆序调用。
func (r *Runtime) nativeEffect(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if r.opts.Host.Effect == nil {
			return engine.Undefined(), fmt.Errorf("ctx.effect 未启用")
		}
		if len(args) < 2 {
			return engine.Undefined(), fmt.Errorf("ctx.effect(label, cleanup) 需要两个参数")
		}
		rawLabel, err := raw.ToGo(args[0])
		if err != nil {
			return engine.Undefined(), fmt.Errorf("ctx.effect 的标签无法转换: %w", err)
		}
		label, ok := rawLabel.(string)
		if !ok || label == "" {
			return engine.Undefined(), fmt.Errorf("ctx.effect 的标签必须是非空字符串")
		}
		if !engine.IsCallable(args[1]) {
			return engine.Undefined(), fmt.Errorf("ctx.effect 的第二个参数必须是函数")
		}
		r.nextID++
		id := r.nextID
		r.handlers[id] = handlerEntry{fn: args[1], this: engine.Undefined()}
		if err := r.opts.Host.Effect(label, func(ctx context.Context) error {
			_, err := r.invokeCallback(ctx, id, nil)
			return err
		}); err != nil {
			delete(r.handlers, id)
			return engine.Undefined(), err
		}
		return engine.Undefined(), nil
	}
}

// invokeHandler 调用一个 ctx.on 注册的 JS 函数;可从任意 goroutine 调用。
func (r *Runtime) invokeHandler(ctx context.Context, id uint64, payload any) (any, error) {
	if r.reentrant() {
		return nil, ErrReentrant
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("jsrt: 事件载荷无法序列化: %w", err)
	}
	return r.invokeCallback(ctx, id, [][]byte{b})
}

// invokeCallback 在 worker 上调用一个已登记的函数;payload 为逐个 JSON 编码的参数。
func (r *Runtime) invokeCallback(ctx context.Context, id uint64, payload [][]byte) (any, error) {
	if r.reentrant() {
		return nil, ErrReentrant
	}
	res := make(chan callResult, 1)
	if err := r.enqueue(ctx, func() { res <- r.invokeOnWorker(id, payload) }); err != nil {
		return nil, err
	}
	select {
	case got := <-res:
		return got.value, got.err
	case <-ctx.Done():
		r.interrupt(ctx.Err())
		select {
		case <-res:
			r.clearInterrupt()
			return nil, ctx.Err()
		case <-time.After(interruptGrace):
			r.mu.Lock()
			r.broken = true
			r.mu.Unlock()
			return nil, fmt.Errorf("%w: 回调 %d 在 %s 内未响应取消", ErrBroken, id, interruptGrace)
		}
	}
}

func (r *Runtime) invokeOnWorker(id uint64, payload [][]byte) callResult {
	r.mu.Lock()
	raw, broken := r.raw, r.broken
	r.mu.Unlock()
	switch {
	case broken:
		return callResult{err: ErrBroken}
	case raw == nil:
		return callResult{err: ErrClosed}
	}
	entry, ok := r.handlers[id]
	if !ok {
		return callResult{err: fmt.Errorf("jsrt: 回调 %d 不存在", id)}
	}
	vals := make([]moejs.Value, len(payload))
	for i, b := range payload {
		v, err := raw.ParseJSON(b)
		if err != nil {
			return callResult{err: fmt.Errorf("jsrt: 回调参数 %d 不是合法 JSON: %w", i, err)}
		}
		vals[i] = v
	}
	out, err := raw.CallFunction(entry.fn, entry.this, vals...)
	if err != nil {
		return callResult{err: r.wrap(raw, err)}
	}
	// 回调的返回值对内核没有意义(emit / 清理语义),转换失败不应让调用失败。
	v, convErr := raw.ToGo(out)
	if convErr != nil {
		return callResult{value: nil}
	}
	return callResult{value: v}
}

// describeService 把 JS 侧给 ctx.provide 的值转成 Go 侧可用的 Service。
//
// 只认对象自身的属性:函数成为方法,其余按 JSON 成为数据。
// 深层嵌套里的函数不会变成方法——「跨边界不传递函数」是 C3 的硬约束。
func (r *Runtime) describeService(raw *moejs.Runtime, name string, v moejs.Value) (*Service, error) {
	svc := &Service{rt: r, name: name, ids: map[string]uint64{}}
	if !v.IsObject() {
		data, err := raw.ToGo(v)
		if err != nil {
			return nil, fmt.Errorf("jsrt: 服务 %q 的值无法转换: %w", name, err)
		}
		svc.value = data
		return svc, nil
	}
	realm := raw.Realm()
	obj := v.AsObject()
	svc.data = map[string]any{}
	for _, key := range obj.OwnPropertyKeys() {
		if key.IsSymbol() || key.IsPrivate() {
			continue
		}
		keyName := key.GoString()
		val, err := obj.GetProp(realm, key)
		if err != nil {
			return nil, fmt.Errorf("jsrt: 服务 %q 的属性 %q 读取失败: %w", name, keyName, err)
		}
		if engine.IsCallable(val) {
			r.nextID++
			id := r.nextID
			// this 绑定到服务对象本身:方法里的 this.x 才能读到兄弟成员。
			r.handlers[id] = handlerEntry{fn: val, this: v}
			svc.ids[keyName] = id
			svc.methods = append(svc.methods, keyName)
			continue
		}
		data, err := raw.ToGo(val)
		if err != nil {
			return nil, fmt.Errorf("jsrt: 服务 %q 的属性 %q 无法转换(深层函数不能跨边界): %w", name, keyName, err)
		}
		svc.data[keyName] = data
	}
	sort.Strings(svc.methods)
	return svc, nil
}

// invokeMethod 调用 JS 服务方法;可从任意 goroutine 调用。
func (r *Runtime) invokeMethod(ctx context.Context, id uint64, payload [][]byte) (any, error) {
	if r.reentrant() {
		return nil, ErrReentrant
	}
	res := make(chan callResult, 1)
	if err := r.enqueue(ctx, func() { res <- r.invokeMethodOnWorker(id, payload) }); err != nil {
		return nil, err
	}
	select {
	case got := <-res:
		return got.value, got.err
	case <-ctx.Done():
		r.interrupt(ctx.Err())
		select {
		case <-res:
			r.clearInterrupt()
			return nil, ctx.Err()
		case <-time.After(interruptGrace):
			r.mu.Lock()
			r.broken = true
			r.mu.Unlock()
			return nil, fmt.Errorf("%w: 方法调用 %d 在 %s 内未响应取消", ErrBroken, id, interruptGrace)
		}
	}
}

func (r *Runtime) invokeMethodOnWorker(id uint64, payload [][]byte) callResult {
	r.mu.Lock()
	raw, broken := r.raw, r.broken
	r.mu.Unlock()
	switch {
	case broken:
		return callResult{err: ErrBroken}
	case raw == nil:
		return callResult{err: ErrClosed}
	}
	entry, ok := r.handlers[id]
	if !ok {
		return callResult{err: fmt.Errorf("jsrt: 服务方法 %d 不存在", id)}
	}
	vals := make([]moejs.Value, len(payload))
	for i, b := range payload {
		v, err := raw.ParseJSON(b)
		if err != nil {
			return callResult{err: fmt.Errorf("jsrt: 方法参数 %d 不是合法 JSON: %w", i, err)}
		}
		vals[i] = v
	}
	out, err := raw.CallFunction(entry.fn, entry.this, vals...)
	if err != nil {
		return callResult{err: r.wrap(raw, err)}
	}
	v, err := raw.ToGo(out)
	if err != nil {
		return callResult{err: fmt.Errorf("jsrt: 方法返回值无法转换: %w", err)}
	}
	return callResult{value: v}
}

// nativeWaterfall 实现 ctx.waterfall(event, handler):登记为内核 waterfall 监听器。
func (r *Runtime) nativeWaterfall(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if r.opts.Host.Waterfall == nil {
			return engine.Undefined(), fmt.Errorf("ctx.waterfall 未启用")
		}
		if len(args) < 2 {
			return engine.Undefined(), fmt.Errorf("ctx.waterfall(event, handler) 需要两个参数")
		}
		rawEvent, err := raw.ToGo(args[0])
		if err != nil {
			return engine.Undefined(), fmt.Errorf("ctx.waterfall 的事件名无法转换: %w", err)
		}
		event, ok := rawEvent.(string)
		if !ok || event == "" {
			return engine.Undefined(), fmt.Errorf("ctx.waterfall 的事件名必须是非空字符串")
		}
		if !engine.IsCallable(args[1]) {
			return engine.Undefined(), fmt.Errorf("ctx.waterfall 的第二个参数必须是函数")
		}
		r.nextID++
		id := r.nextID
		r.handlers[id] = handlerEntry{fn: args[1], this: engine.Undefined()}
		if err := r.opts.Host.Waterfall(event, func(ctx context.Context, input any, next func(any) (any, error)) (any, error) {
			return r.invokeWaterfall(ctx, id, input, next)
		}); err != nil {
			delete(r.handlers, id)
			return engine.Undefined(), err
		}
		return engine.Undefined(), nil
	}
}

// wfValue 是 waterfall 处理器与宿主之间的一次往返结果。
type wfValue struct {
	value any
	err   error
}

// invokeWaterfall 在 worker 上执行 JS 中间件;JS 调 next 时在这里回落到宿主。
//
// 环的规避:同一运行时不允许嵌套(ErrReentrant)。跨运行时的链是安全的——
// 每个运行时有自己的 worker,宿主负责把 next 调用串起来。
func (r *Runtime) invokeWaterfall(ctx context.Context, id uint64, input any, next func(any) (any, error)) (any, error) {
	r.mu.Lock()
	if r.inWaterfall > 0 || r.inHost > 0 {
		r.mu.Unlock()
		return nil, ErrReentrant
	}
	r.inWaterfall++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.inWaterfall--
		r.mu.Unlock()
	}()

	b, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("jsrt: waterfall 输入无法序列化: %w", err)
	}
	res := make(chan callResult, 1)
	nextReq := make(chan wfValue, 1)
	nextResp := make(chan wfValue, 1)
	if err := r.enqueue(ctx, func() { res <- r.invokeWaterfallOnWorker(id, b, nextReq, nextResp) }); err != nil {
		return nil, err
	}
	for {
		select {
		case got := <-res:
			return got.value, got.err
		case req := <-nextReq:
			// 在监听器所在的 goroutine 上继续下游,与内核 waterfall 语义一致。
			v, nextErr := next(req.value)
			nextResp <- wfValue{value: v, err: nextErr}
		case <-ctx.Done():
			r.interrupt(ctx.Err())
			// 若 worker 正阻塞在 next 上,用一条错误结果把它放出来,避免 goroutine 泄漏。
			select {
			case nextResp <- wfValue{err: ctx.Err()}:
			default:
			}
			select {
			case <-res:
				r.clearInterrupt()
				return nil, ctx.Err()
			case <-time.After(interruptGrace):
				r.mu.Lock()
				r.broken = true
				r.mu.Unlock()
				return nil, fmt.Errorf("%w: waterfall 处理器 %d 在 %s 内未响应取消", ErrBroken, id, interruptGrace)
			}
		}
	}
}

func (r *Runtime) invokeWaterfallOnWorker(id uint64, payload []byte, nextReq, nextResp chan wfValue) callResult {
	r.mu.Lock()
	raw, broken := r.raw, r.broken
	r.mu.Unlock()
	switch {
	case broken:
		return callResult{err: ErrBroken}
	case raw == nil:
		return callResult{err: ErrClosed}
	}
	entry, ok := r.handlers[id]
	if !ok {
		return callResult{err: fmt.Errorf("jsrt: waterfall 处理器 %d 不存在", id)}
	}
	input, err := raw.ParseJSON(payload)
	if err != nil {
		return callResult{err: fmt.Errorf("jsrt: waterfall 输入不是合法 JSON: %w", err)}
	}
	var called bool
	nextFn := raw.Function("next", 1, func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if called {
			return engine.Undefined(), fmt.Errorf("waterfall 处理器重复调用 next")
		}
		called = true
		var v any
		if len(args) > 0 {
			converted, convErr := raw.ToGo(args[0])
			if convErr != nil {
				return engine.Undefined(), fmt.Errorf("next 的参数无法转换: %w", convErr)
			}
			v = converted
		}
		nextReq <- wfValue{value: v}
		got := <-nextResp
		if got.err != nil {
			return engine.Undefined(), got.err
		}
		out, fromErr := raw.FromGo(got.value)
		if fromErr != nil {
			return engine.Undefined(), fmt.Errorf("next 的结果无法转换: %w", fromErr)
		}
		return out, nil
	})
	out, err := raw.CallFunction(entry.fn, entry.this, input, nextFn)
	if err != nil {
		return callResult{err: r.wrap(raw, err)}
	}
	v, convErr := raw.ToGo(out)
	if convErr != nil {
		return callResult{value: nil}
	}
	return callResult{value: v}
}

// Exports 返回入口模块的导出名(排序),用于诊断与装配报告。
func (r *Runtime) Exports() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.hooks))
	for name := range r.hooks {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Close 停止运行时。可重复调用。
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()

	r.interrupt(ErrClosed)
	r.stop()
	select {
	case <-r.stopped:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("jsrt: 关闭超时: %w", ctx.Err())
	}
}

// shutdown 是 New 失败路径的清理:停 worker 但不要求优雅(还没有可用的 Runtime)。
func (r *Runtime) shutdown() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stop()
	<-r.stopped
}

func (r *Runtime) stop() { r.quitOne.Do(func() { close(r.quit) }) }
