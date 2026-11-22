# web/src/lib/components/ui

统一设计系统的 Svelte 5 基础 UI 原语库，基于 `bits-ui` 无障碍原语深度定制，严格零边框嵌套。

## 文件索引

- `Badge.svelte` — 柔和色块状态徽章，支持圆点与脉冲动效。
- `Button.svelte` — 弹性按压交互按钮与链接，支持 `primary`/`tonal`/`ghost`/`danger` 与加载态。
- `Dialog.svelte` — 基于 `bits-ui` Dialog 的磨砂背景居中弹窗。
- `EmptyState.svelte` — 极简克制的空状态占位卡，支持主题色图标泡与单步操作。
- `Input.svelte` — 凹陷色阶单行输入框，支持前后置图标片段与柔和聚焦光晕。
- `PageHeader.svelte` — 紧凑页头组件，组合标题、状态胶囊与右侧操作区。
- `SurfaceCard.svelte` — 多色阶 Tonal Surface 容器卡片（`raised`/`subtle`/`tint-*`）。
- `Switch.svelte` — 基于 `bits-ui` Switch 的弹性胶囊开关。
- `Tabs.svelte` — 基于 `bits-ui` Tabs 的凹陷滑槽 + 白色浮起标签切换器。
- `Textarea.svelte` — 凹陷色阶多行文本输入域。
- `index.ts` — UI 原语组件与类型统一导出入口。
- `utils.ts` — `clsx` 与 `tailwind-merge` 类名合并辅助函数 `cn`。
