# web/src/lib/api

WebUI 与 Go Kernel 通信的类型化 REST API 客户端及共享 WS JSON-RPC 2.0 / 领域类型定义。

## 文件索引

- `client.ts` — 面向 `/api/...` 与 `/api/blobs` 的类型化 `fetch` 客户端及 `ApiError` 错误封装。
- `index.ts` — API 客户端与协议类型统一导出入口。
- `types.ts` — 对齐 `internal/protocol` 与 `internal/persona/pad` JSON 契约的 TypeScript 接口定义。
