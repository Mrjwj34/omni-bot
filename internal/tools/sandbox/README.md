# internal/tools/sandbox

默认关闭的隔离沙箱 Bash 与文件工具集（`bash`、`read`、`write`、`edit`、`find`、`grep`），强制路径边界约束、环境变量脱敏白名单、子进程组超时强杀与输出头尾截断落盘。

## 文件索引

- `config.go` — 沙箱配置、`RegisterIfEnabled` 默认关闭门控与基于 `EvalSymlinks` 的路径越界防护 `Workspace`。
- `env.go` — 子进程环境变量白名单清洗与敏感 API Key / Token / Secret 兜底剥离。
- `proc_windows.go` — Windows 子进程组创建与 `taskkill /F /T` 进程树强杀实现。
- `proc_unix.go` — Unix `Setpgid` 进程组隔离与负 PID `SIGKILL` 强杀实现。
- `bash.go` — `bash` 工具实现（跨平台 Shell 调度、超时控制、头尾截断与 `.omni-logs/` 完整日志落盘）。
- `files.go` — 受限于沙箱目录的 `read`、`write`、`edit`、`find` 与 `grep` 五项文件工具实现。
- `sandbox_test.go` — 基于真实临时文件系统与真实子进程的默认关闭门控、路径穿越拦截、密钥脱敏、截断落盘与超时强杀测试。
