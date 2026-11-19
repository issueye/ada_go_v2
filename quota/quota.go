// Package quota 是 M4 的配额与回压:L2 基础设施(不是插件)。
//
// 它只做「记账 + 拒绝」:按 key(插件 id)限制每分钟调用数与并发数;
// 校验/审计/退化策略属于调用方或 policy 插件。配额是**服务侧**的约束,
// 所以内核不需要认识它(guard 强制 kernel 不依赖本包)。
package quota

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/adag/ada-go/v2/sched"
)

// 错误码(seam-api §1.2 之外的编排层补充)。
const (
	// CodeQuota 表示超出速率配额(可退避重试)。
	CodeQuota = "EQUOTA"
	// CodeBusy 表示并发已满(回压,调用方应退避或降级)。
	CodeBusy = "EBUSY"
)

// window 是速率配额的窗口长度(seam-api §12.3:每分钟调用数)。
const window = time.Minute

// Quota 是某个 key 的配额。零值字段表示「不限制」。
type Quota struct {
	MaxCallsPerMinute int
	MaxConcurrent     int
	// MaxCPUTime 与 MaxMemory 是设计里的预留项:强制它们需要计时/计量,
	// 目前不生效,写在这里是为了让数据契约先固定下来。
	MaxCPUTime time.Duration
	MaxMemory  int64
}

// Validate 校验配额自身的一致性。
func (q Quota) Validate() error {
	if q.MaxCallsPerMinute < 0 {
		return fmt.Errorf("quota: MaxCallsPerMinute 不能为负: %d", q.MaxCallsPerMinute)
	}
	if q.MaxConcurrent < 0 {
		return fmt.Errorf("quota: MaxConcurrent 不能为负: %d", q.MaxConcurrent)
	}
	return nil
}

// Error 是配额拒绝错误,带错误码、是否可重试与建议的退避时间。
type Error struct {
	Code       string
	Key        string
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s(%s)", e.Code, e.Message, e.Key)
}

// Is 判断错误链上的配额错误码。
func Is(err error, code string) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == code
	}
	return false
}

// Governor 是全部 key 的配额表。
type Governor struct {
	clock sched.Clock

	mu   sync.Mutex
	keys map[string]*bucket
}

type bucket struct {
	quota       Quota
	windowStart time.Time
	calls       int
	inflight    int
}

// Stats 是某个 key 的当前计数(诊断用)。
type Stats struct {
	CallsInWindow int
	InFlight      int
	WindowEnd     time.Time
}

// NewGovernor 创建配额表。clock 为 nil 时使用真实时钟。
func NewGovernor(clock sched.Clock) *Governor {
	if clock == nil {
		clock = sched.RealClock{}
	}
	return &Governor{clock: clock, keys: make(map[string]*bucket)}
}

// Set 设置某个 key 的配额;重复设置会重置该 key 的窗口与计数。
func (g *Governor) Set(key string, q Quota) error {
	if key == "" {
		return fmt.Errorf("quota: key 不能为空")
	}
	if err := q.Validate(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.keys[key] = &bucket{quota: q, windowStart: g.clock.Now()}
	return nil
}

// Acquire 申请一次调用许可。返回的 release 必须调用一次(幂等)。
//
// 未配置配额的 key 一律放行:配额是「按需收紧」的策略,不是默认拒绝。
func (g *Governor) Acquire(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	b := g.keys[key]
	if b == nil {
		return func() {}, nil
	}
	now := g.clock.Now()
	if now.Sub(b.windowStart) >= window {
		b.windowStart = now
		b.calls = 0
	}
	if b.quota.MaxCallsPerMinute > 0 && b.calls >= b.quota.MaxCallsPerMinute {
		retry := b.windowStart.Add(window).Sub(now)
		if retry < 0 {
			retry = 0
		}
		return nil, &Error{
			Code: CodeQuota, Key: key, Retryable: true, RetryAfter: retry,
			Message: fmt.Sprintf("每分钟调用上限 %d 已用尽", b.quota.MaxCallsPerMinute),
		}
	}
	if b.quota.MaxConcurrent > 0 && b.inflight >= b.quota.MaxConcurrent {
		return nil, &Error{
			Code: CodeBusy, Key: key, Retryable: true,
			Message: fmt.Sprintf("并发上限 %d 已用尽", b.quota.MaxConcurrent),
		}
	}
	b.calls++
	b.inflight++

	var once sync.Once
	release := func() {
		once.Do(func() {
			g.mu.Lock()
			if b.inflight > 0 {
				b.inflight--
			}
			g.mu.Unlock()
		})
	}
	return release, nil
}

// Snapshot 返回全部 key 的当前计数(诊断用;不影响配额状态)。
func (g *Governor) Snapshot() map[string]Stats {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]Stats, len(g.keys))
	for key, b := range g.keys {
		out[key] = Stats{
			CallsInWindow: b.calls,
			InFlight:      b.inflight,
			WindowEnd:     b.windowStart.Add(window),
		}
	}
	return out
}
