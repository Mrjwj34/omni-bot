# internal/memory/eval

阶段 0 记忆层离线评测基准集、五阶段消融检索引擎与向量检索基准包。

## 目录索引

- `cmd/` — 阶段 0 记忆消融评测 CLI 工具入口目录。

## 文件索引

- `types.go` — 记忆条目、关联边、召回事件、评测查询与聚合指标数据结构定义。
- `metrics.go` — `Recall@4`、`MRR`、时效准确率、噪声率与延迟分位数精确计算实现。
- `dataset.go` — 30 天熟人群聊确定性合成评测集（昵称漂移、时序演化链、600 条高干扰水群噪音与 48 条标注查询）。
- `tokenize.go` — 面向 SQLite FTS5 `unicode61` 的中英混合双字切分与安全查询构造器。
- `vector.go` — 纯 Go 连续 `float32` 向量存储、L2 归一化、4 路展开点积 Top-K 扫描与规模延迟基准。
- `embedder.go` — OpenAI 兼容 `/embeddings` 轻量 `net/http` 客户端（批量 `<=32`、支持 Matryoshka 维数）与本地磁盘缓存。
- `engine.go` — 基于真实 `modernc.org/sqlite`（WAL + FTS5）的 `B0`~`B4` 五阶段消融检索引擎与防马太效应衰减强化实现。
- `dataset_metrics_test.go` — 评测集结构不变量与指标边界条件的表驱动单元测试。
- `engine_test.go` — 真实 SQLite FTS5、向量索引并发一致性、Embedding 批量缓存及 `B0`~`B4` 离线消融单元测试。
