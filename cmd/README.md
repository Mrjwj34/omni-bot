# cmd

`omni-bot` 可执行程序入口目录，存放独立运行的前端适配器与内核主进程。

## 目录索引

- `kernel/` — Bot Kernel 主进程可执行入口（统一 HTTP/WS API、内嵌 WebUI 与 `migrate-legacy` 迁移 CLI）。
- `napcat-adapter/` — NapCat OneBot v11 与 Bot Kernel Channel JSON-RPC 2.0 双向适配器。
