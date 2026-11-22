# cmd/napcat-adapter

NapCat OneBot v11 前端适配器，负责将 QQ 群消息、卡片与合并转发归一化为平台无关内容块，并执行 Kernel 下发的出站动作 IR。

## 文件索引

- `actions.go` — OneBot v11 WS Action RPC 客户端（全 8 项协议动作、关联 ID 与超时控制）及出站动作 IR 执行器。
- `actions_test.go` — 基于真实进程内 WS 服务端与 Kernel HTTP Blob 端点的动作映射、错误处理与超时测试。
- `bridge.go` — NapCat OneBot v11（正向/反向 WS + Bearer 鉴权 + 指数退避重连）与 Kernel `/ws` 双向运行时桥接器。
- `bridge_test.go` — 结合模拟 OneBot v11 WS 服务端与真实 `internal/channel.Hub` 的全链路入站/出站、断线重连与反向 WS 集成测试。
- `main.go` — `napcat-adapter` CLI 入口，解析运行参数并通过 `signal.NotifyContext` 执行优雅停机。
- `onebot.go` — OneBot v11 群消息段解析器、QQ/B 站 JSON 卡片解包器、递归合并转发展开、语音转码钩子与 Kernel HTTP Blob 客户端。
- `onebot_test.go` — 基于真实 `channel.Hub` HTTP Blob 端点的表驱动单测，覆盖全部消息段、卡片解包、递归转发深度上限与音频转码降级。
