package flow_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/v2/flow"
	"github.com/adag/ada-go/v2/kernel"
)

// fakeRT 是可脚本化的 Runtime:记录调用,回调决定结果。
type fakeRT struct {
	mu     sync.Mutex
	calls  []string
	events []string
	args   map[string]map[string]any

	callFn  func(ctx context.Context, service, method string, args map[string]any) (any, error)
	eventFn func(ctx context.Context, name, mode string, input any) (any, error)
}

func newFakeRT() *fakeRT { return &fakeRT{args: map[string]map[string]any{}} }

func (f *fakeRT) CallService(ctx context.Context, service, method string, args map[string]any) (any, error) {
	key := service + "." + method
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.args[key] = args
	f.mu.Unlock()
	if f.callFn != nil {
		return f.callFn(ctx, service, method, args)
	}
	return nil, nil
}

func (f *fakeRT) DispatchEvent(ctx context.Context, name, mode string, input any) (any, error) {
	f.mu.Lock()
	f.events = append(f.events, name)
	f.mu.Unlock()
	if f.eventFn != nil {
		return f.eventFn(ctx, name, mode, input)
	}
	return input, nil
}

func (f *fakeRT) callCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == key {
			n++
		}
	}
	return n
}

func newApp(t *testing.T) *kernel.App {
	t.Helper()
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	return app
}

// 1. 顺序执行:use 与 event,参数用 $.path 引用上一步结果。
func TestRunSequential(t *testing.T) {
	rt := newFakeRT()
	rt.callFn = func(ctx context.Context, service, method string, args map[string]any) (any, error) {
		switch method {
		case "m1":
			return float64(2), nil
		case "m2":
			if args["v"] != float64(2) {
				t.Errorf("m2 的参数没有解析成上一步结果: %v", args)
			}
			return float64(4), nil
		}
		return nil, nil
	}

	f := flow.New("seq").
		Add(flow.Step{ID: "a", Use: &flow.UseStep{Service: "svc", Method: "m1"}}).
		Add(flow.Step{ID: "b", Use: &flow.UseStep{Service: "svc", Method: "m2", Args: map[string]any{"v": "$.a"}}}).
		Add(flow.Step{ID: "c", Event: &flow.EventStep{Name: "flow/step", Input: "$.b"}})

	res, err := f.Run(context.Background(), rt, flow.Options{})
	if err != nil {
		t.Fatalf("运行失败: %v", err)
	}
	if strings.Join(res.Completed, ",") != "a,b,c" {
		t.Fatalf("完成顺序不正确: %v", res.Completed)
	}
	if res.Values["a"] != float64(2) || res.Values["b"] != float64(4) || res.Values["c"] != float64(4) {
		t.Fatalf("步骤结果不正确: %v", res.Values)
	}
	if len(rt.calls) != 2 || rt.calls[0] != "svc.m1" || rt.calls[1] != "svc.m2" {
		t.Fatalf("调用顺序不正确: %v", rt.calls)
	}
	if len(rt.events) != 1 || rt.events[0] != "flow/step" {
		t.Fatalf("事件不正确: %v", rt.events)
	}
}

// 2. when 为假则跳过;when 为真且声明 abort 则否决。
func TestWhenSkipAndAbort(t *testing.T) {
	rt := newFakeRT()
	rt.callFn = func(context.Context, string, string, map[string]any) (any, error) { return float64(1), nil }

	f := flow.New("gate").
		Add(flow.Step{ID: "a", Use: &flow.UseStep{Service: "svc", Method: "m"}}).
		Add(flow.Step{ID: "b", When: "$.a == 2", Use: &flow.UseStep{Service: "svc", Method: "m"}}).
		Add(flow.Step{ID: "c", When: "$.a == 1", Abort: "blocked"})

	res, err := f.Run(context.Background(), rt, flow.Options{})
	if !kernel.IsCode(err, kernel.Code(flow.CodeAborted)) {
		t.Fatalf("应返回 EABORTED,实际 %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "b" {
		t.Fatalf("b 应被跳过: %v", res.Skipped)
	}
	if len(rt.calls) != 1 {
		t.Fatalf("被跳过的步骤不应调用服务,实际调用 %d 次", len(rt.calls))
	}
}

// 3. retry:失败两次后成功(总尝试 = max+1)。
func TestRetrySucceeds(t *testing.T) {
	rt := newFakeRT()
	attempts := 0
	rt.callFn = func(context.Context, string, string, map[string]any) (any, error) {
		attempts++
		if attempts <= 2 {
			return nil, errors.New("暂时失败")
		}
		return float64(7), nil
	}
	f := flow.New("retry").
		Add(flow.Step{ID: "a", Retry: &flow.Retry{Max: 2}, Use: &flow.UseStep{Service: "svc", Method: "m"}})

	res, err := f.Run(context.Background(), rt, flow.Options{})
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("应尝试 3 次,实际 %d", attempts)
	}
	if res.Values["a"] != float64(7) {
		t.Fatalf("结果不正确: %v", res.Values["a"])
	}
}

