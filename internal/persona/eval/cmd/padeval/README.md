# internal/persona/eval/cmd/padeval

读取 `.env.local` 并驱动 Gemini Decisions API 与 Interactions API 完成 Phase 0 全套实测的 CLI 工具。

## 文件索引

- `main.go` — 解析命令行参数、加载环境变量、执行全套实验并写出 JSON 与 Markdown 评测报告。
