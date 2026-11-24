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
		Buffer: true,
		FS:     &jsrt.NodeFSOptions{Root: dir, Mount: "/workspace", Cwd: "/workspace", Write: !readOnly},
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

// 5. 文件描述符族:open/write/fsync/fstat/read/close,关闭后 EBADF。
func TestNodeFSFileDescriptors(t *testing.T) {
	rt, dir := newFSExtRuntime(t, map[string]string{
		"plugin.ts": `
import fs from "node:fs"

export function roundtrip(): any {
  const fd = fs.openSync("/workspace/fd.txt", "w+")
  const written = fs.writeSync(fd, "hello-fd")
  fs.fsyncSync(fd)
  const st = fs.fstatSync(fd)
  const buf = Buffer.alloc(8)
  const bytesRead = fs.readSync(fd, buf, 0, 8, 0)
  fs.closeSync(fd)
  let bad = "no-error"
  try {
    fs.readSync(fd, buf, 0, 1, 0)
  } catch (e: any) {
    bad = e.code
  }
  return { written, size: st.size, bytesRead, text: buf.toString("utf8", 0, bytesRead), bad }
}`,
	}, false)

	got, err := rt.Call(context.Background(), "roundtrip")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	obj, _ := got.(map[string]any)
	if n := asInt(t, obj["written"]); n != 8 {
		t.Fatalf("writeSync 应返回 8,实际 %v", n)
	}
	if n := asInt(t, obj["size"]); n != 8 {
		t.Fatalf("fstat.size 应为 8,实际 %v", n)
	}
	if s, _ := obj["text"].(string); s != "hello-fd" {
		t.Fatalf("读回内容不对: %q", s)
	}
	if s, _ := obj["bad"].(string); s != "EBADF" {
		t.Fatalf("关闭后的读应是 EBADF,实际 %q", s)
	}
	if _, err := os.Stat(filepath.Join(dir, "fd.txt")); err != nil {
		t.Fatalf("文件没有落盘: %v", err)
	}
}

// 6. createWriteStream / createReadStream:写完整、读回同样内容,且能按块迭代。
func TestNodeFSFileStreams(t *testing.T) {
	rt, _ := newFSExtRuntime(t, map[string]string{
		"plugin.ts": `
import fs from "node:fs"

function collect(stream: any): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    const parts: string[] = []
    stream.on("data", (c: any) => parts.push(String(c)))
    stream.on("end", () => resolve(parts.join("")))
    stream.on("error", (e: any) => reject(e))
  })
}

export async function run(): Promise<any> {
  await new Promise<void>((resolve, reject) => {
    const w = fs.createWriteStream("/workspace/stream.txt")
    w.on("finish", () => resolve())
    w.on("error", (e: any) => reject(e))
    w.write("hello-")
    w.end("streams")
  })
  const text = await collect(fs.createReadStream("/workspace/stream.txt", { highWaterMark: 4 }))
  const chunks: string[] = []
  for await (const chunk of fs.createReadStream("/workspace/stream.txt", { highWaterMark: 4 })) {
    chunks.push(String(chunk))
  }
  return { text, chunks: chunks.length, joined: chunks.join("") }
}`,
	}, false)

	got, err := rt.Call(context.Background(), "run")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	obj, _ := got.(map[string]any)
	if s, _ := obj["text"].(string); s != "hello-streams" {
		t.Fatalf("createWriteStream/createReadStream 内容不对: %q", s)
	}
	if n := asInt(t, obj["chunks"]); n < 2 {
		t.Fatalf("按 4 字节分块应至少 2 块,实际 %v", n)
	}
	if s, _ := obj["joined"].(string); s != "hello-streams" {
		t.Fatalf("分块拼接结果不对: %q", s)
	}
}

// 7. fd 族的 Promise 形态(fs.promises),以及只读工作区的 EROFS。
func TestNodeFSPromiseFDAndReadOnly(t *testing.T) {
	rt, _ := newFSExtRuntime(t, map[string]string{
		"plugin.ts": `
import { open, write, read, close, fstat } from "node:fs/promises"

export async function run(): Promise<any> {
  const fd = await open("/workspace/async.txt", "w+")
  await write(fd, "async-fd")
  const st = await fstat(fd)
  const buf = Buffer.alloc(8)
  const res = await read(fd, buf, 0, 8, 0)
  await close(fd)
  return { size: st.size, bytesRead: res.bytesRead, text: buf.toString("utf8", 0, res.bytesRead) }
}`,
	}, false)

	got, err := rt.Call(context.Background(), "run")
	if err != nil {
		t.Fatalf("Promise 形态调用失败: %v", err)
	}
	obj, _ := got.(map[string]any)
	if n := asInt(t, obj["size"]); n != 8 {
		t.Fatalf("fstat.size 应为 8,实际 %v", n)
	}
	if s, _ := obj["text"].(string); s != "async-fd" {
		t.Fatalf("Promise 读回内容不对: %q", s)
	}

	rtRO, _ := newFSExtRuntime(t, map[string]string{
		"plugin.ts": `
import fs from "node:fs"
export function probe(): any {
  const out: any = {}
  try { fs.openSync("/workspace/x.txt", "w") } catch (e: any) { out.write = e.code }
  try { fs.openSync("/workspace/missing.txt", "r") } catch (e: any) { out.read = e.code }
  return out
}`,
	}, true)

	got, err = rtRO.Call(context.Background(), "probe")
	if err != nil {
		t.Fatalf("只读运行时调用失败: %v", err)
	}
	obj, _ = got.(map[string]any)
	if s, _ := obj["write"].(string); s != "EROFS" {
		t.Fatalf("只读工作区 open(w) 应返回 EROFS,实际 %q", s)
	}
	if s, _ := obj["read"].(string); s != "ENOENT" {
		t.Fatalf("只读工作区 open(r) 读不存在的文件应返回 ENOENT,实际 %q", s)
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
