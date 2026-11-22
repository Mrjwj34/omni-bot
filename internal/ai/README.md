# internal/ai

AI 能力三层抽象（Provider / Protocol / Model）的核心契约、统一错误分类与功能槽位注册表。

## 目录索引

- `fishaudio/` — 基于标准 `net/http` 的 Fish Audio TTS 协议适配器。
- `google/` — 基于官方 `google.golang.org/genai` 的 Gemini Interactions、Decisions 与内部 File API 适配器。
- `openaiembed/` — 基于标准 `net/http` 的 OpenAI 兼容 Embedding 协议适配器（批量分片与 L2 归一化）。
- `whisper/` — 本地 Whisper 离线语音转文字适配器（`whisper-cli` 子进程与本地 HTTP 双模式）。
- `types.go` — 三层配置结构体、五类对称协议接口（Language/Decision/Embedding/Speech/Transcription）与多模态消息类型定义。
- `errors.go` — 统一错误分类（Auth/InvalidArg/RateLimit/Transient/ContextTooLong/Unknown）、HTTP 状态映射与可重试判定。
- `registry.go` — 线程安全的 API Key 轮询器与 Provider/Model/功能槽位注册解析表。
- `fallback.go` — 按模型 `Capabilities` 自动执行图片占位、音频转写与视频抽帧拼图/音轨转写的 `MediaFallbackMiddleware` 及 `blob_id` 缓存。
- `ai_test.go` — Key 轮询、错误分类与槽位解析单元测试。
- `fallback_test.go` — `MediaFallbackMiddleware` 透传、缓存与真实 `ffmpeg` 视频降级单元测试。
