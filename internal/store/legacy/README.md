# internal/store/legacy

面向旧版 AstrBot 数据目录的只读幂等资产迁移导入器（时间线、摘要、表情库及缩略图、`bk101` 人格与可选 `mem0_export.jsonl`）。

## 文件索引

- `migrate.go` — `MigrateLegacy` 只读幂等迁移主流程，涵盖 `timeline.db`、`memes.db`、`data_v4.db` 与可选 `mem0_export.jsonl` 导入。
- `persona_defaults.go` — `bk101` 第一人称三层人格默认信念、9 条原子性格条目与默认 PAD 气质参数种子定义。
- `migrate_test.go` — 离线确定性夹具迁移幂等测试与面向本地只读 `data/legacy_snapshot/` 的条件门控对账测试。
