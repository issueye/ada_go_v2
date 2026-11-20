package fslocal_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fslocal "github.com/issueye/ada_go_v2/plugins/provider/fs-local"
	seamfs "github.com/issueye/ada_go_v2/plugins/seam/fs"
	"github.com/issueye/ada_go_v2/plugins/seam/seamerr"
)

func newFS(t *testing.T) (*fslocal.FS, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := fslocal.New(fslocal.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("创建 fs-local 失败: %v", err)
	}
	return f, dir
}

func ws(p string) seamfs.Path { return seamfs.Path("/workspace" + p) }

func errCode(err error) seamerr.Code {
	var e *seamerr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// 1. 写→读→编辑:版本守卫、read-before-edit。
func TestWriteReadEdit(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()

	w, err := f.Write(ctx, seamfs.WriteRequest{Path: ws("/a.txt"), Content: []byte("hello"), CreateDirs: true})
	if err != nil {
		t.Fatalf("写失败: %v", err)
	}
	if w.Version == "" {
		t.Fatalf("写应返回版本")
	}
	r, err := f.Read(ctx, seamfs.ReadRequest{Path: ws("/a.txt")})
	if err != nil {
		t.Fatalf("读失败: %v", err)
	}
	if r.Content != "hello" || r.Version != w.Version || r.Kind != seamfs.KindText {
		t.Fatalf("读结果不正确: %+v", r)
	}

	e, err := f.Edit(ctx, seamfs.EditRequest{Path: ws("/a.txt"), IfVersion: r.Version, Old: "hello", New: "world"})
	if err != nil {
		t.Fatalf("编辑失败: %v", err)
	}
	if e.Replacements != 1 {
		t.Fatalf("应替换 1 处,实际 %d", e.Replacements)
	}
	if got, _ := f.Read(ctx, seamfs.ReadRequest{Path: ws("/a.txt")}); got.Content != "world" {
		t.Fatalf("编辑后内容不正确: %q", got.Content)
	}

	// 过期版本 → ECONFLICT,且不落盘。
	if _, err := f.Edit(ctx, seamfs.EditRequest{Path: ws("/a.txt"), IfVersion: r.Version, Old: "world", New: "x"}); err == nil || errCode(err) != seamfs.CodeConflict {
		t.Fatalf("过期版本编辑应 ECONFLICT,实际 %v", err)
	}
	if got, _ := f.Read(ctx, seamfs.ReadRequest{Path: ws("/a.txt")}); got.Content != "world" {
		t.Fatalf("冲突的编辑不应落盘: %q", got.Content)
	}
	// 缺少版本 → ECONFLICT(read-before-edit)。
	if _, err := f.Edit(ctx, seamfs.EditRequest{Path: ws("/a.txt"), Old: "world", New: "x"}); err == nil || errCode(err) != seamfs.CodeConflict {
		t.Fatalf("缺少 IfVersion 应 ECONFLICT,实际 %v", err)
	}
}

// 2. 越界与非法路径全部拒绝。
func TestRejectsEscape(t *testing.T) {
	f, _ := newFS(t)
	bad := []seamfs.Path{
		"/workspace/../x",
		"/workspace/../../etc/passwd",
		"/user/x",
		"/plugin/x/a",
		"workspace/a",
		seamfs.Path("/workspace/a\x00b"),
	}
	for _, p := range bad {
		if _, err := f.Read(context.Background(), seamfs.ReadRequest{Path: p}); err == nil {
			t.Fatalf("非法路径应被拒绝: %q", p)
		}
	}
	// 反斜杠混用同样拒绝。
	if _, err := f.Write(context.Background(), seamfs.WriteRequest{Path: seamfs.Path(`/workspace\a.txt`), Content: []byte("x")}); err == nil {
		t.Fatalf("反斜杠路径应被拒绝")
	}
}

// 3. 有界读/写与二进制。
func TestBoundsAndBinary(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()

	huge := make([]byte, seamfs.DefaultWriteLimit+1)
	if _, err := f.Write(ctx, seamfs.WriteRequest{Path: ws("/huge.txt"), Content: huge, CreateDirs: true}); err == nil || errCode(err) != seamfs.CodeTooLarge {
		t.Fatalf("超限写应 ETOOLARGE,实际 %v", err)
	}

	if _, err := f.Write(ctx, seamfs.WriteRequest{Path: ws("/text.txt"), Content: []byte("hello world"), CreateDirs: true}); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	r, err := f.Read(ctx, seamfs.ReadRequest{Path: ws("/text.txt"), Limit: 5})
	if err != nil {
		t.Fatalf("读失败: %v", err)
	}
	if !r.Truncated || len(r.Content) != 5 || r.Content != "hello" {
		t.Fatalf("有界读结果不正确: %+v", r)
	}

	bin := []byte{0x00, 0x01, 0x02}
	if _, err := f.Write(ctx, seamfs.WriteRequest{Path: ws("/bin.bin"), Content: bin, CreateDirs: true}); err != nil {
		t.Fatalf("写二进制失败: %v", err)
	}
	if _, err := f.Read(ctx, seamfs.ReadRequest{Path: ws("/bin.bin"), Format: seamfs.FormatText}); err == nil || errCode(err) != seamfs.CodeBinary {
		t.Fatalf("二进制按文本读应 EBINARY,实际 %v", err)
	}
	br, err := f.Read(ctx, seamfs.ReadRequest{Path: ws("/bin.bin"), Format: seamfs.FormatBytes})
	if err != nil || br.Kind != seamfs.KindBinary {
		t.Fatalf("二进制按 bytes 读结果不正确: %v %+v", err, br)
	}
}

// 4. 列目录:非递归/递归/上限。
func TestList(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	for _, p := range []string{"/a.txt", "/sub/b.txt", "/sub/deep/c.txt"} {
		if _, err := f.Write(ctx, seamfs.WriteRequest{Path: ws(p), Content: []byte("x"), CreateDirs: true}); err != nil {
			t.Fatalf("写 %s 失败: %v", p, err)
		}
	}
	top, err := f.List(ctx, seamfs.ListRequest{Path: ws("/")})
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	if len(top) != 2 {
		t.Fatalf("非递归应返回 2 条(a.txt + sub),实际 %d: %+v", len(top), top)
	}
	recursive, err := f.List(ctx, seamfs.ListRequest{Path: ws("/"), Recursive: true})
	if err != nil {
		t.Fatalf("递归列目录失败: %v", err)
	}
	if len(recursive) != 5 {
		t.Fatalf("递归应返回 5 条(含 2 个目录),实际 %d: %+v", len(recursive), recursive)
	}
	if _, err := f.List(ctx, seamfs.ListRequest{Path: ws("/"), Recursive: true, Limit: 1}); err == nil || errCode(err) != seamfs.CodeTooLarge {
		t.Fatalf("超过 Limit 应 ETOOLARGE,实际 %v", err)
	}
}

// 5. 符号链接指向根外:拒绝(平台不支持创建链接时跳过)。
func TestSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatalf("写外部文件失败: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "link")); err != nil {
		t.Skipf("平台无法创建符号链接: %v", err)
	}
	f, err := fslocal.New(fslocal.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("创建 fs-local 失败: %v", err)
	}
	if _, err := f.Read(context.Background(), seamfs.ReadRequest{Path: ws("/link")}); err == nil {
		t.Fatalf("指向根外的符号链接应被拒绝")
	}
}

// 5.1 新文件经由根外父目录符号链接写入:必须拒绝(不能只检查已存在目标)。
func TestWriteThroughSymlinkParent(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("平台无法创建符号链接: %v", err)
	}
	f, err := fslocal.New(fslocal.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("创建 fs-local 失败: %v", err)
	}
	if _, err := f.Write(context.Background(), seamfs.WriteRequest{
		Path:       ws("/link/escaped.txt"),
		Content:    []byte("x"),
		CreateDirs: true,
	}); err == nil {
		t.Fatalf("父目录符号链接指向根外时应拒绝")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); err == nil {
		t.Fatalf("根外文件不应被创建")
	}
}

// 6. 换行与编码:CRLF 原样返回,不做换行改写。
func TestPreservesCRLF(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	content := []byte("a\r\nb\r\n")
	if _, err := f.Write(ctx, seamfs.WriteRequest{Path: ws("/crlf.txt"), Content: content, CreateDirs: true}); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	r, err := f.Read(ctx, seamfs.ReadRequest{Path: ws("/crlf.txt")})
	if err != nil {
		t.Fatalf("读失败: %v", err)
	}
	if !strings.Contains(r.Content, "\r\n") {
		t.Fatalf("读结果不应改写换行: %q", r.Content)
	}
}
