# internal/persona

人格三层表达与三维 PAD 动态情绪引擎及其离线/实测验证模块。

## 目录索引

- `eval/` — Phase 0 实测验证框架（Decisions API PAD 单调性、25 轮轨迹仿真与 Interactions API 盲评 A/B）。
- `pad/` — 三维 PAD 连续情绪动力学模型与 30 个细粒度第一人称内在感受原型表。

## 文件索引

- `emotion.go` — 生产级三维 PAD 状态管理器（`EmotionEngine`）、时间衰减查询、刺激持久化与 System 1 Decisions 门控请求构造器。
- `emotion_test.go` — 基于真实 SQLite 的 PAD 状态持久化、时间回落、中性死区静默、拆分上下文门控构造与并发写入单元测试。
