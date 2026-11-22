# internal

`omni-bot` Go 后端内部实现包集合，按子系统职责边界组织。

## 目录索引

- `ai/` — AI 三层抽象（Provider / Protocol / Model）、协议适配器与多模态降级中间件。
- `channel/` — Channel Hub WS JSON-RPC 2.0 服务端、WebUI 实时广播与 HTTP Blob 媒体端点。
- `envload/` — 开发期与离线评测用的轻量 `.env.local` 环境变量加载器。
- `kernel/` — Bot Kernel 核心运行时、Single-Flight 串行总线、三阶段钩子管线、Agent Loop 与 WebUI HTTP API。
- `meme/` — 表情包库引擎、纯 Go dHash 感知去重、动图 2x2 抽帧拼图与混合手牌检索包。
- `memory/` — 记忆层存储、召回与阶段 0 离线消融评测子系统。
- `persona/` — 人格三层结构与三维 PAD 连续情绪系统及其评测模块。
- `protocol/` — 前端适配器与 WebUI 共享的 WS JSON-RPC 2.0 线协议、内容块与出站动作 IR。
- `store/` — SQLite WAL 持久化、动态配置、只写密钥、本地 Blob 存储与旧资产迁移包。
- `tools/` — 工具注册表、核心内置工具、默认关闭的沙箱 Bash/文件工具集与官方 MCP 客户端管理器。
- `webui/` — 通过 Go `embed.FS` 内嵌托管 SvelteKit SPA 静态产物的 HTTP 服务包。
