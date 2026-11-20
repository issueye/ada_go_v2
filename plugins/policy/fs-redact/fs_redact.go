// Package fsredact 是 fs 的脱敏策略装饰器:读结果按正则替换敏感信息。
//
// 它只观察/改写读结果,不做门禁(门禁在 caps-gate/sandbox)。放在 priority=150:
// 在 sandbox(100)之外、caps-gate(200)之内。
package fsredact

import (
	"context"
	"fmt"
	"regexp"

	"github.com/issueye/ada_go_v2/platform/kernel"
	seamfs "github.com/issueye/ada_go_v2/plugins/seam/fs"
)

// PolicyName 是插件 id。
const PolicyName = "fs-redact"

// Priority 是装饰器优先级。
const Priority = 150

// DefaultReplacement 是默认替换文本。
const DefaultReplacement = "[redacted]"

// defaultPatterns 是默认的常见密钥模式。
var defaultPatterns = []string{
	`gh[pousr]_[A-Za-z0-9]{20,}`, // GitHub token
	`AKIA[0-9A-Z]{16}`,           // AWS access key
	`sk-[A-Za-z0-9]{20,}`,        // OpenAI key
}

// New 构造脱敏装饰器;patterns 为空时用默认模式。
func New(inner seamfs.FS, patterns []string, replacement string) (seamfs.FS, error) {
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
			return nil, fmt.Errorf("fs-redact: 非法正则 %q: %w", p, err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

type redactor struct {
	inner       seamfs.FS
	patterns    []*regexp.Regexp
	replacement string
}

func (r *redactor) redact(s string) string {
	for _, re := range r.patterns {
		s = re.ReplaceAllString(s, r.replacement)
	}
	return s
}

func (r *redactor) Read(ctx context.Context, req seamfs.ReadRequest) (seamfs.ReadResult, error) {
	res, err := r.inner.Read(ctx, req)
	if err != nil {
		return res, err
	}
	res.Content = r.redact(res.Content)
	return res, nil
}

func (r *redactor) Stat(ctx context.Context, p seamfs.Path) (seamfs.FileInfo, error) {
	return r.inner.Stat(ctx, p)
}
func (r *redactor) List(ctx context.Context, req seamfs.ListRequest) ([]seamfs.DirEntry, error) {
	return r.inner.List(ctx, req)
}
func (r *redactor) Write(ctx context.Context, req seamfs.WriteRequest) (seamfs.WriteResult, error) {
	return r.inner.Write(ctx, req)
}
func (r *redactor) Edit(ctx context.Context, req seamfs.EditRequest) (seamfs.EditResult, error) {
	return r.inner.Edit(ctx, req)
}
func (r *redactor) Search(ctx context.Context, req seamfs.SearchRequest) (seamfs.SearchResult, error) {
	return r.inner.Search(ctx, req)
}

type config struct {
	Patterns    []string `json:"patterns,omitempty"`
	Replacement string   `json:"replacement,omitempty"`
}

// Plugin 返回 fs-redact 插件:inject fs seam,注册脱敏装饰器。
func Plugin() kernel.Plugin {
	return kernel.Func(PolicyName, func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamfs.Chain](ctx, "fs")
		if err != nil {
			return nil, err
		}
		var conf config
		if err := cfg.Decode(&conf); err != nil {
			return nil, kernel.Fail(kernel.CodeInvalid, "fs-redact: 解析配置失败: %v", err)
		}
		// 配置期先编译,避免把错误藏到第一次读。
		compiled, err := compilePatterns(conf.Patterns)
		if err != nil {
			return nil, kernel.Fail(kernel.CodeInvalid, "fs-redact: %v", err)
		}
		replacement := conf.Replacement
		if replacement == "" {
			replacement = DefaultReplacement
		}
		return chain.Decorate(func(inner seamfs.FS) seamfs.FS {
			return &redactor{inner: inner, patterns: compiled, replacement: replacement}
		}, Priority)
	}).WithInject("fs")
}
