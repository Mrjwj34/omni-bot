# internal/persona

人格三层表达与三维 PAD 动态情绪引擎及其离线/实测验证模块。

## 目录索引

- `eval/` — Phase 0 实测验证框架（Decisions API PAD 单调性、25 轮轨迹仿真与 Interactions API 盲评 A/B）。
- `pad/` — 三维 PAD 连续情绪动力学模型与 30 个细粒度第一人称内在感受原型表。

## 文件索引

- `emotion.go` — 生产级三维 PAD 状态管理器（`EmotionEngine`）、时间衰减查询、刺激持久化与 System 1 Decisions 门控请求构造器。
- `emotion_test.go` — 基于真实 SQLite 的 PAD 状态持久化、时间回落、中性死区静默、拆分上下文门控构造与并发写入单元测试。
- `persona.go` — 三层人格提示词拼装器（`PersonaEngine`）、硬包络限制气质 EMA 慢漂移、快照 Diff 与安全回滚实现。
- `reflect.go` — 零证据池单次强模型原子性格反思（`keep`/`add`/`revise`/`drop`）、8~12 条硬上下限与第一人称不变量校验。
- `persona_test.go` — 覆盖三层拼装与前缀缓存隔离、气质 EMA 硬包络钳制、性格反思上下限与第一人称校验、快照 Diff 与回滚的真实 SQLite 单元测试。
