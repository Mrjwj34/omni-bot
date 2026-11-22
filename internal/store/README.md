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
- `store_test.go` — 真实 SQLite WAL 参数、幂等迁移、FTS5 索引、向量编解码与密钥脱敏单元测试。
