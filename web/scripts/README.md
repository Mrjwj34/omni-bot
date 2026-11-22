# web/scripts

WebUI 构建流水线辅助脚本目录。

## 文件索引

- `sync-dist.mjs` — 在 `vite build` 后将 `web/build/` 静态产物同步到 `internal/webui/dist/`，同时保留 Git 追踪占位文件与目录索引。
