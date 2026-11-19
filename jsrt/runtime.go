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

// Options 描述一次运行时装配。
type Options struct {
	Artifact *compile.Artifact
	// MemoryLimit 是 JS 堆上限(字节),0 表示不限。
	MemoryLimit int64
	// DisableDynamicCode 关闭 eval 与 Function 构造器。
	DisableDynamicCode bool
	// Logf 接收 JS 侧 __adaLog 的日志;nil 表示丢弃。
	Logf func(format string, args ...any)
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
	broken bool
	closed bool
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
		art:     opts.Artifact,
		opts:    opts,
		jobs:    make(chan func()),
		quit:    make(chan struct{}),
		stopped: make(chan struct{}),
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
	// 宿主函数必须在 Load 之前注入:模块顶层就可能调用它们。
	// 裸 Runtime 没有 console(C4),所以日志必须显式给。
	//
	// 这里直接写 Realm.Global.SetProp 而不是 SetGlobal:SetGlobal 会走 FromGo,
	// 而 FromGo 不认 engine.Value(函数值),结果会把它变成空对象。
	realm := raw.Realm()
	if err := realm.Global.SetProp(realm, realm.KeyFromGoString("__adaLog"), raw.Function("__adaLog", 2, r.nativeLog(raw))); err != nil {
		return fmt.Errorf("jsrt: 注入 __adaLog 失败: %w", err)
	}

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

// nativeLog 把 JS 的 __adaLog(msg) 转成宿主日志。
func (r *Runtime) nativeLog(raw *moejs.Runtime) moejs.NativeFunc {
	return func(_ *engine.Realm, _ engine.Value, args []engine.Value) (engine.Value, error) {
		if r.opts.Logf != nil && len(args) > 0 {
			if v, err := raw.ToGo(args[0]); err == nil {
				r.opts.Logf("%v", v)
			}
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
	payload := make([][]byte, len(args))
	for i, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			return nil, fmt.Errorf("jsrt: 参数 %d 无法序列化: %w", i, err)
		}
		payload[i] = b
	}

	res := make(chan callResult, 1)
	job := func() { res <- r.callOnWorker(export, payload) }
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
func (r *Runtime) callOnWorker(export string, payload [][]byte) callResult {
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
	vals := make([]moejs.Value, len(payload))
	for i, b := range payload {
		v, err := raw.ParseJSON(b)
		if err != nil {
			return callResult{err: fmt.Errorf("jsrt: 参数 %d 不是合法 JSON: %w", i, err)}
		}
		vals[i] = v
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
