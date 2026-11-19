# host/desktop(视图映射骨架)

> 配套 [plugin-kernel-design.md](plugin-kernel-design.md) §12(宿主)与 AGENTS §2.2(宿主适配器)。

## 1. 范围

```
host/desktop/   内核快照 → 视图模型(View)→ Renderer
```

本包**不引入任何窗口/UI 依赖**(mygo 等),只定义映射契约;真实窗口由宿主在 Renderer 里实现。

## 2. 契约

| 类型 | 语义 |
|---|---|
| `Renderer` | `Render(View) error`;桌面宿主实现 |
| `View` | 只读视图:FiberRow / ServiceRow / EffectRow / Events |
| `New(app, renderer)` | app 与 renderer 都不能为 nil |
| `Refresh()` | `app.Inspect()` → View → Render;返回本轮视图 |

## 3. 测试

- `desktop.TestRefresh`:挂载提供服务的插件后刷新,断言 fibers/services/effects 映射与渲染器调用次数(两次刷新);
- `desktop.TestNewValidation`:nil app/renderer 明确报错。

## 4. 偏差与边界

1. **没有真实窗口**:没有 mygo/菜单/托盘/主线程;Renderer 只是接口。
2. **没有事件驱动刷新**:`Refresh` 由宿主显式调用;没有订阅内核事件自动重绘。
3. **只读视图**:不能从 View 触发插件操作(命令/审批入口留空)。
4. **没有生命周期托管**:不负责 app 的启动/停止。
5. **快照是一次性的**:并发装配期间刷新可能看到中间状态(与 `Inspect` 语义一致)。

## 5. 下一步

1. mygo 窗口 Renderer + 事件订阅(节流重绘);
2. 命令面板/审批弹窗的映射(只做数据,不做业务);
3. 与 host/server 共享视图模型(若需要)。

## 6. 验证命令

```powershell
go test -race -count=1 ./host/desktop
go test -race -count=1 ./...
pwsh scripts/guard.ps1
```