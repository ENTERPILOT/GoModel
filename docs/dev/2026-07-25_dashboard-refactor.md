# Svelte dashboard refactor — findings

2026-07-25, `web/dashboard/`. Net: **65 files changed, +653 / −2587**, plus 14
new files. `svelte-check` 0 errors / 0 warnings, 330 tests green (was 317),
`npm run build` clean, dashboard visually verified against a live gateway
(Overview, Models, Audit Logs incl. an expanded entry, Usage, Settings).

## 1. Dead CSS

`src/styles/dashboard.css` went 2618 → 2400 lines. Removed, all verified
unreferenced (including via dynamically composed class names):

| Rule | Note |
|---|---|
| `[x-cloak]` | Alpine.js leftover; nothing emits the attribute |
| `.alert-success` | only `.alert-warning` is ever used |
| `.alias-checkbox`, `.alias-checkbox input` | |
| `.budget-bar-fill-period-{monthly,weekly,daily,hourly,custom}` | only `-usage`/`-danger` are produced |
| `.budget-help-notice` | |
| `.budget-override-dialog-backdrop`, `-shell` | z-index overrides for a dialog that uses `Modal` |
| `.inline-link` | |
| `.model-row-actions-empty` | |
| `.settings-panel-title-row` | only survived inside a media query |
| `.audit-tabpanel-enter{,-start,-end}` + its reduced-motion block | a tab transition that was never wired up |
| `.settings-guardrails-layout.is-editor-open`, `.workflows-layout.is-editor-open` | nothing sets `is-editor-open` |
| `@media (min-width: 769px) {}` | empty block |
| `.editor-modal-shell-wide` (in `Modal.svelte`) | two components carry comments saying the Modal atom has no wide variant — it doesn't |
| `.workflow-node-cache-miss` (in `WorkflowChart.svelte`) | `workflowCacheNodeClass()` never returns it |
| four dangling section comments in the workflow block | headings with no rules under them |

Why the compiler didn't catch these: Svelte prunes and warns about unused
scoped selectors, but only for classes it can see statically. Every rule above
either lived in the global stylesheet or sat in a component whose `class`
attribute is built from an expression, which makes the compiler conservative.
A one-off audit script (class extraction + dynamic-prefix detection) found
them; it is worth re-running after big UI changes.

## 2. Duplicated CSS

- `.workflow-section-head h4` was declared twice back-to-back (18px then
  14px) — merged to the effective values.
- `.pricing-recalculate-date-field .date-picker-trigger` was declared twice
  (`width` then `justify-content`) — merged, and moved into
  `PricingRecalculation.svelte` next to its sibling `:global` rules.
- `.audit-method-badge` was declared twice in `AuditEntryRow.svelte` — merged.
- `.provider-status-pill.is-{healthy,degraded,unhealthy}` and
  `.provider-status-health-state.is-{…}` were three identical pairs — merged
  into three shared rules.
- `.theme-icon` (global) and `.theme-btn :global(svg)` / `.theme-toggle-mobile
  :global(svg)` (scoped) both sized the same icons — dropped the scoped pair.
- `.col-price` / `.data-table th.col-price` **looked** duplicated but are not:
  `.data-table th` (0,1,1) outranks `.col-price` (0,1,0), so the `th` selector
  is what actually right-aligns headers. Merged into one commented rule
  instead of deleting.

## 3. A real bug the CSS work surfaced

