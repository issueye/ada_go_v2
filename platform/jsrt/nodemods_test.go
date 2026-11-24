package jsrt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/issueye/ada_go_v2/platform/jsrt"
)

// 1. node:timers 必须是全局定时器的同一组函数,并且模块路径创建的定时器同样能被 await。
func TestNodeTimersModule(t *testing.T) {
	rt := newRuntime(t, map[string]string{
		"plugin.ts": `
import * as timers from "node:timers"

export function same(): boolean {
  return timers.setTimeout === setTimeout && timers.clearTimeout === clearTimeout
}

export async function wait(v: number): Promise<number> {
  return await new Promise<number>((resolve) => { timers.setTimeout(() => resolve(v + 1), 1) })
}`,
	}, jsrt.Options{})

	got, err := rt.Call(context.Background(), "same")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if same, _ := got.(bool); !same {
		t.Fatalf("模块导出的定时器必须与全局是同一组函数")
	}

	got, err = rt.Call(context.Background(), "wait", 41)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if n := asInt(t, got); n != 42 {
		t.Fatalf("期望 42,实际 %v", got)
	}
}

// 2. node:timers/promises:setTimeout/setImmediate/可迭代的 setInterval。
func TestNodeTimersPromises(t *testing.T) {
	rt := newRuntime(t, map[string]string{
		"plugin.ts": `
import { setInterval as every, setImmediate as soon, setTimeout as delay } from "node:timers/promises"

export async function run(): Promise<any> {
  const a = await delay(1, "delay")
  const b = await soon("immediate")
  const ticks: string[] = []
  for await (const v of every(1, "tick")) {
    ticks.push(v)
    if (ticks.length >= 3) break
  }
  return { a, b, ticks: ticks.join(",") }
}`,
	}, jsrt.Options{})

	got, err := rt.Call(context.Background(), "run")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	obj, _ := got.(map[string]any)
	if s, _ := obj["a"].(string); s != "delay" {
		t.Fatalf("setTimeout 应返回 value,实际 %v", obj["a"])
	}
	if s, _ := obj["b"].(string); s != "immediate" {
		t.Fatalf("setImmediate 应返回 value,实际 %v", obj["b"])
	}
	if s, _ := obj["ticks"].(string); s != "tick,tick,tick" {
		t.Fatalf("setInterval 迭代不对: %q", s)
	}
}

// 3. node:stream:Readable.from → PassThrough → pipeline,以及 for await 消费。
func TestNodeStreamPipelineAndIteration(t *testing.T) {
	rt := newRuntime(t, map[string]string{
		"plugin.ts": `
import { PassThrough, Readable, pipeline } from "node:stream"

function collect(stream: any): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    const parts: string[] = []
    stream.on("data", (c: any) => parts.push(String(c)))
    stream.on("end", () => resolve(parts.join("")))
    stream.on("error", (e: any) => reject(e))
  })
}

export async function piped(): Promise<string> {
  const src = Readable.from(["a", "b", "c"])
  const pass = new PassThrough()
  const done = collect(pass)
  pipeline(src, pass)
  return await done
}

export async function iterated(): Promise<string> {
  const src = Readable.from(["x", "y", "z"])
  const out: string[] = []
  for await (const chunk of src) out.push(String(chunk))
  return out.join("-")
}`,
	}, jsrt.Options{})

	got, err := rt.Call(context.Background(), "piped")
	if err != nil {
		t.Fatalf("pipeline 失败: %v", err)
	}
	if s, _ := got.(string); s != "abc" {
		t.Fatalf("pipeline 期望 abc,实际 %q", s)
	}

	got, err = rt.Call(context.Background(), "iterated")
	if err != nil {
		t.Fatalf("异步迭代失败: %v", err)
	}
	if s, _ := got.(string); s != "x-y-z" {
		t.Fatalf("异步迭代期望 x-y-z,实际 %q", s)
	}
}

