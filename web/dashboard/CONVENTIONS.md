# GoModel dashboard — Svelte conventions

The admin dashboard is a Svelte 5 SPA in `web/dashboard/`. Every page MUST
follow these rules so the pages compose into one coherent app.

## Golden rules

1. **Preserve the admin API contract.** Keep every admin API endpoint,
   request/response field, query parameter, and behavioral rule exactly as
   the Go backend implements it — `internal/admin/*.go` is the source of
   truth. Do not invent endpoints or payload fields.
2. **CSS lives with its owner.** Component-specific styles are scoped
   `<style>` blocks in the owning component; `src/styles/dashboard.css`
   holds only the shared design system (tokens, reset, typography, buttons,
   forms, tables, alerts, layout, keyframes) plus rules that target
   child-component DOM (e.g. a class passed as a prop into `Icon` or
   `LoadingState` renders in the child's markup, where the parent's scope
   hash cannot match — those must stay global). Reuse existing shared class
   names for standard elements. Cascade gotcha: scoping adds specificity,
   so when moving a rule out of dashboard.css move its state/media/theme
   variants with it, and check the shipped computed style doesn't change —
   a scoped rule can win ties it used to lose.
3. **Svelte 5 runes only.** `$state`, `$derived`, `$effect`, `$props`,
   snippets/`{@render}`. No legacy `$:` reactivity, no svelte/store.
4. **Files:** page code lives in `src/pages/<page>/`. The entry component is
   `<Pascal>Page.svelte`. Split big pages into sub-components in the same
   directory (atomic design: compose from
   `$lib/components/atoms|molecules|organisms`). Keep files under ~400 lines
   where practical.
5. **Shared foundation code lives in `src/lib/`** (plus `src/App.svelte`) —
   changes there affect every page, so keep them deliberate and consistent
   with the foundation API below. Page-specific helpers belong in the page
   directory.

## Foundation API (already implemented — use it, don't re-implement)

Imports use the `$lib` alias.

### HTTP: `$lib/api/client.js`

```js
import { getJSON, sendJSON, apiFetch, errorMessage, isAbortError } from "$lib/api/client.js";

const result = await getJSON("/admin/foo?x=1", { label: "foo", signal });
// result = { ok, stale, status, data, res }
// - 401s are handled globally (auth dialog opens). result.ok === false.
// - result.stale === true → response belongs to an old API key: return
//   WITHOUT touching your state (a stale response must not clobber fresh data).
// - Non-OK: result.data holds the parsed error payload when present.
const saved = await sendJSON("/admin/foo", "POST", payload, { label: "save foo" });
const msg = errorMessage(saved, "Failed to save."); // extracts payload message
```

`apiFetch(path, options)` is the raw escape hatch (SSE streams, blobs); it
adds auth + timezone headers and the base path. Never call `fetch` directly
with `/admin/...` paths.

### Stores (`$lib/stores/*.svelte.js`) — all singletons

- `auth` — `apiKey, needsAuth, hasApiKey(), openDialog(), refreshTick`.
  Pages re-fetch when `auth.refreshTick` changes (see Page skeleton).
