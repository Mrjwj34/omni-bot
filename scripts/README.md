# scripts

本地与 CI 共享的统一质量门禁脚本及跨平台校验工具。

## 目录索引

- `checkreadme/` — 跨平台 Go 目录 `README.md` 索引一致性校验器与单元测试。
- `ci.ps1` — Windows/跨平台 PowerShell 统一 CI 入口（`gofmt`、`go vet`、`go test`、`checkreadme` 与条件 `web/` 检查）。
- `ci.sh` — Linux/macOS Bash 统一 CI 入口，与 `ci.ps1` 执行完全一致的门禁步骤。
