# internal/tools

工具注册表、安全执行器及核心内置工具实现（记忆主动回忆、DeepSeek 联网搜索、防 SSRF 网页正文提取与表情库管理）。

## 目录索引

- `sandbox/` — 默认关闭的受限沙箱 `bash`/`read`/`write`/`edit`/`find`/`grep` 工具集与环境脱敏实现。

## 文件索引

- `registry.go` — `Tool` / `Result` 统一契约、`Registry` 确定性 Schema 导出、快照隔离与防 Panic/非法 JSON 安全执行器。
- `memory_recall.go` — `memory_recall` 内置工具与 `MemoryRecaller` 使用侧接口。
- `web_search.go` — 对接 DeepSeek `/v1/tools/search` 的 `web_search` 内置工具。
- `web_fetch.go` — 带私网/回环 SSRF 拦截、2MB 响应体上限与 HTML 主正文清洗提取的 `web_fetch` 内置工具。
- `manage_meme.go` — 对接 `*meme.Vault` 的 `manage_meme`（检索、印象修正与拉黑删除）内置工具。
- `tools_test.go` — 基于 `httptest.NewServer` 与真实 SQLite 表情库的注册表与内置工具单元测试。
