<script>
  // Overview page: live token throughput, the first-run checklist, usage
  // summary over the shared reporting window, cache meter, usage chart,
  // activity calendar, request status/latency charts, and the providers
  // overview grid.
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import { untrack } from "svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { access } from "$lib/stores/access.svelte.js";
  import { usageData } from "$lib/stores/usageData.svelte.js";
  import DatePicker from "$lib/components/molecules/DatePicker.svelte";
  import LiveTokens from "./LiveTokens.svelte";
  import SummaryCards from "./SummaryCards.svelte";
  import CacheMeter from "./CacheMeter.svelte";
  import UsageChart from "./UsageChart.svelte";
  import ContributionCalendar from "./ContributionCalendar.svelte";
  import RequestStatsCharts from "./RequestStatsCharts.svelte";
  import ProviderStatusSection from "./ProviderStatusSection.svelte";
  import GettingStarted from "./GettingStarted.svelte";
  import {
    providerStatusState,
    requestStatsState,
    mcpServersState,
    calendarState,
  } from "./overviewState.svelte.js";
  import { liveTokensState } from "./liveTokensState.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  const PAGE = "overview";

  // Gateway-wide widgets (cache overview, provider and MCP status, live
  // throughput) are skipped for a key scoped to a user path: their
  // endpoints answer 403 for such credentials.
  const scoped = $derived(access.scoped);

  function loadPage() {
    // The checklist judges this visit (and this key) by its own answers, not
    // by what the previous one left in the shared state.
    providerStatusState.loadSucceeded = false;
    calendarState.loadSucceeded = false;
    usageData.fetchUsage();
    requestStatsState.fetch();
    calendarState.fetch();
    if (scoped) return;
    usageData.fetchCacheOverview("");
    providerStatusState.fetch();
    mcpServersState.fetch();
  }

  // A reporting-window or interval change refetches usage, the cache
  // overview, and (on the overview) the request stats.
  function refetchWindow() {
    usageData.fetchUsage();
    requestStatsState.fetch();
    if (scoped) return;
    usageData.fetchCacheOverview("");
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
    // The access scope decides which widgets may fetch, so wait for it; the
    // effect re-runs once it lands (or lands again under a new key).
    if (!access.loaded) return;
    untrack(() => {
      loadPage();
      if (!scoped) liveTokensState.start();
    });
    return () => {
      liveTokensState.stop();
      providerStatusState.stopPolling();
    };
  });
</script>

<div class="page-with-sticky-date">
  {#if !scoped}
    <LiveTokens />
  {/if}

  <div class="page-header date-range-page-header">
    <h2>{m.overview_usage_title()}</h2>
  </div>
  <div class="sticky-date-range">
    <DatePicker onchange={onDateRangeChange} />
  </div>

  <AuthBanner />

  {#if !scoped}
    <GettingStarted />
  {/if}
  <SummaryCards />
  {#if !scoped}
    <CacheMeter />
  {/if}
  <UsageChart onintervalchange={refetchWindow} />
  <ContributionCalendar />
  <RequestStatsCharts />
  {#if !scoped}
    <ProviderStatusSection />
  {/if}
</div>
