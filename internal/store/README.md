# internal/store

`omni-bot` 的 SQLite WAL 持久化层、动态配置与只写密钥存储、本地媒体 Blob 存储与旧资产迁移基础包。

## 目录索引

- `migrations/` — 内嵌的版本化 SQLite 迁移脚本目录。

## 文件索引

- `store.go` — SQLite WAL 数据库初始化、读写连接池分离与生命周期管理。
- `migrate.go` — 基于 `embed.FS` 的幂等版本化 Schema 迁移执行器。
- `tokenize.go` — 面向 SQLite FTS5 `unicode61` 的中英混合双字切分与安全查询构造器。
- `vector.go` — 1024 维 `float32` 小端序向量的 SQLite BLOB 编解码辅助函数。
- `secrets.go` — 严格 write-only 脱敏语义的本地密钥写入、状态列表与内核只读解析实现。
- `timeline.go` — 群聊追加式时间线与滚动摘要仓储方法。
- `memory.go` — 记忆 CRUD、FTS5 同步检索、活跃向量加载、时效取代、防马太强化与实体别名仓储方法。
- `persona.go` — 三层人格核心、原子性格条目替换、版本快照留存与快照 Diff 仓储方法。
- `pad.go` — 三维 PAD 情绪当前状态与演进轨迹历史仓储方法。
- `memes.go` — 表情包元数据、前缀查找、使用计数、软删除与 `meme_seen` 防重收录仓储方法。
- `traces.go` — Run Trace 结构化可观测记录的写入、查询与列表仓储方法。
- `config.go` — 动态运行配置（Provider、Model、Slot、MCP、Feature Flags）与脱敏视图仓储方法。
- `blob.go` — 内容寻址 `blobs/` 与 `memes/` 本地原子文件存储及路径穿越防护实现。
- `store_test.go` — 真实 SQLite WAL 参数、幂等迁移、FTS5 索引、向量编解码与密钥脱敏单元测试。
- `repo_test.go` — 全领域仓储不变量与 WAL 并发读写压测单元测试。
- `blob_test.go` — Blob 去重、失败原子清理、表情文件存取与路径穿越攻击向量单元测试。
