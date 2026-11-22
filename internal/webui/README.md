# internal/webui

通过 Go `embed.FS` 内嵌并托管 SvelteKit SPA 静态产物的 HTTP 服务端包，支持客户端路由 `index.html` 回退与不可变资源长缓存，运行时零 Node.js 依赖。

## 目录索引

- `dist/` — SvelteKit 构建产物嵌入目录（构建文件由 `.gitignore` 忽略，仅保留编译占位文件）。
- `webui.go` — `embed.FS` 静态资源挂载、`Handler()` 与 `NewHandler(fs.FS)` SPA 回退及缓存头策略实现。
- `webui_test.go` — 基于 `httptest` 与 `fstest.MapFS` 的路由回退、缓存头、保留前缀与内嵌产物测试。
