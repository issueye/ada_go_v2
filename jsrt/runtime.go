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

	"github.com/adag/ada-go/v2/compile"
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
}

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
	host := engine.ObjectValue(ctxObj)
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
		name, ok := r.art.Resolve(ref.Name(), spec)
		if !ok {
			return nil, fmt.Errorf("jsrt: 无法解析 %q(来自 %s);只允许相对导入", spec, ref.Name())
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
		if err := r.opts.Host.Emit(event, payload); err != nil {
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
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("jsrt: 事件载荷无法序列化: %w", err)
	}
	return r.invokeCallback(ctx, id, [][]byte{b})
}

// invokeCallback 在 worker 上调用一个已登记的函数;payload 为逐个 JSON 编码的参数。
func (r *Runtime) invokeCallback(ctx context.Context, id uint64, payload [][]byte) (any, error) {
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
