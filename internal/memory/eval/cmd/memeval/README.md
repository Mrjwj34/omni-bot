# internal/memory/eval/cmd/memeval

阶段 0 记忆层消融评测 CLI 入口，默认离线运行，传入 `-live` 时调用 `Qwen/Qwen3-Embedding-4B` 并缓存向量至本地目录。

## 文件索引

- `main.go` — 执行 `B0`~`B4`（含 2560d / 1024d）、衰减/强化/跳数子消融、纯 Go 向量扫描基准与旧库快照核验的主程序。
