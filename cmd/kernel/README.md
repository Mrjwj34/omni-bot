# cmd/kernel

`omni-bot` Bot Kernel 可执行入口，提供默认统一 HTTP/WS+内嵌 WebUI 服务（`serve`）以及只读幂等的旧版资产迁移子命令（`migrate-legacy`）。

## 文件索引

- `main.go` — 主进程入口、`serve` 命令行参数解析、`.env.local` 加载、统一 HTTP/WS+WebUI 托管与信号优雅退出。
- `migrate.go` — `migrate-legacy` 只读幂等旧版资产迁移子命令实现与汇总输出。
- `main_test.go` — 覆盖 `migrate-legacy` 源目录只读幂等迁移与实时 HTTP 服务（`/api/...` + 内嵌 WebUI）启动关闭的冒烟测试。
