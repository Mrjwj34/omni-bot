# internal/ai/google

基于官方 `google.golang.org/genai` 的 Google 协议适配层，封装 Gemini Interactions API、Gemini Decisions API 与内部 File API 缓存。

## 目录索引

- `client.go` — GenAI 客户端构造、多 Key 轮询 HTTP Transport 与 Registry 工厂注册。
- `errors.go` — Google SDK / HTTP 错误到 `internal/ai` 统一错误分类的转换。
- `files.go` — 内部封闭的 Google File API 上传、视频 ACTIVE 轮询、44h TTL 缓存与自适应 `media_resolution`。
- `interactions.go` — 基于无状态 `step_list`（`store: false`）与不透明 `NativeContent` 的 `LanguageModel` 实现。
- `decisions.go` — 支持 `noul` / `score` / `choice` 的 Gemini Decisions API `DecisionModel` 实现。
- `google_test.go` — 基于 `httptest.NewServer` 的离线单元测试（多轮工具续接、File API 缓存过期、Decisions、错误分类与禁重试校验）。
- `integration_test.go` — 带 `//go:build integration` 门禁的真实 Gemini API 冒烟测试。
