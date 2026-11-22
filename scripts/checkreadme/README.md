# scripts/checkreadme

跨平台 Go 目录索引校验器，确保每个受跟踪目录（排除 `.git`、`.github`、`data`、`node_modules`）均包含 `README.md` 且索引条目与实际文件/子目录完全一致。

## 文件索引

- `main.go` — 校验器 CLI 入口，支持 Git 感知文件收集与目录索引完整性/陈旧条目检查。
- `main_test.go` — 基于 `t.TempDir()` 的表驱动与 Git 仓库真实集成测试。
