# internal/channel

Channel Hub 服务端实现，提供前端适配器与 WebUI 共享的 WebSocket JSON-RPC 2.0 通道、能力协商分发及 HTTP Blob 媒体存取接口。

## 文件索引

- `blob.go` — 使用侧极简 `BlobStore` 接口定义及 `POST /api/blobs`、`GET /api/blobs/{id}` HTTP 媒体端点。
- `hub.go` — `Hub` 核心实现，管理适配器连接、WebUI 订阅者非阻塞广播、出站 RPC 关联/超时与断连清理。
- `hub_test.go` — 基于真实 `httptest.NewServer` 与真实 WebSocket 客户端的端到端集成测试。
