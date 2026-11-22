# internal/kernel

Bot Kernel 核心运行时包，负责按房间严格串行的 Single-Flight 总线、统一外部触发器与三阶段钩子契约（`OnGate` / `OnAssemble` / `OnPostRun`）。

## 文件索引

- `types.go` — 统一 `TriggerEvent`、`RunContext`、不可变冻结 `RunSpec` 与对齐前端契约的 Trace 类型定义。
- `hooks.go` — 三阶段钩子接口（`GateHook`、`ContextContributor`、`PostRunHook`）与确定性有序 `HookChain` 执行器。
- `bus.go` — 按 `room_id` 严格串行、跨房间并行、支持防抖与在途微批合并的 `SingleFlightBus` 调度总线。
- `bus_test.go` — 覆盖同房串行、跨房并行、微批合并、防抖与取消以及 `HookChain` 确定性冻结的单元测试。
