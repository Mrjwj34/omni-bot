# internal/tools/mcp

基于官方 `github.com/modelcontextprotocol/go-sdk` 的 MCP 客户端管理器，支持 Streamable HTTP 与本机 stdio 传输、工具名前缀隔离、`structuredContent` 透传、`notifications/tools/list_changed` 热刷新与连接池原子热重载。

## 文件索引

- `manager.go` — `Manager`、`Snapshot` 冻结视图、Streamable HTTP/stdio 连接管理、禁用 Sampling/Elicitation 能力声明与原子 `Reload` 实现。
- `mcp_test.go` — 基于进程内官方 `mcp.NewServer` + `httptest.NewServer` 的发现、前缀执行、结构化结果、动态工具列表刷新与在途 Run 隔离集成测试。
