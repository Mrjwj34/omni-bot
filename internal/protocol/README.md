# internal/protocol

前端适配器与 WebUI 共享的 WebSocket JSON-RPC 2.0 线协议类型、归一化内容块定义与方法常量。

## 文件索引

- `jsonrpc.go` — JSON-RPC 2.0 通用信封、标准错误码与编解码辅助函数。
- `types.go` — 平台无关入站消息、归一化内容块、`MediaRef`、能力注册与 WebUI 订阅/事件契约。
- `protocol_test.go` — JSON-RPC 信封、内容块与 WebUI 载荷的表驱动序列化往返测试。