// 4. node:readline(+ promises):从可读流按行切分,question 返回下一行。
func TestNodeReadline(t *testing.T) {
	rt := newRuntime(t, map[string]string{
		"plugin.ts": `
import { createInterface } from "node:readline"
import { createInterface as createPromiseInterface } from "node:readline/promises"
import { Readable } from "node:stream"

export async function lines(): Promise<string> {
  const rl = createInterface({ input: Readable.from(["a\nb", "\nc\n"]) })
  const out: string[] = []
  for await (const line of rl) out.push(String(line))
  return out.join("|")
}

export async function ask(): Promise<string> {
  const rl = createPromiseInterface({ input: Readable.from(["answer\n"]) })
  const a = await rl.question("q? ")
  rl.close()
  return a
}`,
	}, jsrt.Options{})

	got, err := rt.Call(context.Background(), "lines")
	if err != nil {
		t.Fatalf("按行读取失败: %v", err)
	}
	if s, _ := got.(string); s != "a|b|c" {
		t.Fatalf("期望 a|b|c,实际 %q", s)
	}

	got, err = rt.Call(context.Background(), "ask")
	if err != nil {
		t.Fatalf("question 失败: %v", err)
	}
	if s, _ := got.(string); s != "answer" {
		t.Fatalf("question 期望 answer,实际 %q", s)
	}
}

// 5. node:perf_hooks:单调 now、mark/measure、观察者异步收到条目。
func TestNodePerfHooks(t *testing.T) {
	rt := newRuntime(t, map[string]string{
		"plugin.ts": `
import { PerformanceObserver, performance } from "node:perf_hooks"

export async function run(): Promise<any> {
  const t0 = performance.now()
  performance.mark("a")
  performance.mark("b")
  const measure = performance.measure("m", "a", "b")

  const seen: string[] = []
  const observer = new PerformanceObserver((list: any) => {
    for (const entry of list.getEntries()) seen.push(entry.name)
  })
  observer.observe({ entryTypes: ["mark"] })
  performance.mark("c")
  await new Promise<void>((resolve) => { setTimeout(() => resolve(), 1) })
  observer.disconnect()

  return {
    t0,
    measure: measure.duration,
    seen: seen.join(","),
    marks: performance.getEntriesByType("mark").length,
    origin: performance.timeOrigin,
  }
}`,
	}, jsrt.Options{})

	got, err := rt.Call(context.Background(), "run")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	obj, _ := got.(map[string]any)
	if n := asInt(t, obj["t0"]); n < 0 {
		t.Fatalf("now() 应是非负数,实际 %v", obj["t0"])
	}
	if d, _ := obj["measure"].(float64); d < 0 {
		t.Fatalf("measure 的时长不能为负: %v", obj["measure"])
	}
	if s, _ := obj["seen"].(string); !strings.Contains(s, "c") {
		t.Fatalf("观察者应收到 mark c,实际 %q", s)
	}
	if n := asInt(t, obj["marks"]); n < 3 {
		t.Fatalf("应有 3 个 mark 条目,实际 %v", n)
	}
}

// 6. node:tty:没有终端 → isatty 恒 false;流类构造即抛 ERR_NOT_SUPPORTED。
func TestNodeTty(t *testing.T) {
	rt := newRuntime(t, map[string]string{
		"plugin.ts": `
import { isatty, WriteStream } from "node:tty"

export function probe(): any {
  let code = "no-error"
  try {
    new (WriteStream as any)(1)
  } catch (e: any) {
    code = e.code
  }
  return { tty: isatty(1), code }
}`,
	}, jsrt.Options{})

	got, err := rt.Call(context.Background(), "probe")
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	obj, _ := got.(map[string]any)
	if b, _ := obj["tty"].(bool); b {
		t.Fatalf("运行时没有终端,isatty 必须为 false")
	}
	if s, _ := obj["code"].(string); s != "ERR_NOT_SUPPORTED" {
		t.Fatalf("WriteStream 构造应抛 ERR_NOT_SUPPORTED,实际 %q", s)
	}
}
