# web

`omni-bot` WebUI 前端工程，基于 Svelte 5（Runes & Snippets）、SvelteKit（`@sveltejs/adapter-static` SPA）、TypeScript（`strict`）、Tailwind CSS v4 与 `bits-ui` 构建，产物同步至 Go 后端内嵌。

## 目录索引

- `package.json` — 前端依赖清单、pnpm 工具链声明与 `dev`/`check`/`build` 脚本。
- `pnpm-lock.yaml` — pnpm 确定性依赖锁文件。
- `pnpm-workspace.yaml` — pnpm 12 构建白名单（`esbuild`）与依赖安全策略配置。
- `svelte.config.js` — SvelteKit 与 `@sveltejs/adapter-static`（`fallback: 'index.html'`）配置。
- `tsconfig.json` — TypeScript 严格模式与 SvelteKit 路径类型配置。
- `vite.config.ts` — Vite 构建插件及开发期 `/ws`、`/api`、`/media` 代理配置。
- `scripts/` — 前端构建后同步静态产物到 Go 嵌入目录的辅助脚本。
- `src/` — Svelte 5 应用源码、样式主题、组件库与路由页面。
- `static/` — 原样拷贝的静态资源（图标等）。
