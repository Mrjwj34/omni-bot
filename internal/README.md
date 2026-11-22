# internal

`omni-bot` Go 后端内部实现包集合，按子系统职责边界组织。

## 目录索引

- `channel/` — Channel Hub WS JSON-RPC 2.0 服务端、WebUI 实时广播与 HTTP Blob 媒体端点。
- `envload/` — 开发期与离线评测用的轻量 `.env.local` 环境变量加载器。
- `memory/` — 记忆层存储、召回与阶段 0 离线消融评测子系统。
- `persona/` — 人格三层结构与三维 PAD 连续情绪系统及其评测模块。
- `protocol/` — 前端适配器与 WebUI 共享的 WS JSON-RPC 2.0 线协议、内容块与出站动作 IR。
