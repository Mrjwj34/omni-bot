# internal/envload

面向开发期离线评测与集成测试的轻量 `.env.local` 加载器，支持注释、引号与 `export` 前缀且绝不覆盖已有进程环境变量。

## 文件索引

- `envload.go` — `.env` 语法解析与非覆盖式加载实现（`Parse`、`LoadFile`、`LoadIfExists`、`LoadRepoEnv`）。
- `envload_test.go` — 基于 `t.TempDir()` 与真实临时文件的表驱动单元测试。
