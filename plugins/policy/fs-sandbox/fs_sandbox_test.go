package fssandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/adag/ada-go/v2/kernel"
	fssandbox "github.com/adag/ada-go/v2/plugins/policy/fs-sandbox"
	fslocal "github.com/adag/ada-go/v2/plugins/provider/fs-local"
	seamfs "github.com/adag/ada-go/v2/plugins/seam/fs"
)

// fakeFS 记录调用,不碰真实磁盘。
type fakeFS struct {
	writes []seamfs.Path
	reads  []seamfs.Path
}

func (f *fakeFS) Stat(context.Context, seamfs.Path) (seamfs.FileInfo, error) {
	return seamfs.FileInfo{}, nil
}
func (f *fakeFS) List(context.Context, seamfs.ListRequest) ([]seamfs.DirEntry, error) {
	return nil, nil
}
func (f *fakeFS) Read(_ context.Context, req seamfs.ReadRequest) (seamfs.ReadResult, error) {
	f.reads = append(f.reads, req.Path)
	return seamfs.ReadResult{Content: "ok"}, nil
}
func (f *fakeFS) Write(_ context.Context, req seamfs.WriteRequest) (seamfs.WriteResult, error) {
	f.writes = append(f.writes, req.Path)
	return seamfs.WriteResult{Version: "v1"}, nil
}
func (f *fakeFS) Edit(_ context.Context, req seamfs.EditRequest) (seamfs.EditResult, error) {
	f.writes = append(f.writes, req.Path)
	return seamfs.EditResult{Replacements: 1, Version: "v1"}, nil
}
func (f *fakeFS) Search(context.Context, seamfs.SearchRequest) (seamfs.SearchResult, error) {
	return seamfs.SearchResult{}, nil
}

// 1. 三模式 × 写路径的判定矩阵;读在任何模式都放行。
func TestModeMatrix(t *testing.T) {
	fake := &fakeFS{}
	cases := []struct {
		mode    fssandbox.Mode
		path    seamfs.Path
		allowed bool
	}{
		{fssandbox.ReadOnly, "/workspace/a.txt", false},
		{fssandbox.WorkspaceWrite, "/workspace/a.txt", true},
		{fssandbox.WorkspaceWrite, "/tmp/a.txt", true},
		{fssandbox.WorkspaceWrite, "/plugin/x/a.txt", false},
		{fssandbox.Danger, "/workspace/a.txt", true},
		{fssandbox.Danger, "/plugin/x/a.txt", false},
	}
	for _, tc := range cases {
		fsys := fssandbox.New(fake, tc.mode)
		_, err := fsys.Write(context.Background(), seamfs.WriteRequest{Path: tc.path})
		if tc.allowed && err != nil {
			t.Fatalf("%s 写 %s 应允许,实际 %v", tc.mode, tc.path, err)
		}
		if !tc.allowed && err == nil {
			t.Fatalf("%s 写 %s 应拒绝", tc.mode, tc.path)
		}
	}
	fsys := fssandbox.New(fake, fssandbox.ReadOnly)
	if _, err := fsys.Read(context.Background(), seamfs.ReadRequest{Path: "/workspace/a.txt"}); err != nil {
		t.Fatalf("只读模式读应放行: %v", err)
	}
}

// 2. 拒绝时底层实现不会被调用。
func TestDeniedDoesNotReachInner(t *testing.T) {
	fake := &fakeFS{}
	fsys := fssandbox.New(fake, fssandbox.ReadOnly)
	if _, err := fsys.Write(context.Background(), seamfs.WriteRequest{Path: "/workspace/x"}); err == nil {
		t.Fatalf("应拒绝")
	}
	if len(fake.writes) != 0 {
		t.Fatalf("被拒绝的写不应到达底层,实际 %v", fake.writes)
	}
}

// 3. 与内核装配:seam + provider + policy,消费者拿到的是带沙箱的 FS。
func TestKernelIntegration(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	if _, err := app.Plugin(seamfs.Plugin(), kernel.NewConfig(nil)); err != nil {
		t.Fatalf("挂载 seam 失败: %v", err)
	}
	if _, err := app.Plugin(fslocal.Plugin(), kernel.NewConfig(map[string]any{"workspace": dir})); err != nil {
		t.Fatalf("挂载 provider 失败: %v", err)
	}
	if _, err := app.Plugin(fssandbox.Plugin(), kernel.NewConfig(map[string]any{"mode": string(fssandbox.ReadOnly)})); err != nil {
		t.Fatalf("挂载 policy 失败: %v", err)
	}

	var (
		readContent string
		writeErr    error
	)
	consumer := kernel.Func("consumer", func(ctx *kernel.Context, cfg kernel.Config) (kernel.Dispose, error) {
		chain, err := kernel.Service[*seamfs.Chain](ctx, "fs")
		if err != nil {
			return nil, err
		}
		fsys, err := chain.Get()
		if err != nil {
			return nil, err
		}
		r, err := fsys.Read(context.Background(), seamfs.ReadRequest{Path: "/workspace/a.txt"})
		if err != nil {
			return nil, err
		}
		readContent = r.Content
		_, writeErr = fsys.Write(context.Background(), seamfs.WriteRequest{Path: "/workspace/new.txt", Content: []byte("x")})
		return nil, nil
	}).WithInject("fs")
	if _, err := app.Plugin(consumer, kernel.NewConfig(nil)); err != nil {
		t.Fatalf("挂载消费者失败: %v", err)
	}

	if readContent != "hello" {
		t.Fatalf("读内容不正确: %q", readContent)
	}
	if writeErr == nil {
		t.Fatalf("只读模式下写应被拒绝")
	}
}

// 4. 未安装 provider 时,Get 必须返回 EUNIMPLEMENTED,而不是 panic。
func TestKernelWithoutProvider(t *testing.T) {
	app := kernel.New(kernel.Options{})
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if _, err := app.Plugin(seamfs.Plugin(), kernel.NewConfig(nil)); err != nil {
		t.Fatalf("挂载 seam 失败: %v", err)
	}
	chain, err := kernel.Service[*seamfs.Chain](app.Root(), "fs")
	if err != nil {
		t.Fatalf("取服务失败: %v", err)
	}
	if _, err := chain.Get(); err == nil {
		t.Fatalf("没有 provider 时 Get 应失败")
	}
}
