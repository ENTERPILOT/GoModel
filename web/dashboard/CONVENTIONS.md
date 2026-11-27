# GoModel dashboard — Svelte conventions

The admin dashboard is a Svelte 5 SPA in `web/dashboard/`. Every page MUST
follow these rules so the pages compose into one coherent app.

## Golden rules

1. **Preserve the admin API contract.** Keep every admin API endpoint,
   request/response field, query parameter, and behavioral rule exactly as
   the Go backend implements it — `internal/admin/*.go` is the source of
   truth. Do not invent endpoints or payload fields.
2. **CSS lives with its owner.** Component-specific styles are scoped
   `<style>` blocks in the owning component; the shared design system
   (tokens, reset, typography, buttons, forms, tables, alerts, layout,
   keyframes) plus rules that target child-component DOM — a class passed
   as a prop into `Icon` or `LoadingState` renders in the child's markup,
   where the parent's scope hash cannot match — lives in `src/styles/`.
   `dashboard.css` is only the entry point: it `@import`s the modules in
   cascade order, so append new rules to the module that owns them and
   never reorder the imports. No CSS preprocessor, on purpose: scoped
   styles, custom properties, and native nesting cover the need with zero
   extra dependencies. Reuse existing shared class names for standard
   elements.
3. **Mind the scope hash when moving CSS.** Scoping adds specificity, and it
   cuts both ways:
   - Moving a rule *out* of dashboard.css can make it win ties it used to
     lose. Move its state/media/theme variants with it and check the shipped
     computed style is unchanged.
   - A global modifier (`.x-request`) silently *loses* to the scoped base
     (`.x.svelte-hash`) it is meant to override. If the modifier is composed
     dynamically, keep it in the owning component as a compound `:global`
     (`.x:global(.x-request)`) so it still outranks the base.
   - The compiler prunes selectors it cannot see in the markup, so a
     component whose state classes are computed strings (`WorkflowChart`,
     `ProviderStatusCard`) cannot hand that CSS to a child component.
4. **Svelte 5 runes only.** `$state`, `$derived`, `$effect`, `$props`,
   snippets/`{@render}`. No legacy `$:` reactivity, no svelte/store.
5. **Files:** page code lives in `src/pages/<page>/`. The entry component is
   `<Pascal>Page.svelte`. Split big pages into sub-components in the same
   directory (atomic design: compose from
   `$lib/components/atoms|molecules|organisms`). Keep files under ~400 lines
   where practical.
6. **Shared foundation code lives in `src/lib/`** (plus `src/App.svelte`) —
   changes there affect every page, so keep them deliberate. Page-specific
   helpers belong in the page directory.

## Foundation — use it, don't re-implement

Imports use the `$lib` alias. **Each module documents its own exports and
props in its header comment** — read those for the details; this list exists
so you know what already exists.

### HTTP: `$lib/api/client.js`

```js
const result = await getJSON("/admin/foo?x=1", { label: "foo", signal });
// result = { ok, stale, status, data, res }
const saved = await sendJSON("/admin/foo", "POST", payload, { label: "save foo" });
```

- `result.stale === true` → the response belongs to an old API key: **return
  without touching your state**, or it clobbers fresh data.
- 401s are handled globally (the auth dialog opens); `result.ok` is `false`.
- `errorMessage(result, fallback)` reads a result envelope;
  `errorPayloadMessage(data, fallback)` reads a raw `{error:{message}}` body.
  Both really live in `$lib/api/errors.js`, which imports no Svelte runtime —
  pure page logic and its `node:test` suite import them from there directly.
- `apiFetch(path, options)` is the raw escape hatch (SSE, blobs); it adds auth
  + timezone headers and the base path. Never call `fetch` on `/admin/...`.
- **CRUD stores use `$lib/api/adminCrud.js`**: `loadAdminList` /
  `sendAdminMutation` reduce a request to an outcome object with the guard
  ladder applied in the one correct order (stale first, then
  unavailable-503, then errors; 401 loads stay silent). Apply the outcome
  to your `$state` fields instead of re-implementing the branches.

### Stores (`$lib/stores/*.svelte.js`) — all singletons

