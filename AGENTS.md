# AGENTS.md

本项目是面向熟人群聊场景的拟人化 Bot（Go 后端 + Svelte 5 WebUI）。任何 Agent 与开发者在阅读代码或提交改动前，必须遵守本文件中的所有约定。

## 1. 仓库导航与必读顺序

1. 先读根目录及目标目录的 `README.md` 索引，快速定位模块职责与文件清单，避免盲目全仓扫描。
2. 涉及整体运行时、通道协议、AI 三层抽象与 WebUI 边界，先读 [docs/design/architecture.md](docs/design/architecture.md)。
3. 涉及记忆、人格与情绪演化的改动，先读 [docs/design/memory.md](docs/design/memory.md) 与 [docs/design/persona-and-emotion.md](docs/design/persona-and-emotion.md)，索引见 [docs/design/README.md](docs/design/README.md)。

## 2. 核心工程原则

- **KISS 优先**：优先采用最小、最清晰的数据结构与控制流；禁止为了架构“看起来完整”引入不必要的抽象层、平行表或外部重型中间件。
- **实测驱动**：涉及召回率、排序权重、记忆衰减、Prompt 表达质感等效果问题，必须以离线评测基准或 A/B 实测数字为准；未带来可观测收益的复杂度一律删除。
- **拒绝代码层死板表演规则**：情绪、表达风格与人格质感由模型基于连续状态与自然语言内在感受自主呈现，**禁止**在代码中编写僵硬的启发式规则（例如根据情绪数值硬截断气泡数量、字数或强行篡改发言格式）。
- **热路径零阻塞**：实时消息处理与被动召回链路不得被后台归档、Dreaming 巩固或慢速网络 I/O 锁阻塞。

## 3. 目录索引维护规范（强制）

仓库内的**每一个目录 / Go package / 前端功能目录**都必须维护一份 `README.md`，作为结构化索引：

- **必须包含的内容**：
  1. 1~2 句话说明该目录 / 包的职责边界；
  2. 该目录下每个子目录与代码/文档文件的**一句话描述**。
- **防腐化铁律**：凡新增、删除、重命名文件，或显著改变某文件职责的改动，**必须在同一次变更（同一次 commit）中同步更新对应目录的 `README.md`**。不得遗留已删除文件条目，也不得遗漏新建文件。

## 4. 注释规范（强制）

- **严禁复述代码**：禁止编写任何无意义的代码翻译式注释（例如 `// increment counter`、`// unmarshal json`、`// return error if failed`、`// 获取用户信息`）。
- **仅在必要处写简短注释**：只有当代码结构本身无法直接表达以下信息时，才允许补充 1~2 行精炼注释：
  - **Why**：反直觉设计取舍的原因；
  - **不变量 / 前置条件**：并发锁顺序、状态机约束、缓存键一致性要求；
  - **外部协议坑**：第三方 API（NapCat / Gemini / B站等）的未文档化行为或容错边界。
- 导出符号仅在语义非自明时写简洁 GoDoc，不为了满足形式审查而写废话注释。

## 5. Go 代码规范（现代写法）

项目使用现代 Go（Go 1.24+ / 当前工具链 Go 1.27）：

- **现代标准库与语法**：
  - 使用 `any` 替代 `interface{}`；
  - 优先使用标准库 `slices`、`maps`、`cmp`、`iter` 与 `for i := range n`；
  - 日志统一使用 `log/slog` 结构化日志；
  - HTTP 路由优先使用标准库 `net/http` 增强模式（如 `mux.HandleFunc("GET /api/...", ...)`）；
  - 错误处理使用 `fmt.Errorf("...: %w", err)`、`errors.Is` / `errors.As` / `errors.Join`；严禁忽略 error 或静默吞掉关键异常。
- **接口与架构纪律**：
  - 接口定义在使用侧（consumer-side），保持 1~3 个方法的极小接口，避免提前抽象；
  - 所有网络、数据库、子进程与 LLM 调用必须显式透传 `context.Context` 并支持超时与取消；
  - 测试使用 `t.Context()`、`t.TempDir()` 与表驱动测试（table-driven tests）。
- **并发与存储**：
  - 严禁持有 `sync.Mutex` / `sync.RWMutex` 跨越网络请求或 LLM 调用；
  - SQLite 统一开启 WAL 模式与合理 `busy_timeout`，明确分离高频读路径与离线后台写路径。
- **提交前质量门禁**（Go 代码存在时必须通过）：
  - `gofmt -w .`
  - `go vet ./...`
  - `go test -race ./...`

## 6. Svelte 5 前端规范（现代写法）

WebUI 使用 **Svelte 5 + SvelteKit（`adapter-static` SPA）+ TypeScript（`strict: true`）+ Tailwind CSS v4 + shadcn-svelte**：

- **强制 Runes 与 Snippets**：
  - 状态与派生统一使用 `$state`、`$state.raw`、`$derived`、`$derived.by`；
  - 组件接口使用 `$props()`、`$bindable()` 与回调函数 props；
  - 内容分发使用 `{#snippet ...}` 与 `{@render ...}`。
- **严禁 Svelte 3/4 遗留语法**：
  - 禁止 `export let` 声明 props；
  - 禁止 `$:` 响应式标签；
  - 禁止 `createEventDispatcher` 与 `on:click` 旧事件指令（统一使用原生属性 `onclick={...}`）；
  - 禁止 `<slot>`。
- **响应式纪律**：
  - 能用 `$derived` 表达的计算状态，严禁使用 `$effect` 手动同步；
  - `$effect` 仅用于与外部世界同步（DOM API、定时器、WebSocket / SSE 订阅等），且必须返回清理函数；
  - 前端只负责可视化观测、调试与配置交互，不承载核心业务决策逻辑。
- **视觉与验收铁律**：
  - 面向娱乐 Bot 的活泼清新配色，深度覆写 `shadcn-svelte` 默认样式；
  - **严禁「边框套边框」**，统一用色差/明度对比与留白表达层级；
  - 文字保持克制（少用冗余副标题和说明段落），善用动效与排版直觉表达逻辑；
  - 页面开发完成后必须真实运行并截图验收，确保整齐无挤压换行与错位。

## 7. Git 规范

- **提交信息格式**：遵循 Conventional Commits：
  `<type>(<scope>): <concise summary>`
  - 允许的 `type`：`feat`、`fix`、`docs`、`refactor`、`perf`、`test`、`chore`、`build`；
  - 摘要简明扼要，说明改动意图而非罗列文件名。
- **原子提交**：单次 commit 只做一件完整的事（文档、重构、功能、测试边界清晰）；目录文件变动必须与对应的 `README.md` 索引更新在同一次 commit 中提交。
- **严禁入库内容**：禁止提交任何 API Key、Token、本地 `.env` / `config.local.*`、SQLite 运行库文件、日志与媒体缓存。
- **历史卫生**：不得对已推送的共享分支做破坏性 `push --force`；工作树保持干净，临时脚本不得遗留提交。
