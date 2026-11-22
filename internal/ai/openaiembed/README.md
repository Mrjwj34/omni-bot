# internal/ai/openaiembed

基于标准库 `net/http` 的 OpenAI 兼容 `/embeddings` 协议适配器，支持批量自动分片（默认 `<= 32`）、1024 维 Matryoshka 截断与 L2 归一化。

## 目录索引

- `client.go` — `ai.EmbeddingModel` 客户端实现（自动分片、索引重排、L2 归一化、多 Key 轮询与错误分类）。
- `client_test.go` — 基于 `httptest.NewServer` 的分片、乱序重排、L2 归一化与 HTTP 错误分类单测。
