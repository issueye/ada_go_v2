// 示例 TS 插件:提供服务、写日志、并在卸载时清理。
// 入口约定与 Go 插件一致:apply(ctx, config)。
export enum Tone {
  Friendly = "friendly",
}

type Ctx = {
  log: (msg: string) => void
  provide: (name: string, value: unknown) => void
  effect: (label: string, cleanup: () => void) => void
}

type Config = { name?: string }

export function apply(ctx: Ctx, cfg: Config): void {
  const name = cfg.name ?? "world"
  ctx.log("hello-ts 装配完成,name=" + name)
  ctx.provide("ts.hello", {
    tone: Tone.Friendly,
    message: "你好," + name,
    render(this: { message: string }): string {
      return "[" + this.tone + "] " + this.message
    },
  })
  ctx.effect("hello-ts", () => ctx.log("hello-ts 清理"))
}