`auth` · `router` · `themeStore` (bump `tick` to rebuild charts) · `sidebar` ·
`modals` (owned by the `Modal` atom — don't touch) · `timezone` ·
`runtimeConfig` (feature-flag visibility) · `modelsStore` · `dateRange`
(shared reporting window) · `usageData` · `flash` · `confirmDialog` (typed
confirmations).

### Components

- **atoms** — `Icon` (kebab-case lucide names), `Spinner`, `Modal`,
  `EmptyState`, `NoDataIllustration`, `CopyButton`, `TableActionButton`,
  `DialogCloseButton`, `SegmentedControl`, `EnabledToggle`, `GoModelLogo`.
- **molecules** — `LoadingState`, `Pagination`, `DatePicker`, `FilterInput`,
  `SearchSelect` (searchable single-value dropdown; use it instead of a
  `<datalist>` or a long `<select>`), `InlineHelpSection`, `ChartCanvas`,
  `DemoModeBanner`, `FormField`.
- **organisms** — `AuthBanner`, `AuthDialog`, `Sidebar` (nav items in
  `navigation.js`), `ThemeToggle`, `FlashMessages`,
  `TypedConfirmationDialog`, `EditorDialog`.

`Modal` handles Escape/backdrop/scroll-lock and autofocuses
`[data-modal-autofocus]`. `ChartCanvas` runs its `build()` inside an effect,
so reactive reads are tracked and theme changes rebuild automatically.
**Every create/edit modal composes `EditorDialog`** (shell + header + error
banner + actions + the Escape-under-auth-dialog guard) with `FormField`
cells inside — never hand-roll that shell again.

### Utils

`format.js` (numbers, costs, tokens, UTC dates and date params, comma lists,
provider/model display) · `dateKeys.js` (UTC "YYYY-MM-DD" day-key math shared
by the timezone store, the reporting window and the contribution calendar) ·
`chartTheme.js` (theme colors + the shared Chart.js style fragments) ·
`clipboard.svelte.js` · `debounce.js` · `storage.js` (localStorage can be
absent or blocked — never touch it directly) · `api/paths.js` (`gomodelPath`) ·
`attachments.js` (`{@attach ...}` behaviours: `dismissOnOutside`,
`autofocusWithin`).

For DOM behaviour tied to one element, prefer an attachment over `bind:this` +
`$effect`: the element arrives as the argument, the returned teardown runs with
the element, and a falsy expression (`{@attach open ? x(…) : undefined}`)
switches it off.

## 设计 token

视觉规范的事实源是 [`DESIGN.md`](./DESIGN.md)。本节只写**编码约定**。

`src/styles/themes.css` 是唯一调色板，分两层：

```
第 1 层  PALETTE   Arco 官方色阶，每族 10 级，深浅各一套（深色方向反转）
第 2 层  SEMANTIC  扁平语义名，指向第 1 层
```

规矩：

1. **组件里不写死颜色。** 任何前景 / 背景 / 描边都要落到 `themes.css` 的语义 token 上。需要新颜色时，先在 `themes.css` 加 token（调色板或语义层），再用它。唯一例外是 `color-mix()` 里参与混合的比例色（`#fff` / `#000`）。
2. **先挑对的层。** 填充 / 描边 / 图标用 `--primary`（`--success` / `--warning` / `--danger` 同理）；**文字**用 `--primary-text` / `--success-text` / `--warning-text` / `--danger-text`。浅色主题下三个状态色本身已是文字安全级，深色主题下 level-6 在深底上也已达标 —— 但"压在色块上的文字"另有专用的 `--on-fill`。
3. **文字压在色块上**用 `--on-fill`（饱和底，如主按钮 / danger 按钮）、`--on-dark-fill`（深色实底，如预算周期条）、`--meter-label`（仪表条切片）；**开关旋钮**用 `--switch-thumb`。这四个 token 都不是主题色，别用 `--text` 顶替。
4. **分类编码**（工作流"当前执行"节点、provider scope 色相）用 `--category-1`，不要用 `--primary` —— 两套编码必须异色。
5. **6 个 `--chart-*` token 必须是字面量颜色**（`--chart-grid`、`--chart-text`、`--chart-day-marker`、`--chart-tooltip-bg`、`--chart-tooltip-border`、`--chart-tooltip-text`）。`chartTheme.js` 的 `cssVar()` 返回原始文本并直接交给 Chart.js，写成 `var(--text)` 会让 Canvas 静默丢弃该颜色。
6. **`data-theme` 永远存在。** `themeStore.apply()` 把 `system` 解析成具体的 `light` / `dark` 后写入 `<html data-theme>`，`public/theme-init.js`（首屏外部脚本，index.html 在样式表之前同步引入）做同样的事。因此样式表里**不要**再写 `@media (prefers-color-scheme: ...)` —— 浅色调色板只定义一次。首屏主题逻辑不要写回内联 `<script>`：网关 CSP 只 hash-放行 Go 注入的 runtime globals，内联脚本会在真实实例上被拦。
7. **焦点环用共享 token，不要自己写 alpha。** 全局 `:focus-visible` 兜底在 `base.css`，输入类用 `:focus`（`forms.css`），按钮用 `box-shadow`（`buttons.css`）。环的颜色只有一个来源：`--primary-ring`（填充按钮同样用它 —— 零偏移 box-shadow 画在边框盒外侧，环像素落在页面 / 卡片 / 浮层上，primary-8 对这些底全部 ≥3:1）。自定义环一律改用 `--primary-ring`，不要写 `color-mix(... 28%, transparent)` 这类比例：透明环实测只有 1–2:1，等于没有环。
8. **交互三态用 token。** 填充按钮的悬停 / 按下用 `--primary-hover` / `--primary-active`（不是自己 mix 出来的颜色）；禁用态用 `--disabled-opacity` + `--text-disabled` + `not-allowed` 光标，不要各写各的 opacity。
9. **改动 token 要跑守卫测试**：`tests/themeTokens.test.js` 会按 DESIGN.md §6 的组合表算对比度（含焦点环的 3:1 非文本门槛），也会检查第 5、6 条。新增颜色时同步更新该表的期望值。
10. **圆角只用四个 token**（`themes.css`）：`--radius-control`（2px，控件）、`--radius-panel`（4px，面板）、`--radius-pill`（999px，胶囊）、`--radius-circle`（50%，正圆）。不要写 `border-radius: 6px` 这类一次性值——`tests/designGrid.test.js` 会直接报错。判断档位看形状：按钮、输入、标签、图标按钮、菜单项是控件；卡片、弹窗、浮层、下拉、表格容器是面板；状态徽章、计数胶囊是胶囊；状态点、加载圈、开关旋钮是正圆。
11. **间距走 4px 栅格**：`padding` / `margin` / `gap` 的长度取 4 / 8 / 12 / 16 / 20 / 24 / 32。两种例外是**有意**的：1px / 2px 的光学微调（图标居中、描边重叠），以及 Arco 自身的两个组件度量（按钮横向内边距 15px、表格单元格纵向内边距 9px，见 DESIGN.md §7 #12）。改这两个值前先读 DESIGN.md §5.2。
12. **字体只用两个 token**（`themes.css`）：正文 `var(--font-sans)`（Arco 正文字栈，含 PingFang SC / Hiragino Sans GB / Microsoft YaHei，中文不再回退到随机系统字体）、等宽 `var(--font-mono)`。组件里不要写新的 `font-family` 字栈——继承（默认）或引用 token 即可，`tests/fonts.test.js` 会拒绝硬编码。唯一例外是 Canvas 字体字符串（`chartTheme.js`、`liveTokensLogic.js`）：Canvas 读不到 `var()`，改 `--font-mono` 时必须同步改这两处（测试会比对）。
13. **不做 uppercase**：任何规则都不要写 `text-transform: uppercase`（Arco 全库只有 `none`，标签层级靠字重与颜色表达）。`tests/fonts.test.js` 会直接报错。

## Page skeleton

```svelte
<script>
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";

  const PAGE = "budgets"; // this page's route id

  async function loadPage() { /* fetch everything this page needs */ }

  // Re-fetch when the page becomes active or the API key / timezone changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) loadPage();
  });
</script>
```

Cross-page conventions:

- Abortable fetches: cancel the in-flight request when a newer one starts
  (AbortController); ignore abort errors.
- `history.pushState` deep links (e.g. request log filters in the query string):
  keep the established URL shapes; use `gomodelPath()`.
- Timestamps shown to users: `timezone.formatTimestamp(ts)`;
  `title` tooltips use `formatTimestampUTC(ts)`.
- Confirmation prompts: simple ones use `confirm()`; typed confirmations use
  `confirmDialog`.

## Tests

Pure logic (formatting, reducers, query building) lives in plain `.js` files
in the page directory and is tested in `web/dashboard/tests/<name>.test.js`
using `node:test` + `assert` (ESM imports; no DOM).

Those `.js` files must use **relative** imports, not `$lib` — node runs them
without Vite, so the alias does not resolve. That also means they cannot
import a `.svelte.js` store (runes need the compiler); keep shared helpers
they need in a plain module.

## Verification

Run from `web/dashboard/`:

```sh
npm run check   # svelte-check: zero errors required (warnings OK)
npm test        # compile messages + node --test tests/*.test.js
npm run build   # keeps the embedded dist/ in sync — CI enforces drift
```
