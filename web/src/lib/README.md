# web/src/lib

`omni-bot` WebUI 共享基础层，提供统一的 Tonal Surface 设计系统原语、响应式应用导航壳、类型化 REST API 客户端与 WS JSON-RPC 2.0 实时状态仓库。

## 目录索引

- `api/` — 匹配 Go 后端契约的类型化 REST API 客户端与共享协议类型定义。
- `components/` — 零边框嵌套的 Svelte 5 UI 原语与响应式导航壳组件。
- `stores/` — 基于 Svelte 5 Runes 的 WS JSON-RPC 2.0 自动重连实时状态仓库。

## 统一设计系统契约（页面开发必读）

所有并行页面模块必须复用本目录原语与 `web/src/app.css` 令牌，确保全站视觉与交互 100% 一致：

1. **Tonal Surface 层级（严禁边框套边框）**：
   - 画布 `bg-surface-canvas`（`#F6F8F7`） -> 悬浮卡片 `SurfaceCard variant="raised"`（`#FFFFFF` + 柔光投影） -> 凹陷区/输入/次级面板 `bg-surface-subtle`（`#EDF2F0`）；
   - 情绪与状态亮点使用柔和糖果色板：`tint-mint` / `tint-peach` / `tint-sky` / `tint-lilac` / `tint-amber`；
   - 禁止叠加 `border` / `1px` 细线套卡片，统一通过色差、圆角与留白表达层级。
2. **统一交互原语（`$lib/components/ui`）**：
   - `SurfaceCard`、`Badge`、`Button`、`Switch`、`Tabs`、`Dialog`、`Input`、`Textarea`、`EmptyState`、`PageHeader`；
   - 全部组件仅使用 Svelte 5 Runes（`$props` / `$bindable` / `$state` / `$derived`）与 Snippets（`{#snippet}` / `{@render}`）。
3. **克制文案与微动效**：
   - `PageHeader` 只放标题 + 状态 `Badge` + 操作按钮，不堆砌说明段落；
   - 连续数值变化（如 PAD 情绪坐标）优先使用 `svelte/motion` 的 `Spring`。
4. **通信契约（`$lib/api` 与 `$lib/stores/realtime.svelte.ts`）**：
   - REST 请求统一走 `api.*`；
   - 实时事件直接读取 `realtime.padState`、`realtime.activeRun`、`realtime.adapters`、`realtime.recentEvents`、`realtime.liveTimeline` 与 `realtime.callRPC(...)`，禁止在页面内重复建立 WebSocket 连接。
