# docs/benchmarks

Phase 0 及后续离线/在线实测基准报告目录，用于以量化数据裁决核心子系统的算法取舍、参数校准与 A/B 对照结论。

## 文件索引

- `memory-eval.md` — 记忆层五阶段消融评测（`B0`~`B4`）、2560d vs 1024d Matryoshka、纯 Go 向量扫描性能与生产旧库快照体检报告。
- `persona-pad-eval.md` — Phase 0 人格三层结构、Decisions API PAD 刺激单调性、25 轮轨迹校准与 `bk101` Interactions API 盲评 A/B 实测报告。