`AuditPane`'s request/response direction arrows are supposed to be tinted
(accent for request, blue for response — there's a comment saying so). The
tints were global (`.audit-pane-icon-request`, specificity 0,1,0) while the
base rule was scoped (`.audit-pane-icon.svelte-hash`, 0,2,0) and came later,
so **both arrows rendered muted grey**. Confirmed in the previously committed
compiled CSS. Same latent risk on `.audit-pane-kind-*`.

Fixed by moving the modifiers into the owning component as compound `:global`
selectors (`.audit-pane-icon:global(.audit-pane-icon-request)`, 0,3,0), which
keeps them reachable for dynamically composed classes *and* winning over the
base. **This is a visible change** — the arrows are now coloured as intended.
The trap is now documented in `CONVENTIONS.md`.

## 4. Extracted components

| New | Replaces |
|---|---|
| `atoms/NoDataIllustration.svelte` | a 60-line hand-drawn SVG copy-pasted byte-identically into 3 pages |
| `molecules/FilterInput.svelte` | the search-icon + input block repeated in 11 pages (its CSS moved out of `dashboard.css` too) |
| `atoms/CopyButton.svelte` | the copy/check icon-swap button (2× in `AuditPane`, 1× in `AuthKeyEditor`, which had already diverged to lucide icons) |
| `audit-logs/AuditEntrySummary/AuditPaneTabs/AuditEntryMetadata.svelte` | `AuditEntryRow.svelte`, 622 → 71 lines |
| `workflows/WorkflowIdBadge.svelte` | the copy-the-workflow-id pill inside `WorkflowChart` |
| `molecules/DatePickerCalendar.svelte` + `datePickerLogic.js` | the month grid and its date math inside `DatePicker` (576 → 293) |

**Deleted `pages/models/TableIcon.svelte`** — a hand-rolled name→SVG switch for
six icons. The rest of the app already renders the same table actions as
`<Icon name="pencil" class="table-icon-svg" />` etc., so `models/` was the
odd one out (and its pencil/trash glyphs differed from the ones on API Keys
and MCP Servers). Now consistent: `x`, `trash-2`, `pencil`,
`circle-dollar-sign`, `gauge`, `shuffle`.

## 5. Simplified markup

- `WorkflowChart`'s nine near-identical node blocks collapsed into one
  `{#snippet node({icon, label, variant, state, sub, badge})}`; its ten inline
  SVGs became `<Icon>` (the file also lost the id-badge, 761 → 575).
- `ProviderStatusCard`'s eight repeated label/value rows collapsed into a
  `configRow` snippet; the chevron became `<Icon name="chevron-down">`.
- `Sidebar`'s three theme buttons and the mobile toggle's `if/else-if/else`
  icon chain collapsed into one `themes` array + `{#each}`.
- `AuditEntryMetadata`'s eleven conditional badges became one ordered,
  filtered array (render order preserved exactly).
- Two dead single-child wrappers removed: `.settings-guardrails-layout`
  (a `grid-template-columns: 1fr` around one `<section>`) and
  `.workflows-layout` (a `flex column gap:16px` around one child). Both also
  had a media query re-declaring the value they already had.

## 6. Deduplicated JS

An AST-ish scan for identical function bodies found 11 duplicate groups;
all are now zero.

- **`errorPayloadMessage`** — four byte-identical `{error:{message}}`
  extractors (`authKeyErrorMessage`, `guardrailErrorMessage`,
  `mcpErrorPayloadMessage`, `providerCredentialErrorMessage`) plus four
  near-identical test blocks. Now one function in the new
  `$lib/api/errors.js`, re-exported from `client.js`, with one test file.
  `errors.js` exists (rather than living in `client.js`) because `client.js`
  imports rune-based stores, which pure page logic and `node:test` can't load.
- **Chart style fragments** — `chartTickFont`, `chartTooltip`, and
  `resolveColor`/`resolveCssColor` existed in both `overview/chartStyle.js`
  and `usage/usage-chart-config.js`. Now in `$lib/utils/chartTheme.js`.
- **The 11-colour categorical palette** — three copies (`usage-helpers.js`,
  `authKeysLogic.js`, `chartStyle.js`), with `labelColor()`'s djb2 hash
  duplicated verbatim and a comment admitting it. Now one palette in
  `chartTheme.js`.
- **`browserStorage()`** — two copies; now `$lib/utils/storage.js`, which also
  gave `ui.svelte.js` `readStored`/`writeStored` in place of four hand-rolled
  try/catch blocks.
- **`formatDateParam`**, **`formatNumber`**, **`splitCommaList`** (was
  `normalizeMcpCommaList` / `normalizeProviderCredentialCommaList`),
  **`mcpServerStatus`**, **`qualifiedModelName`** — each collapsed to one
  definition.
- **`workflowAiConnClass` / `workflowResponseConnClass`** — identical bodies in
  the same file; merged into `workflowBypassedConnClass` with a comment
  explaining why both edges dim on a cache hit.
- **Debounce** — the same 6-line timer/clear/cleanup shape in three
  components; now `debounced(fn, delay)` in `$lib/utils/debounce.js`.

Gotcha worth remembering: `export { x as y } from "./m.js"` re-exports without
creating a local binding, so internal call sites break silently at runtime.
Six modules needed `import` + `export { … }` instead. Tests caught it.

## 7. New tests

+13 tests (317 → 330): `date-picker-logic.test.js`, `debounce.test.js`,
`api-errors.test.js`. The date-picker tests immediately caught a bug I had
introduced while extracting `calendarDays` — I wrote
`for (let d = 1; d <= 42 - days.length; d++)`, whose bound is re-evaluated
each iteration, so the grid came out at 38–40 cells instead of 42. Fixed.

## 8. Deliberately left alone

- **Light/dark token duplication** (`[data-theme="light"]` vs
  `@media (prefers-color-scheme: light) :root:not([data-theme="dark"])`, ~45
  duplicated lines). Both are load-bearing: `themeStore.apply()` *removes*
  `data-theme` in system mode. The obvious fix, `light-dark()`, would break
  `chartTheme.cssVar()`, which reads these variables back with
  `getComputedStyle` — an unregistered custom property computes to its token
  stream, so Chart.js would receive the literal `light-dark(…)` text. The
  file's existing "intentional duplication for now" TODO stands.
- **`WorkflowChart.svelte` is still 575 lines** (130 markup, the rest CSS).
  The bulk is a 5-state × 5-element status matrix. Collapsing it with custom
  properties either changes colours slightly (the mix percentages drift —
  18/16/14/14/12% for icon backgrounds) or needs a per-state token block that
  is less readable than what's there. Splitting is blocked too: the state
  classes are computed strings, so a child component's scoped rules would be
  pruned. Same reason `Sidebar` (435) and `ProviderStatusCard` (429) sit just
  over the 400-line guideline — the remainder is flat, commented CSS.
- **The per-page CRUD stores** (`mcpServers`, `guardrails`, `providersConfig`,
  `budgets`, `authKeys`) share a *shape* (fetch / openCreate / openEdit /
  closeForm / submitForm / delete) but almost no code: different endpoints,
  payload builders, validation and flash messages. A generic base class would
  be the premature abstraction CLAUDE.md warns about.

## 9. `.pagination-btn` → `.btn`

`.pagination-btn` was the app's **base button class**, used on ~37 elements
that have nothing to do with pagination — every primary action read
`class="pagination-btn pagination-btn-primary pagination-btn-with-icon"`.
Renamed across 36 files (157 occurrences) to `.btn`, `.btn-primary`,
`.btn-danger`, `.btn-danger-outline`, `.btn-with-icon`. `.pagination` (the
container) is unchanged, and the rename is token-exact — no other class
contains the substring `pagination-btn`, and `.btn-danger` does not collide
with the pre-existing `.table-action-btn-danger` (different class token).

I did *not* wrap the button in a component: the call sites vary a lot
(`disabled`, `title`, `aria-label`, differing icons, conditional labels,
`&nbsp;` in text), so a component would need ~8 props and read worse than the
plain `<button>` plus utility classes.