- `router` — `page`, `sub`, `navigate(page, sub?)`.
- `themeStore` — `theme`, `tick` (increment = rebuild charts).
- `sidebar`, `modals` (used by Modal atom; don't touch directly).
- `timezone` — `effectiveTimezone()`, `formatTimestamp(ts)` (effective TZ),
  `currentDateKey()`, `dateKeyToDate/dateToDateKey/addDaysToDateKey`,
  `ensureOptions()`, `options`, `override`, `saveOverride()/clearOverride()`.
- `runtimeConfig` — `ensureLoaded()`, `booleanFlag(name, def)`,
  `cacheVisible()`, `auditVisible()`, `usageVisible()`, `budgetsVisible()`,
  `rateLimitsVisible()`, `guardrailsVisible()`, `mcpVisible()`,
  `liveLogsVisible()`, `flag(name)`.
- `modelsStore` — `models`, `categories`, `filteredModels`, `filter`,
  `activeCategory`, `loading`, `fetchModels()`, `fetchCategories()`,
  `selectCategory(cat)`, `categoryCount(cat)`.
- `dateRange` — shared reporting window: `days`, `selectedPreset`,
  `customStartDate/customEndDate`, `interval`, `queryStr()`,
  `dateRangeLabel()`, `chartTitle()`.
- `usageData` — `summary`, `daily`, `cacheOverview`, `fetchUsage()`,
  `fetchCacheOverview(extraQuery)`, `emptyUsageSummary()`,
  `emptyCacheOverview()` (named exports), `cacheAnalyticsEnabled()`.
- `confirmDialog` — typed confirmation:
  `confirmDialog.open({ title, message, requiredText, confirmLabel, icon, onConfirm: async () => { ...; confirmDialog.close(); } })`.

### Components

- `$lib/components/atoms/Icon.svelte` — `<Icon name="chart-column" class="nav-icon" />`
  (kebab-case lucide names).
- `$lib/components/atoms/Spinner.svelte` — `<Spinner size={16} label="Loading models" />`.
  Use it for loading states; prefer a visible loading affordance over a
  silent load.
- `$lib/components/atoms/Modal.svelte` —
  `<Modal open={x} variant="editor|auth" onclose={...}> <section class="model-editor" role="dialog" aria-modal="true">…</section> </Modal>`.
  Handles Escape/backdrop/scroll-lock/autofocus (`data-modal-autofocus` attr).
  `variant="editor"` renders `editor-modal-*` classes (model editors),
  `"auth"` renders `auth-dialog-*` (small centered dialogs).
- `$lib/components/atoms/EmptyState.svelte` — `<EmptyState icon="inbox" title="No data" hint="..." />`.
- `$lib/components/atoms/TableActionButton.svelte` — small table/card action
  button (`.table-action-btn`): `label` fills both title and aria-label,
  children supply the icon (and optional `.budget-action-label` span);
  optional extra `class` (state classes like `is-set`) and `disabled`.
  Only hand-roll the `<button>` when title and aria-label must differ.
- `$lib/components/atoms/DialogCloseButton.svelte` — the standard editor/drawer
  X button: `<DialogCloseButton label="Close budget editor" onclick={...} />`.
  Optional `class` (e.g. `auth-dialog-close`), `disabled`, `bind:el`, and
  `iconClass=""` for the larger auth-dialog-style X.
- `$lib/components/molecules/LoadingState.svelte` —
  `<LoadingState label="Loading budgets..." />` (the `.loading-state` row
  with the CSS spinner; optional extra `class`).
- `$lib/components/organisms/AuthBanner.svelte` — `<AuthBanner />`; the
  "Authentication required" page banner. Owns its own `auth.authError` gate.
- `$lib/components/atoms/SegmentedControl.svelte` — exclusive-option button group:
  `<SegmentedControl ariaLabel="Usage mode" options={[{value, label}, ...]} value={mode} onchange={(v) => ...} />`
  (uses the `segmented-control`/`segmented-btn` classes; the standard markup
  for mode toggles and interval pickers).
- `$lib/components/molecules/Pagination.svelte` —
  `<Pagination total={log.total} offset={log.offset} limit={log.limit} onprev={...} onnext={...} />`.
- `$lib/components/molecules/DatePicker.svelte` — `<DatePicker onchange={() => refetch()} />`
  (binds the shared `dateRange` store).
- `$lib/components/molecules/InlineHelpSection.svelte` — title row with the
  "?" help toggle and collapsible copy below (the `inline-help-section`
  markup): `<InlineHelpSection copyId="x-help-copy" label="x help" text={...}>
  {#snippet title()}<h3>…</h3>{/snippet} </InlineHelpSection>`.
  aria reads "Show/Hide {label}". Rich copy goes in a `help` snippet instead
  of `text`; `extra` renders after the toggle (e.g. a spinner); `bind:open`
  for store-held state; `external` when the caller renders the copy elsewhere
  (give that element `id={copyId}`). The toggle hides when there is no copy.
- `$lib/components/molecules/ChartCanvas.svelte` — Chart.js wrapper:
  `<ChartCanvas build={() => makeConfig()} />`. `build` runs in an effect:
  reactive values read inside it are tracked, and the chart rebuilds on theme
  change automatically. Return `null` for "no chart". Import chart color
  tokens from `$lib/utils/chartTheme.js` (`chartColors()`, `cssVar(name)`).

### Utils

- `$lib/utils/format.js` — `formatNumber, formatCost, formatPrice,
  formatPriceFine, formatTokensShort, tokenCountTitle, formatDateUTC,
  formatTimestampUTC, providerTypeValue, providerDisplayValue,
  qualifiedModelDisplay, qualifiedModelValueDisplay,
  qualifiedResolvedModelDisplay, auditModelDisplay`.
- `$lib/utils/clipboard.svelte.js` — `writeTextToClipboard(v)`,
  `createCopyState()` (copy-button feedback state).

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
- `history.pushState` deep links (e.g. audit filters in the query string):
  keep the established URL shapes; use `gomodelPath()` from
  `$lib/api/paths.js`.
- Timestamps shown to users: `timezone.formatTimestamp(ts)`;
  `title` tooltips use `formatTimestampUTC(ts)`.
- Confirmation prompts: simple ones use `confirm()`; typed
  confirmations use `confirmDialog`.
- Do NOT add new npm dependencies.

## Verification

Run from `web/dashboard/`:

```sh
npx svelte-check --tsconfig ./jsconfig.json  # zero errors required (warnings OK)
```

Run `npm run build` before committing so the embedded `dist/` stays in sync
(CI enforces drift).

## Tests

Pure logic (formatting, reducers, query building) lives in plain `.js` files
in the page directory and is tested in `web/dashboard/tests/<name>.test.js`
using `node:test` + `assert` (ESM imports; no DOM).
Verify with `node --test tests/<name>.test.js`.
