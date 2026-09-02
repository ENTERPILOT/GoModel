<script>
  // Usage Analytics page. Route sub "costs" selects the costs mode
  // (deep link /admin/dashboard/usage/costs).
  import DatePicker from "$lib/components/molecules/DatePicker.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { router } from "$lib/stores/router.svelte.js";
  import { liveLogs } from "../audit-logs/liveLogs.svelte.js";
  import { usagePage } from "./usage.svelte.js";
  import FacetFilters from "./FacetFilters.svelte";
  import UsageStatCards from "./UsageStatCards.svelte";
  import UsageBreakdownChart from "./UsageBreakdownChart.svelte";
  import UsageLog from "./UsageLog.svelte";
  import * as m from "$lib/paraglide/messages.js";

  const PAGE = "usage";

  // Re-fetch when the page becomes active or the API key changes. Live
  // request-log previews stream in via the shared liveLogs singleton
  // (ensureLiveLogs is a no-op when the stream is already running).
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) {
      usagePage.fetchUsagePage();
      liveLogs.ensureLiveLogs();
    }
  });

  // Keep the mode in sync with the route (deep links, back/forward).
  $effect(() => {
    if (router.page === PAGE) {
      usagePage.usageMode = router.sub === "costs" ? "costs" : "tokens";
    }
  });
</script>

<div class="page-with-sticky-date">
  <div class="page-header date-range-page-header">
    <h2>{m.usage_title()}</h2>
  </div>
  <!-- The mode toggle lives in the sticky bar so it stays reachable while
       scrolling, like the date range it changes the meaning of. -->
  <div class="sticky-date-range usage-sticky-controls">
    <SegmentedControl
      ariaLabel={m.usage_mode_label()}
      options={[
        { value: "tokens", label: m.overview_tokens() },
        { value: "costs", label: m.overview_costs() },
      ]}
      value={usagePage.usageMode}
      onchange={(mode) => usagePage.toggleUsageMode(mode)}
    />
    <DatePicker onchange={() => usagePage.fetchUsagePage()} />
  </div>

  <!-- Page-level data filters: every widget below follows them -->
  <FacetFilters />

  <!-- Period totals for the active filters -->
  <UsageStatCards />

  <!-- Usage charts: two per row, a chart alone in its row stretches to full width -->
  <div class="usage-charts-grid">
    <UsageBreakdownChart kind="model" />
    <UsageBreakdownChart kind="userPath" />
    <UsageBreakdownChart kind="label" />
  </div>

  <!-- Session cardinality and identifiers need the full page width. -->
  <div class="usage-session-breakdown-wrap">
    <UsageBreakdownChart kind="session" />
  </div>

  <UsageLog />
</div>

<style>
  /* Usage page: the Tokens/Costs toggle shares the sticky bar with the date
     picker so both stay reachable while scrolling. */
  .usage-sticky-controls {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    flex-wrap: wrap;
    gap: 12px;
  }

  /* Usage Mode Toggle */
  /* Usage charts flow two per row; a section alone in its row (odd count,
     or neighbors hidden by x-show) grows to fill the full width. */
  .usage-charts-grid {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-stack);
    margin-bottom: var(--space-stack);
  }

  .usage-charts-grid :global(.model-chart-section) {
    flex: 1 1 calc(50% - 24px);
    min-width: 420px;
    margin-bottom: 0;
  }

  .usage-session-breakdown-wrap {
    margin-bottom: var(--space-stack);
  }

  .usage-session-breakdown-wrap :global(.session-usage-breakdown) {
    width: 100%;
    margin-bottom: 0;
  }

  /* Mobile: one chart per row. Two 130px-wide charts leave no room for a
     title, the view toggle, or the axis labels. */
  @media (max-width: 768px) {
    .usage-charts-grid :global(.model-chart-section) {
        flex: 1 1 100%;
        min-width: 0;
      }
  }
</style>
