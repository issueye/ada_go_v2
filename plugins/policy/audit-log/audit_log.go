// Package auditlog 是审计落盘策略:把观察/审批事件追加为 JSONL。
//
// 只记录事件 payload(观察事件本身已约定不含 body/secret);不做脱敏/轮转/上报。
package auditlog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/adag/ada-go/v2/kernel"
	seamapproval "github.com/adag/ada-go/v2/plugins/seam/approval"
	seamfs "github.com/adag/ada-go/v2/plugins/seam/fs"
	seamnet "github.com/adag/ada-go/v2/plugins/seam/net"
	seamshell "github.com/adag/ada-go/v2/plugins/seam/shell"
)

// PolicyName 是插件 id。
const PolicyName = "audit-log"

// record 是 JSONL 的一行。
type record struct {
	TS      string `json:"ts"`
	Event   string `json:"event"`
	Payload any    `json:"payload,omitempty"`
}

type config struct {
	Path string `json:"path"`
}

// Plugin 返回 audit-log 插件:按配置路径追加 JSONL。
func Plugin() kernel.Plugin {
	return kernel.Func(PolicyName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		var conf config
		if err := cfg.Decode(&conf); err != nil {
			return nil, kernel.Fail(kernel.CodeInvalid, "audit-log: 解析配置失败: %v", err)
		}
		if conf.Path == "" {
			return nil, kernel.Fail(kernel.CodeInvalid, "audit-log: 必须配置 path")
		}
		f, err := os.OpenFile(conf.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("audit-log: 打开审计文件失败: %w", err)
		}

		var mu sync.Mutex
		write := func(event string, payload any) {
			mu.Lock()
			defer mu.Unlock()
			data, err := json.Marshal(record{
				TS: time.Now().UTC().Format(time.RFC3339Nano), Event: event, Payload: payload,
			})
			if err != nil {
				return
			}
			_, _ = f.Write(append(data, '\n'))
		}

		for _, name := range []string{
			seamfs.EventObserve, seamshell.EventObserve,
			seamnet.EventObserve, seamapproval.EventRequested,
		} {
			eventName := name
			ctx.On(eventName, func(_ context.Context, _ *kernel.Context, args []any) (any, error) {
				var payload any
				if len(args) > 0 {
					payload = args[0]
				}
				write(eventName, payload)
				return nil, nil
			})
		}
		return func(context.Context) error { return f.Close() }, nil
	})
}
