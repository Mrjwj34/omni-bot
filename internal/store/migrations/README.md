# internal/store/migrations

内嵌至 `internal/store` 的版本化 SQLite 数据库迁移脚本目录。

## 文件索引

- `001_init.sql` — 初始全量表结构定义（时间线、摘要、记忆与 FTS5、实体别名、人格与快照、PAD 情绪状态与轨迹、表情库、Run Trace、动态配置与密钥）。
