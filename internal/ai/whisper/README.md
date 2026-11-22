# internal/ai/whisper

本地离线 Whisper 语音转文字（`ai.TranscriptionModel`）适配器，支持零 CGO 的 `whisper-cli` 子进程模式（结合 `ffmpeg` 16kHz 单声道 WAV 转码）与本地 OpenAI 兼容 `whisper-server` HTTP 模式。

## 目录索引

- `client.go` — `whisper-cli` 子进程与 `/v1/audio/transcriptions` HTTP 双模式实现及 WAV 时长解析。
- `whisper_test.go` — HTTP `httptest.NewServer` 测试与结合真实 `ffmpeg` 转码的子进程 CLI 单元测试。
