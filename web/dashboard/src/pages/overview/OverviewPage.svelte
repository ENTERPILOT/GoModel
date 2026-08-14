<script>
  // Overview page: live token throughput, usage summary over the shared
  // reporting window, cache meter, usage chart, activity calendar, audit
  // status/latency charts, and the providers overview grid.
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import { untrack } from "svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { usageData } from "$lib/stores/usageData.svelte.js";
  import DatePicker from "$lib/components/molecules/DatePicker.svelte";
  import LiveTokens from "./LiveTokens.svelte";
  import SummaryCards from "./SummaryCards.svelte";
  import CacheMeter from "./CacheMeter.svelte";
  import UsageChart from "./UsageChart.svelte";
  import ContributionCalendar from "./ContributionCalendar.svelte";
  import AuditStatsCharts from "./AuditStatsCharts.svelte";
  import ProviderStatusSection from "./ProviderStatusSection.svelte";
  import {
    providerStatusState,
    auditStatsState,
    mcpServersState,
    calendarState,
  } from "./overviewState.svelte.js";
  import { liveTokensState } from "./liveTokensState.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  const PAGE = "overview";

  function loadPage() {
    usageData.fetchUsage();
    usageData.fetchCacheOverview("");
    auditStatsState.fetch();
    providerStatusState.fetch();
    mcpServersState.fetch();
    calendarState.fetch();
  }

  // A reporting-window or interval change refetches usage, the cache
  // overview, and (on the overview) the audit stats.
  function refetchWindow() {
    usageData.fetchUsage();
    usageData.fetchCacheOverview("");
    auditStatsState.fetch();
  }

  function onDateRangeChange() {
    refetchWindow();
    calendarState.fetch();
  }

  // Fetch when the page becomes active or the API key changes; stop the live
  // token engine (SSE + timers + buffers) and the provider-status poll loop
  // when navigating away.
  $effect(() => {
    void auth.refreshTick;
    if (router.page !== PAGE) return;
    untrack(() => {
      loadPage();
      liveTokensState.start();
    });
    return () => {
      liveTokensState.stop();
      providerStatusState.stopPolling();
    };
  });
</script>

<div class="page-with-sticky-date">
  <LiveTokens />

  <div class="page-header date-range-page-header">
    <h2>{m.overview_usage_title()}</h2>
  </div>
  <div class="sticky-date-range">
    <DatePicker onchange={onDateRangeChange} />
  </div>

  <AuthBanner />

  <SummaryCards />
  <CacheMeter />
  <UsageChart onintervalchange={refetchWindow} />
  <ContributionCalendar />
  <AuditStatsCharts />
  <ProviderStatusSection />
</div>
