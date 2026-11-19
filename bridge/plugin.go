// Package bridge 把编译产物接成 kernel 插件:M2 的第一段桥。
//
// 这一段只做最小闭环——加载 JS、调用 apply(配置)、接收 JS 日志。
// ctx.* 的完整 JS API 与跨 goroutine 的锁是后续切片;在此之前,
// JS 侧只能调用宿主注入的 __adaLog,不允许回写内核状态。
package bridge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/adag/ada-go/v2/compile"
	"github.com/adag/ada-go/v2/jsrt"
	"github.com/adag/ada-go/v2/kernel"
	"github.com/adag/ada-go/v2/kernel/schema"
)

// applyTimeout 是装配预算。装配只做编译 + 加载 + 一次 apply,
// 不应该长时间占住内核的控制协程。
const applyTimeout = 10 * time.Second

// Options 描述一个 JS 插件。
type Options struct {
	Name     string
	Artifact *compile.Artifact
	// Logf 接收宿主判定的日志(默认是 JS 侧的 __adaLog);nil 表示丢弃。
	Logf func(format string, args ...any)
}

// Plugin 是 kernel.Plugin 的 JS 实现。
type Plugin struct {
	opts Options

	mu   sync.Mutex
	logs []string
	rt   *jsrt.Runtime
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

// Provide 声明不提供服务:JS 侧 provide 需要跨 goroutine 的锁,留给后续切片。
func (p *Plugin) Provide() []string { return nil }

// Config 返回 nil:JS 插件的配置 schema 由插件目录里的 config.schema.json 提供,
// 加载器尚未实现(M3)。
func (p *Plugin) Config() *schema.Schema { return nil }

// Apply 装配 JS 运行时并调用入口模块的 apply(cfg)。
func (p *Plugin) Apply(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
	// kernel.Apply 的签名没有 context(M1 冻结),这里用一个有界预算兜底。
	actx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	rt, err := jsrt.New(actx, jsrt.Options{Artifact: p.opts.Artifact, Logf: p.record})
	if err != nil {
		return nil, kernel.Wrap(kernel.CodeInvalid, err, "JS 插件 %s 装配失败", p.opts.Name)
	}

	res, err := rt.Call(actx, "apply", cfg.Raw())
	if err != nil {
		_ = rt.Close(context.Background())
		return nil, kernel.Wrap(kernel.CodeInvalid, err, "JS 插件 %s 的 apply 失败", p.opts.Name)
	}

	p.mu.Lock()
	p.rt = rt
	p.mu.Unlock()

	ctx.Log().Info("JS 插件已装配", "exports", rt.Exports(), "result", res)
	return func(dctx context.Context) error {
		p.mu.Lock()
		r := p.rt
		p.rt = nil
		p.mu.Unlock()
		if r == nil {
			return nil
		}
		return r.Close(dctx)
	}, nil
}

// Logs 返回 JS 侧 __adaLog 的输出(测试与诊断用)。
func (p *Plugin) Logs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.logs))
	copy(out, p.logs)
	return out
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
