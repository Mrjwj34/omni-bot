# internal/webui/dist

SvelteKit SPA 静态构建产物嵌入目录。构建产物由 `pnpm --dir web build` 同步写入并由 `.gitignore` 忽略，仅保留占位文件以确保未构建前端时 Go 编译与测试仍可正常通过。

## 文件索引

- `.gitkeep` — 保证 `//go:embed all:dist` 在全新克隆仓库中始终可编译的空占位文件。
