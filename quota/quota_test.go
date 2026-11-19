package quota_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adag/ada-go/v2/quota"
	"github.com/adag/ada-go/v2/sched"
)

func base() time.Time { return time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC) }

// 1. 未配置配额的 key 一律放行:配额是「按需收紧」,不是默认拒绝。
func TestUnconfiguredIsAllowed(t *testing.T) {
	g := quota.NewGovernor(sched.NewFakeClock(base()))
	release, err := g.Acquire(context.Background(), "unknown")
	if err != nil || release == nil {
		t.Fatalf("未配置配额应放行: %v", err)
	}
	release()
}

// 2. 并发上限:满了返回 EBUSY,释放后可再取。
func TestConcurrencyLimit(t *testing.T) {
	g := quota.NewGovernor(sched.NewFakeClock(base()))
	if err := g.Set("p", quota.Quota{MaxConcurrent: 2}); err != nil {
		t.Fatalf("设置配额失败: %v", err)
	}
	r1, err := g.Acquire(context.Background(), "p")
	if err != nil {
		t.Fatalf("第一次申请失败: %v", err)
	}
	r2, err := g.Acquire(context.Background(), "p")
	if err != nil {
		t.Fatalf("第二次申请失败: %v", err)
	}

	_, err = g.Acquire(context.Background(), "p")
	if !quota.Is(err, quota.CodeBusy) {
		t.Fatalf("并发满应返回 EBUSY,实际 %v", err)
	}
	var qe *quota.Error
	if !errors.As(err, &qe) || !qe.Retryable {
		t.Fatalf("EBUSY 应可重试: %+v", qe)
	}

	r1()
	release, err := g.Acquire(context.Background(), "p")
	if err != nil {
		t.Fatalf("释放后应可再申请: %v", err)
	}
	release()
	r2()
}

// 3. 每分钟调用上限:用尽返回 EQUOTA + 退避时间;窗口重置后可再申请。
func TestPerMinuteLimit(t *testing.T) {
	clk := sched.NewFakeClock(base())
	g := quota.NewGovernor(clk)
	if err := g.Set("p", quota.Quota{MaxCallsPerMinute: 2}); err != nil {
		t.Fatalf("设置配额失败: %v", err)
	}
	for i := 0; i < 2; i++ {
		release, err := g.Acquire(context.Background(), "p")
		if err != nil {
			t.Fatalf("第 %d 次申请失败: %v", i+1, err)
		}
		release()
	}

	_, err := g.Acquire(context.Background(), "p")
	if !quota.Is(err, quota.CodeQuota) {
		t.Fatalf("速率用尽应返回 EQUOTA,实际 %v", err)
	}
	var qe *quota.Error
	if !errors.As(err, &qe) || !qe.Retryable {
		t.Fatalf("EQUOTA 应可重试: %+v", qe)
	}
	if qe.RetryAfter <= 0 || qe.RetryAfter > time.Minute {
		t.Fatalf("退避时间应在 (0, 1m],实际 %s", qe.RetryAfter)
	}

	clk.Advance(time.Minute)
	release, err := g.Acquire(context.Background(), "p")
	if err != nil {
		t.Fatalf("窗口重置后应可申请: %v", err)
	}
	release()
	if stats := g.Snapshot()["p"]; stats.CallsInWindow != 1 || stats.InFlight != 0 {
		t.Fatalf("窗口重置后的计数不正确: %+v", stats)
	}
}

// 4. release 幂等:重复调用不会把计数减成负数。
func TestReleaseIdempotent(t *testing.T) {
	g := quota.NewGovernor(sched.NewFakeClock(base()))
	if err := g.Set("p", quota.Quota{MaxConcurrent: 1}); err != nil {
		t.Fatalf("设置配额失败: %v", err)
	}
	release, err := g.Acquire(context.Background(), "p")
	if err != nil {
		t.Fatalf("申请失败: %v", err)
	}
	release()
	release()
	if stats := g.Snapshot()["p"]; stats.InFlight != 0 {
		t.Fatalf("重复 release 后并发计数应为 0,实际 %d", stats.InFlight)
	}
	if _, err := g.Acquire(context.Background(), "p"); err != nil {
		t.Fatalf("release 后应可再申请: %v", err)
	}
}

// 5. 非法配置在 Set 时被拒绝,而不是留到运行期。
func TestValidate(t *testing.T) {
	g := quota.NewGovernor(nil)
	if err := g.Set("p", quota.Quota{MaxCallsPerMinute: -1}); err == nil {
		t.Fatalf("负配额应被拒绝")
	}
	if err := g.Set("", quota.Quota{MaxConcurrent: 1}); err == nil {
		t.Fatalf("空 key 应被拒绝")
	}
}

// 6. 并发申请(带 -race):并发上限必须被真正守住。
func TestConcurrentAcquire(t *testing.T) {
	const maxConcurrent = 8
	const attempts = 64

	g := quota.NewGovernor(sched.NewFakeClock(base()))
	if err := g.Set("p", quota.Quota{MaxConcurrent: maxConcurrent, MaxCallsPerMinute: attempts}); err != nil {
		t.Fatalf("设置配额失败: %v", err)
	}

	var (
		wg       sync.WaitGroup
		success  atomic.Int32
		start    = make(chan struct{})
		releases = make(chan func(), attempts)
	)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			release, err := g.Acquire(context.Background(), "p")
			if err == nil {
				success.Add(1)
				releases <- release
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := success.Load(); got > maxConcurrent {
		t.Fatalf("并发上限应 ≤ %d,实际成功 %d", maxConcurrent, got)
	}
	for i := 0; i < int(success.Load()); i++ {
		(<-releases)()
	}
	if stats := g.Snapshot()["p"]; stats.InFlight != 0 {
		t.Fatalf("全部释放后并发计数应为 0,实际 %d", stats.InFlight)
	}
}