// 4. retry 用尽后失败,并带上步骤名与尝试次数。
func TestRetryExhausted(t *testing.T) {
	rt := newFakeRT()
	rt.callFn = func(context.Context, string, string, map[string]any) (any, error) {
		return nil, errors.New("一直失败")
	}
	f := flow.New("fail").
		Add(flow.Step{ID: "a", Retry: &flow.Retry{Max: 1}, Use: &flow.UseStep{Service: "svc", Method: "m"}})

	_, err := f.Run(context.Background(), rt, flow.Options{})
	if !kernel.IsCode(err, kernel.Code(flow.CodeFlow)) {
		t.Fatalf("应返回 EFLOW,实际 %v", err)
	}
	if !strings.Contains(err.Error(), "步骤 \"a\"") {
		t.Fatalf("错误应点名步骤: %v", err)
	}
	if got := rt.callCount("svc.m"); got != 2 {
		t.Fatalf("应尝试 2 次,实际 %d", got)
	}
}

// 5. timeout:步骤超时后失败,调用收到 ctx 取消。
func TestStepTimeout(t *testing.T) {
	rt := newFakeRT()
	rt.callFn = func(ctx context.Context, _ string, _ string, _ map[string]any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f := flow.New("timeout").
		Add(flow.Step{ID: "a", Timeout: flow.Duration(20 * time.Millisecond),
			Use: &flow.UseStep{Service: "svc", Method: "m"}})

	start := time.Now()
	_, err := f.Run(context.Background(), rt, flow.Options{})
	if err == nil {
		t.Fatalf("超时应失败")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("超时没有及时返回: %s", elapsed)
	}
}

// 6. checkpoint/resume:第二次运行跳过已检查点的步骤,并保留其状态。
func TestCheckpointResume(t *testing.T) {
	store := flow.NewMemoryStore()
	rt := newFakeRT()
	aCalls := 0
	bAttempts := 0
	rt.callFn = func(_ context.Context, _ string, method string, args map[string]any) (any, error) {
		switch method {
		case "a":
			aCalls++
			return float64(10), nil
		case "b":
			bAttempts++
			if bAttempts == 1 {
				return nil, errors.New("第一次失败")
			}
			if args["v"] != float64(10) {
				t.Errorf("恢复后 b 的参数不正确: %v", args)
			}
			return float64(11), nil
		}
		return nil, nil
	}

	f := flow.New("resume").
		Add(flow.Step{ID: "a", Checkpoint: true, Use: &flow.UseStep{Service: "svc", Method: "a"}}).
		Add(flow.Step{ID: "b", Use: &flow.UseStep{Service: "svc", Method: "b", Args: map[string]any{"v": "$.a"}}})

	opts := flow.Options{Store: store, RunID: "run-1"}
	if _, err := f.Run(context.Background(), rt, opts); err == nil {
		t.Fatalf("第一次运行应失败")
	}
	if aCalls != 1 {
		t.Fatalf("a 应执行一次,实际 %d", aCalls)
	}

	res, err := f.Run(context.Background(), rt, opts)
	if err != nil {
		t.Fatalf("恢复后应成功: %v", err)
	}
	if aCalls != 1 {
		t.Fatalf("已有检查点的步骤不应重跑,实际 %d 次", aCalls)
	}
	if res.Values["a"] != float64(10) || res.Values["b"] != float64(11) {
		t.Fatalf("恢复后的结果不正确: %v", res.Values)
	}
}

// 7. OnFailure:失败后执行补偿步骤,并记录失败信息。
func TestOnFailureSteps(t *testing.T) {
	rt := newFakeRT()
	rt.callFn = func(context.Context, string, string, map[string]any) (any, error) {
		return nil, errors.New("坏了")
	}
	f := flow.New("with-failure").
		Add(flow.Step{ID: "a", Use: &flow.UseStep{Service: "svc", Method: "m"}})
	// OnFailure 是独立字段:主链失败后执行,不进入 completed。
	f.OnFailure = []flow.Step{{ID: "notify", Event: &flow.EventStep{Name: "flow/failed"}}}

	res, err := f.Run(context.Background(), rt, flow.Options{})
	if err == nil {
		t.Fatalf("失败步骤应返回错误")
	}
	if len(rt.events) != 1 || rt.events[0] != "flow/failed" {
		t.Fatalf("OnFailure 事件未派发: %v", rt.events)
	}
	if res.Values["__failureErrors"] == nil {
		t.Fatalf("失败信息应记录到 __failureErrors")
	}
}

// 8. 声明式 JSON:合法可解析并执行;非法在解析/校验期拒绝。
func TestParseFlowJSON(t *testing.T) {
	valid := []byte(`{
	  "flow": "review",
	  "steps": [
	    {"id": "read", "use": {"service": "svc", "method": "m"}},
	    {"id": "scan", "event": {"name": "flow/scan", "mode": "waterfall"},
	     "when": "$.read == null", "retry": {"max": 1, "backoff": "1ms"},
	     "timeout": "2s", "checkpoint": true}
	  ]
	}`)
	f, err := flow.Parse(valid)
	if err != nil {
		t.Fatalf("合法 JSON 解析失败: %v", err)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("校验失败: %v", err)
	}

	bad := map[string]string{
		"未知字段":    `{"flow":"x","steps":[{"id":"a","use":{"service":"s","method":"m"},"foo":1}]}`,
		"空 steps": `{"flow":"x","steps":[]}`,
		"重复 id":   `{"flow":"x","steps":[{"id":"a","use":{"service":"s","method":"m"}},{"id":"a","abort":"b"}]}`,
		"无动作":     `{"flow":"x","steps":[{"id":"a"}]}`,
		"两个动作":    `{"flow":"x","steps":[{"id":"a","use":{"service":"s","method":"m"},"abort":"b"}]}`,
		"坏 when":  `{"flow":"x","steps":[{"id":"a","abort":"b","when":"$.x >"}]}`,
		"坏时长":     `{"flow":"x","steps":[{"id":"a","use":{"service":"s","method":"m"},"timeout":"bad"}]}`,
		"坏事件模式":   `{"flow":"x","steps":[{"id":"a","event":{"name":"e","mode":"parallel"}}]}`,
	}
	for name, raw := range bad {
		if _, err := flow.Parse([]byte(raw)); err == nil {
			t.Fatalf("%s: 应被拒绝", name)
		}
	}
}

// 9. 与内核集成:KernelRuntime 走 inject 门禁与服务/waterfall。
func TestKernelRuntimeIntegration(t *testing.T) {
	app := newApp(t)
	app.Root().OnWaterfall("flow/op", func(_ context.Context, _ *kernel.Context, in any, next kernel.Next) (any, error) {
		n, _ := in.(float64)
		return next(n + 1)
	})

	provider := kernel.Func("provider", func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		_, err := ctx.Provide("calc", calcService{})
		return nil, err
	}).WithProvide("calc")
	if _, err := app.Plugin(provider, kernel.NewConfig(nil)); err != nil {
		t.Fatalf("挂载提供者失败: %v", err)
	}

	var result *flow.Result
	consumer := kernel.Func("consumer", func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		f := flow.New("kernel").
			Add(flow.Step{ID: "dbl", Use: &flow.UseStep{Service: "calc", Method: "double", Args: map[string]any{"n": float64(2)}}}).
			Add(flow.Step{ID: "op", Event: &flow.EventStep{Name: "flow/op", Mode: "waterfall", Input: "$.dbl"}})
		rt := flow.NewKernelRuntime(ctx)
		var err error
		result, err = f.Run(context.Background(), rt, flow.Options{})
		return nil, err
	}).WithInject("calc")
	if _, err := app.Plugin(consumer, kernel.NewConfig(nil)); err != nil {
		t.Fatalf("挂载消费者失败: %v", err)
	}
	if result == nil {
		t.Fatalf("流水线没有产出结果")
	}
	if result.Values["dbl"] != float64(4) {
		t.Fatalf("服务调用结果不正确: %v", result.Values["dbl"])
	}
	if result.Values["op"] != float64(5) {
		t.Fatalf("waterfall 结果不正确: %v", result.Values["op"])
	}
}

// calcService 是实现了 flow.Invoker 的服务(测试用)。
type calcService struct{}

func (calcService) Invoke(_ context.Context, method string, args map[string]any) (any, error) {
	if method != "double" {
		return nil, kernel.Fail(kernel.CodeInvalid, "未知方法 %q", method)
	}
	n, _ := args["n"].(float64)
	return n * 2, nil
}
