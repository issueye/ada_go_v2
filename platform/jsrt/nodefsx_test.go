package jsrt_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/issueye/ada_go_v2/platform/jsrt"
)

// newFSExtRuntime 装配一个装了 node:fs 的运行时;readOnly 决定写权限。
func newFSExtRuntime(t *testing.T, files map[string]string, readOnly bool) (*jsrt.Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	rt := newRuntime(t, files, jsrt.Options{Node: &jsrt.NodeOptions{
		FS: &jsrt.NodeFSOptions{Root: dir, Mount: "/workspace", Cwd: "/workspace", Write: !readOnly},
	}})
	return rt, dir
}

// 1. 补齐层三种形态都能用:sync / callback / promise,并且真的落到磁盘。
func TestNodeFSExtThreeForms(t *testing.T) {
	rt, dir := newFSExtRuntime(t, map[string]string{
		"plugin.ts": `
import fs from "node:fs"
import { mkdtemp as mkdtempAsync } from "node:fs/promises"

export async function run(): Promise<any> {
  const syncDir = fs.mkdtempSync("/workspace/s-")
  const promiseDir = await mkdtempAsync("/workspace/a-")
  const callbackDir = await new Promise<string>((resolve, reject) => {
    fs.mkdtemp("/workspace/c-", (err: any, p: string) => (err ? reject(err) : resolve(p)))
  })
  fs.writeFileSync("/workspace/t.txt", "0123456789")
  fs.truncateSync("/workspace/t.txt", 4)
  await fs.promises.truncate("/workspace/t.txt", 4)
  await mkdtempAsync("/workspace/a2-")
  const afterTruncate = fs.readFileSync("/workspace/t.txt", "utf8")
  fs.utimesSync("/workspace/t.txt", 1000, 2000)
  fs.chmodSync("/workspace/t.txt", 0o600)
  fs.symlinkSync("t.txt", "/workspace/link.txt")
  fs.linkSync("/workspace/t.txt", "/workspace/hard.txt")
  return { syncDir, promiseDir, callbackDir, afterTruncate }
}`,
	}, false)

	got, err := rt.Call(context.Background(), "run")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	obj, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("返回值应为对象,实际 %T(%v)", got, got)
	}
	for _, key := range []string{"syncDir", "promiseDir", "callbackDir"} {
		p, _ := obj[key].(string)
		if !strings.HasPrefix(p, "/workspace/") {
			t.Fatalf("%s 应返回虚拟路径,实际 %q", key, p)
		}
		if st, err := os.Stat(filepath.Join(dir, strings.TrimPrefix(p, "/workspace/"))); err != nil || !st.IsDir() {
			t.Fatalf("%s 对应的目录没有建出来: %v", key, err)
		}
	}
	if s, _ := obj["afterTruncate"].(string); s != "0123" {
		t.Fatalf("truncate 后应为 0123,实际 %q", s)
	}
	if st, err := os.Stat(filepath.Join(dir, "t.txt")); err != nil {
		t.Fatalf("文件不存在: %v", err)
	} else if st.ModTime().Unix() != 2000 {
		t.Fatalf("utimes 没有生效: mtime=%d", st.ModTime().Unix())
	}
	if target, err := os.Readlink(filepath.Join(dir, "link.txt")); err != nil || target != "t.txt" {
		t.Fatalf("symlink 不对: %q %v", target, err)
	}
	if st, err := os.Stat(filepath.Join(dir, "hard.txt")); err != nil || st.Size() != 4 {
		t.Fatalf("硬链接不对: %v", err)
	}
}

// 2. 错误码:越界、只读、不存在,都给 Node 风格的 code。
func TestNodeFSExtErrorCodes(t *testing.T) {
	const src = `
import fs from "node:fs"

export function probe(): any {
  const out: any = {}
  try { fs.mkdtempSync("/etc/tmp-") } catch (e: any) { out.escape = e.code }
  try { fs.mkdtempSync("/workspace/../out-") } catch (e: any) { out.dotdot = e.code }
  try { fs.mkdtempSync("/workspace/missing/sub-") } catch (e: any) { out.missing = e.code }
  try { fs.chmodSync("/workspace/nope.txt", 384) } catch (e: any) { out.noent = e.code }
  try { fs.symlinkSync("/etc/passwd", "/workspace/abs.txt") } catch (e: any) { out.absolute = e.code }
  return out
}`

	rt, _ := newFSExtRuntime(t, map[string]string{"plugin.ts": src}, false)
	got, err := rt.Call(context.Background(), "probe")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	obj, _ := got.(map[string]any)
	want := map[string]string{
		"escape":   "EPERM",
		"dotdot":   "EPERM",
		"missing":  "ENOENT",
		"noent":    "ENOENT",
		"absolute": "EPERM",
	}
	for key, code := range want {
		if got, _ := obj[key].(string); got != code {
			t.Fatalf("%s 期望 %s,实际 %q(全部结果 %v)", key, code, got, obj)
		}
	}
}

// 3. 只读工作区:补齐层的写操作一律 EROFS。
func TestNodeFSExtReadOnly(t *testing.T) {
	rt, _ := newFSExtRuntime(t, map[string]string{
		"plugin.ts": `
import fs from "node:fs"
export function probe(): string {
  try { fs.mkdtempSync("/workspace/x-") } catch (e: any) { return e.code }
  return "no-error"
}`,
	}, true)

	got, err := rt.Call(context.Background(), "probe")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if s, _ := got.(string); s != "EROFS" {
		t.Fatalf("只读工作区应返回 EROFS,实际 %q", got)
	}
}

// 4. 没有工作区根时 node:fs 仍然不可用(补齐层不能绕开这条边界)。
func TestNodeFSExtNeedsRoot(t *testing.T) {
	art := newArtifact(t, map[string]string{
		"plugin.ts": `import fs from "node:fs"
export function size(): number { return fs.readdirSync("/workspace").length }`,
	})
	if _, err := jsrt.New(context.Background(), jsrt.Options{Artifact: art}); err == nil {
		t.Fatalf("没有工作区根时 node:fs 应当装配失败")
	}
}
