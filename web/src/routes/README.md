# web/src/routes

SvelteKit SPA 客户端路由树，统一关闭 SSR 与预渲染以交由 Go `embed.FS` 提供单页回退。

## 文件索引

- `+layout.svelte` — 全局根布局组件与样式挂载入口。
- `+layout.ts` — 关闭 SSR 与预渲染的 SPA 路由全局配置。
- `+page.svelte` — 首页入口视图。
