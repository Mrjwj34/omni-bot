# web/src/routes

SvelteKit SPA 客户端路由树，统一关闭 SSR 与预渲染并由全局 `AppShell` 承载 7 大核心功能视图。

## 目录索引

- `+layout.svelte` — 全局导航壳布局与 `/ws` 实时连接生命周期挂载。
- `+layout.ts` — 关闭 SSR 与预渲染的 SPA 路由全局配置。
- `+page.svelte` — `/` 驾驶舱总览页（实时 PAD 呼吸球、Run 管线步进器、核心指标与事件流）。
- `chat/` — `/chat` 内建对话调试台路由。
- `memes/` — `/memes` 表情金库路由。
- `memory/` — `/memory` 记忆检索、时序链与 Dreaming 路由。
- `persona/` — `/persona` 三层人格、气质 PAD 与快照 Diff 路由。
- `settings/` — `/settings` Provider/Model 槽位、Write-Only 密钥、MCP 与安全开关路由。
- `traces/` — `/traces` Run Trace 详情与时间线路由。
