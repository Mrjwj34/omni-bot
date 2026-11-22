# internal/meme

表情包库引擎，提供纯 Go 64 位感知哈希（dHash）去重、动图 2x2 接触表抽帧拼图、缩略图生成、视觉打标与混合手牌检索能力。

## 文件索引

- `phash.go` — 纯 Go 9x8 灰度 64 位 dHash 计算、十六进制编解码与汉明距离计算。
- `media.go` — 表情缩略图生成与多帧 GIF 2x2 接触表 JPEG 拼图构造器。
- `vault.go` — `Vault` 收录去重、混合关键词+向量手牌检索（`<available_memes>`）、印象更新、使用计数与拉黑删除实现。
- `meme_test.go` — 基于真实生成 PNG/多帧 GIF、临时目录 BlobStore 与真实 SQLite 的表情库单元测试。
