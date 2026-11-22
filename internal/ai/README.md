# internal/ai

AI 能力三层抽象（Provider / Protocol / Model）的核心契约、统一错误分类与功能槽位注册表。

## 目录索引

- `google/` — 基于官方 `google.golang.org/genai` 的 Gemini Interactions、Decisions 与内部 File API 适配器。
- `types.go` — 三层配置结构体、五类对称协议接口（Language/Decision/Embedding/Speech/Transcription）与多模态消息类型定义。
- `errors.go` — 统一错误分类（Auth/InvalidArg/RateLimit/Transient/ContextTooLong/Unknown）、HTTP 状态映射与可重试判定。
- `registry.go` — 线程安全的 API Key 轮询器与 Provider/Model/功能槽位注册解析表。
- `ai_test.go` — Key 轮询、错误分类与槽位解析单元测试。
