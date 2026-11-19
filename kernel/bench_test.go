package kernel_test

import (
	"context"
	"testing"

	"github.com/adag/ada-go/v2/kernel"
)

// 性能基准(AGENTS.md §6.3:单次调用开销 ≤ 20 µs)。
// 基准只提供回归基线,不是绝对指标:数字随机器变化,趋势才是信号。

func BenchmarkEmitSingleListener(b *testing.B) {
	a := kernel.New(kernel.Options{})
	root := a.Root()
	root.On("bench/emit", func(context.Context, *kernel.Context, []any) (any, error) { return nil, nil })
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.Emit("bench/emit")
	}
}

func BenchmarkWaterfallThreeLayers(b *testing.B) {
	a := kernel.New(kernel.Options{})
	root := a.Root()
	for i := 0; i < 3; i++ {
		root.OnWaterfall("bench/flow", func(_ context.Context, _ *kernel.Context, in any, next kernel.Next) (any, error) {
			return next(in)
		})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := root.Waterfall(context.Background(), "bench/flow", i); err != nil {
			b.Fatalf("waterfall 失败: %v", err)
		}
	}
}

func BenchmarkServiceResolve(b *testing.B) {
	a := kernel.New(kernel.Options{})
	prov := kernel.Func("bench-provider", func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		_, err := ctx.Provide("bench-svc", 7)
		return nil, err
	}).WithProvide("bench-svc")
	if _, err := a.Plugin(prov, kernel.NewConfig(nil)); err != nil {
		b.Fatalf("挂载提供者失败: %v", err)
	}

	// 取服务必须有 inject 声明(可见性门禁),所以基准用一个真实消费者上下文。
	var captured *kernel.Context
	cons := kernel.Func("bench-consumer", func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		captured = ctx
		return nil, nil
	}).WithInject("bench-svc")
	if _, err := a.Plugin(cons, kernel.NewConfig(nil)); err != nil {
		b.Fatalf("挂载消费者失败: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, err := kernel.Service[int](captured, "bench-svc")
		if err != nil || v != 7 {
			b.Fatalf("取服务失败: %v %v", v, err)
		}
	}
}
