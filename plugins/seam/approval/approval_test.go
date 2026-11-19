package approval_test

import (
	"context"
	"testing"
	"time"

	seamapproval "github.com/adag/ada-go/v2/plugins/seam/approval"
	"github.com/adag/ada-go/v2/plugins/seam/seamerr"
)

type deny struct{}

func (deny) Name() string { return "deny" }
func (deny) Approve(context.Context, seamapproval.Request) (seamapproval.Decision, error) {
	return seamapproval.Decision{Allow: false, Reason: "拒绝", Approver: "deny"}, nil
}

type slow struct{}

func (slow) Name() string { return "slow" }
func (slow) Approve(ctx context.Context, _ seamapproval.Request) (seamapproval.Decision, error) {
	<-ctx.Done()
	return seamapproval.Decision{}, ctx.Err()
}

// 1. 注册表:默认选择确定、重复注册拒绝、没有审批人报错。
func TestRegistry(t *testing.T) {
	registry := seamapproval.NewRegistry()
	if _, err := registry.Get(""); err == nil {
		t.Fatalf("空注册表应报错")
	}
	if _, err := registry.Register(deny{}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if got := registry.Names(); len(got) != 1 || got[0] != "deny" {
		t.Fatalf("Names 不正确: %v", got)
	}
	if _, err := registry.Register(deny{}); err == nil {
		t.Fatalf("重复注册应失败")
	}
	if _, err := registry.Get(""); err != nil {
		t.Fatalf("默认选择失败: %v", err)
	}
}

// 2. Ask:拒绝结果原样返回;没有服务时 fail-closed。
func TestAskDenied(t *testing.T) {
	registry := seamapproval.NewRegistry()
	_, _ = registry.Register(deny{})
	dec, err := seamapproval.Ask(context.Background(), registry, "", seamapproval.Request{Source: "fs"})
	if err != nil {
		t.Fatalf("审批拒绝不应报错: %v", err)
	}
	if dec.Allow || dec.Reason == "" {
		t.Fatalf("拒绝结果不正确: %+v", dec)
	}

	if _, err := seamapproval.Ask(context.Background(), nil, "", seamapproval.Request{}); err == nil {
		t.Fatalf("没有 approval 服务时应报错")
	}
}

// 3. Ask 强制超时:挂起的审批人不能阻塞调用方,超时按拒绝处理。
func TestAskTimeout(t *testing.T) {
	registry := seamapproval.NewRegistry()
	_, _ = registry.Register(slow{})
	start := time.Now()
	dec, err := seamapproval.Ask(context.Background(), registry, "", seamapproval.Request{Timeout: 50 * time.Millisecond})
	if err == nil || !seamerr.Is(err, seamerr.ErrTimeout) {
		t.Fatalf("超时应返回 ETIMEOUT,实际 %v", err)
	}
	if dec.Allow {
		t.Fatalf("超时后不应放行")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("超时没有及时返回: %s", elapsed)
	}
}
