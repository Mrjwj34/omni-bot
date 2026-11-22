# internal/kernel

Bot Kernel 核心运行时包，负责按房间严格串行的 Single-Flight 总线、统一外部触发器、三阶段钩子管线（`OnGate` / `OnAssemble` / `OnPostRun`）、受控 `<= 6` 轮 Agent Loop、热重载运行时与完整 WebUI HTTP REST API。

## 文件索引

- `types.go` — 统一 `TriggerEvent`、`RunContext`、不可变冻结 `RunSpec` 与对齐前端契约的 Trace 类型定义。
- `hooks.go` — 三阶段钩子接口（`GateHook`、`ContextContributor`、`PostRunHook`）与确定性有序 `HookChain` 执行器。
- `bus.go` — 按 `room_id` 严格串行、跨房间并行、支持防抖与在途微批合并的 `SingleFlightBus` 调度总线。
- `bus_test.go` — 覆盖同房串行、跨房并行、微批合并、防抖与取消以及 `HookChain` 确定性冻结的单元测试。
- `loop.go` — 受控 `<= 6` 轮 Agent Loop，保持冻结 `RunSpec` 前缀字节不变、透传不透明 `NativeContent`、受控单次重试与工具错误回传。
- `pipeline.go` — 默认三阶段钩子管线（并发门控+PAD+向量、有序六层上下文装配、出站 IR 投递、Trace 持久化与非阻塞后台维护）。
- `e2e_test.go` — 覆盖 Agent Loop 重试/容错/上限以及完整真实 SQLite+Hub+WS+Memory+Persona+Meme+Tools+Dreaming 的端到端集成测试。
- `runtime.go` — 统一内核 `Runtime` 生命周期管理，支持 AI Registry、工具集（含默认关闭 Bash 沙箱开关）与 MCP 连接池原子热重载。
- `httpapi.go` — 与 `web/src/lib/api/` 1:1 严格对齐的 WebUI HTTP REST API 处理器（驾驶舱、Trace、记忆、人格快照、表情媒体、脱敏配置与只写密钥、对话调试）。
- `httpapi_test.go` — 覆盖全部 REST API 端点、只写密钥零泄露校验与配置热重载 Bash 沙箱开关的真实 `httptest` 契约测试。
