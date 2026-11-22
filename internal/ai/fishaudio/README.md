# internal/ai/fishaudio

基于标准库 `net/http` 的 Fish Audio `/tts` 语音合成协议适配器（通过 HTTP `model` 请求头指定模型）。

## 目录索引

- `client.go` — `ai.SpeechModel` 客户端实现（`model` HTTP Header、多 Key 轮询、音频 MIME 解析与错误分类）。
- `client_test.go` — 基于 `httptest.NewServer` 的请求头/请求体协议校验与 HTTP 错误分类单测。
