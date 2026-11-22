# internal/persona/eval

Phase 0 人格三层结构、PAD 刺激打分与 Interactions API 实测评测框架（默认离线单测，实盘调用收口于 `cmd/padeval/`）。

## 目录与文件索引

- `cmd/` — 实盘评测命令行入口目录。
- `persona.go` — 现网 `bk101` 725 字旧人设、第一人称三层迁移候选与 System 1 / System 2 提示词拼装器。
- `scenarios.go` — 18 组 Decisions 刺激单调性用例、25 轮多阶段长对话脚本与 10 组盲评 A/B 群聊场景。
- `client.go` — 基于官方 `google.golang.org/genai` SDK 的 Decisions API、无状态 Interactions API 与结构化盲评裁判封装。
- `runner.go` — 串联实验一/实验二全流程、计算分位数与单调性指标并渲染 Markdown 评测报告。
- `eval_test.go` — 验证人设不变量、场景完整性、分数归一化、分位数与盲评去盲逻辑的离线单元测试。
