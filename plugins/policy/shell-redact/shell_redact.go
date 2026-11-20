// Package shellredact 是 shell 的脱敏策略:执行结果与流式块按正则替换敏感信息。
//
// priority=150:在 shell-observe(250)之内、sandbox(100)之外。
// 注意:流式输出按块脱敏,若密钥被拆到两个块里仍可能漏(见 docs/seam-shell.md)。
package shellredact

import (
	"context"
	"fmt"
	"regexp"

	"github.com/adag/ada-go/v2/platform/kernel"
	seamshell "github.com/adag/ada-go/v2/plugins/seam/shell"
)

// PolicyName 是插件 id。
const PolicyName = "shell-redact"

// Priority 是装饰器优先级。
const Priority = 150

// DefaultReplacement 是默认替换文本。
const DefaultReplacement = "[redacted]"

// defaultPatterns 与 fs-redact 有意保持同一组常见密钥模式(两包不互相 import)。
var defaultPatterns = []string{
	`gh[pousr]_[A-Za-z0-9]{20,}`,
	`AKIA[0-9A-Z]{16}`,
	`sk-[A-Za-z0-9]{20,}`,
}

// New 构造脱敏装饰器;patterns 为空时用默认模式。
func New(inner seamshell.Shell, patterns []string, replacement string) (seamshell.Shell, error) {
	compiled, err := compilePatterns(patterns)
	if err != nil {
		return nil, err
	}
	if replacement == "" {
		replacement = DefaultReplacement
	}
	return &redactor{inner: inner, patterns: compiled, replacement: replacement}, nil
}

func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	list := patterns
	if len(list) == 0 {
		list = defaultPatterns
	}
	compiled := make([]*regexp.Regexp, 0, len(list))
	for _, p := range list {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("shell-redact: 非法正则 %q: %w", p, err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

type redactor struct {
	inner       seamshell.Shell
	patterns    []*regexp.Regexp
	replacement string
}

func (r *redactor) redact(s string) string {
	for _, re := range r.patterns {
		s = re.ReplaceAllString(s, r.replacement)
	}
	return s
}

func (r *redactor) redactResult(res seamshell.Result) seamshell.Result {
	res.Stdout = []byte(r.redact(string(res.Stdout)))
	res.Stderr = []byte(r.redact(string(res.Stderr)))
	return res
}

func (r *redactor) Run(ctx context.Context, cmd seamshell.Command) (seamshell.Result, error) {
	res, err := r.inner.Run(ctx, cmd)
	return r.redactResult(res), err
}

func (r *redactor) Stream(ctx context.Context, cmd seamshell.Command, onChunk func(seamshell.Chunk) error) (seamshell.Result, error) {
	wrapped := func(chunk seamshell.Chunk) error {
		chunk.Text = r.redact(chunk.Text)
		return onChunk(chunk)
	}
	res, err := r.inner.Stream(ctx, cmd, wrapped)
	return r.redactResult(res), err
}

func (r *redactor) Env() seamshell.Env { return r.inner.Env() }

type config struct {
	Patterns    []string `json:"patterns,omitempty"`
	Replacement string   `json:"replacement,omitempty"`
}

// Plugin 返回 shell-redact 插件:inject shell seam,注册脱敏装饰器。
func Plugin() kernel.Plugin {
	return kernel.Func(PolicyName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamshell.Chain](ctx, "shell")
		if err != nil {
			return nil, err
		}
		var conf config
		if err := cfg.Decode(&conf); err != nil {
			return nil, kernel.Fail(kernel.CodeInvalid, "shell-redact: 解析配置失败: %v", err)
		}
		compiled, err := compilePatterns(conf.Patterns)
		if err != nil {
			return nil, kernel.Fail(kernel.CodeInvalid, "shell-redact: %v", err)
		}
		replacement := conf.Replacement
		if replacement == "" {
			replacement = DefaultReplacement
		}
		return chain.Decorate(func(inner seamshell.Shell) seamshell.Shell {
			return &redactor{inner: inner, patterns: compiled, replacement: replacement}
		}, Priority)
	}).WithInject("shell")
}
