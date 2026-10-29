// Usage page state: filtered summaries backing the stat cards, the
// per-model / per-user-path / per-label / per-request-type / per-session
// breakdowns, and the request log. The filters live in usageFilters, their
// dropdown choices in usageFacets, the breakdown in usagePivot, and the API
// and URL queries are built in usage-view.js. The shared cache overview lives
// in usageData (this page passes its filter query through fetchCacheOverview).

import { getJSON, isAbortError } from "$lib/api/client.js";
import { router } from "$lib/stores/router.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { usageData } from "$lib/stores/usageData.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { toggleSearchValue } from "$lib/components/molecules/searchSelectLogic.js";
import { liveLogs } from "../audit-logs/liveLogs.svelte.js";
import { emptySessionUsage, emptyUsageLog, emptyUsagePageSummary } from "./usage-helpers.js";
import {
  endpointsSelected,
  endpointUsageRows,
  foldEndpointUsageByType,
  toggleEndpointSelection,
} from "./usage-endpoints.js";
import { sessionUsageQueryParams, usageLogQueryParams } from "./usage-query.js";
import { syncUsageUrl, usageApiQuery, usageFilterQueryStr, usagePivotQuery } from "./usage-view.js";
import { usageFacets } from "./usageFacets.svelte.js";
import { usageFilters } from "./usageFilters.svelte.js";
import { usagePivot } from "./usagePivot.svelte.js";

class UsagePageState {
  usageMode = $state("tokens");

  // Page-level data filters: every widget on the page follows them, and the
  // live request-log merge reads them to pause previews while any is set.
  // Multi-value filters hold the selected values (empty = all): any value
  // matches within one filter, and every filter applies.
  filters = usageFilters;
  // Filtered summaries backing the stat cards, fetched in both cache modes:
  // uncached carries real provider spend (cached rows store the avoided
  // cost, which must not inflate the cost card), all carries the row count
  // matching the log's default view.
  usageSummary = $state(emptyUsagePageSummary());
  usageSummaryAll = $state(emptyUsagePageSummary());

  modelUsage = $state([]);
  userPathUsage = $state([]);
  labelUsage = $state([]);
  // Per-endpoint rows from /admin/usage/endpoints, shown folded by request
  // type or one row per endpoint.
  endpointUsage = $state([]);
  endpointUsageGrouping = $state("type");
  typeUsage = $derived(
    this.endpointUsageGrouping === "endpoint"
      ? endpointUsageRows(this.endpointUsage)
      : foldEndpointUsageByType(this.endpointUsage),
  );
  sessionUsage = $state(emptySessionUsage());

  // The request log is backed by liveLogs.usageLog — the live-merge source of
  // truth. Fetched pages are written into it and usage.* stream events merge
  // into it.
  get usageLog() {
    return liveLogs.usageLog;
  }
  set usageLog(value) {
    liveLogs.usageLog = value;
  }
  get usageLogSearch() {
    return liveLogs.usageLogSearch;
  }
  set usageLogSearch(value) {
    liveLogs.usageLogSearch = value;
  }
  get usageLogHideCached() {
    return liveLogs.usageLogHideCached;
  }
  set usageLogHideCached(value) {
    liveLogs.usageLogHideCached = value;
  }

  modelUsageView = $state("chart");
  userPathUsageView = $state("chart");
  labelUsageView = $state("chart");
  typeUsageView = $state("chart");
  sessionUsageView = $state("table");

  summaryLoading = $state(false);
  modelUsageLoading = $state(false);
  userPathUsageLoading = $state(false);
  labelUsageLoading = $state(false);
  typeUsageLoading = $state(false);
  sessionUsageLoading = $state(false);
  usageLogLoading = $state(false);

  #controllers = {};

  #startRequest(key) {
    if (this.#controllers[key]) this.#controllers[key].abort();
    const controller = new AbortController();
    this.#controllers[key] = controller;
    return controller;
  }

  #clearRequest(key, controller) {
    if (this.#controllers[key] === controller) {
      this.#controllers[key] = null;
    }
  }

  onUsageFilterChanged() {
    this.fetchUsagePage();
  }

  // A breakdown setting changed: only that section refetches.
  onPivotChanged(field, value) {
    usagePivot.set(field, value);
    syncUsageUrl();
    usagePivot.fetch(usagePivotQuery());
  }

  // Chip click: add the label to the page filter, or remove it when it is
  // already selected.
  toggleUsageLabelFilter(label) {
    this.filters.labels = toggleSearchValue(this.filters.labels, label);
    this.onUsageFilterChanged();
  }

  // Chip click: add the API key to the page filter, or remove it when it is
  // already selected.
  toggleUsageAuthKeyFilter(authKeyID) {
    this.filters.authKeys = toggleSearchValue(this.filters.authKeys, authKeyID);
    this.onUsageFilterChanged();
  }

  // Chip click: add the row's endpoints (a request type's, or one endpoint)
  // to the endpoint filter, or remove them when all are already selected.
  toggleUsageEndpointsFilter(endpoints) {
    this.filters.endpoints = toggleEndpointSelection(this.filters.endpoints, endpoints);
    this.onUsageFilterChanged();
  }

  filterBySession(sessionID, userPath = "") {
    const path = String(userPath || "").trim();
    this.filters.set({ userPaths: path ? [path] : [], session: String(sessionID || "").trim() });
    this.usageLogSearch = "";
    this.usageLog.offset = 0;
    this.sessionUsage.offset = 0;
    if (router.page === "usage") {
      this.fetchUsagePage();
      return;
    }
    router.navigate("usage");
  }

  usageLabelChipTitle(label) {
    if (this.filters.labels.includes(label)) return m.usage_clear_label_filter();
    return m.usage_filter_by_label_value({ label });
  }

  usageEndpointsChipTitle(endpoints, label) {
    if (endpointsSelected(this.filters.endpoints, endpoints)) {
      return m.usage_remove_from_filter({ value: label });
    }
    return m.usage_add_to_filter({ value: label });
  }

  toggleUsageMode(mode) {
    this.usageMode = mode;
    router.navigate("usage", mode === "costs" ? "costs" : null);
    syncUsageUrl();
  }

  toggleUsageChartView(target, view) {
    if (target === "model") this.modelUsageView = view;
    if (target === "userPath") this.userPathUsageView = view;
    if (target === "label") this.labelUsageView = view;
    if (target === "type") this.typeUsageView = view;
    if (target === "session") this.sessionUsageView = view;
  }

  // Refetch everything the page shows for the current window + filters.
  async fetchUsagePage() {
    syncUsageUrl();
    await runtimeConfig.ensureLoaded();
    const requests = [
      usagePivot.fetch(usagePivotQuery()),
      this.fetchUsagePageSummary(),
      usageFacets.fetch(),
      this.fetchModelUsage(),
      this.fetchUserPathUsage(),
      this.fetchLabelUsage(),
      this.fetchEndpointUsage(),
      this.fetchSessionUsage(true),
      this.fetchUsageLog(true),
    ];
    if (usageData.cacheAnalyticsEnabled()) {
      // The usage page filters its cache cards along with the rest of the
      // page; the overview page stays unfiltered.
      requests.push(usageData.fetchCacheOverview(usageFilterQueryStr()));
    }
    await Promise.all(requests);
  }

  async fetchUsagePageSummary() {
    const controller = this.#startRequest("summary");
    this.summaryLoading = true;
    try {
      const baseQs = usageApiQuery();
      const [uncachedResult, allResult] = await Promise.all([
        getJSON("/admin/usage/summary?" + baseQs + "&cache_mode=uncached", {
          label: "usage page summary",
          signal: controller.signal,
        }),
        getJSON("/admin/usage/summary?" + baseQs + "&cache_mode=all", {
          label: "usage page summary (all)",
          signal: controller.signal,
        }),
      ]);
      if (uncachedResult.stale || allResult.stale) return;
      if (controller.signal.aborted) return;
      if (!uncachedResult.ok || !allResult.ok) {
        this.usageSummary = emptyUsagePageSummary();
        this.usageSummaryAll = emptyUsagePageSummary();
        return;
      }
      this.usageSummary =
        uncachedResult.data && typeof uncachedResult.data === "object"
          ? uncachedResult.data
          : emptyUsagePageSummary();
      this.usageSummaryAll =
        allResult.data && typeof allResult.data === "object"
          ? allResult.data
          : emptyUsagePageSummary();
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch usage page summary:", e);
      this.usageSummary = emptyUsagePageSummary();
      this.usageSummaryAll = emptyUsagePageSummary();
    } finally {
      this.#clearRequest("summary", controller);
      if (this.#controllers["summary"] === null) this.summaryLoading = false;
    }
  }

  async #fetchBreakdown(key, endpoint, label, assign, setLoading) {
    const controller = this.#startRequest(key);
    setLoading(true);
    try {
      const result = await getJSON(
        endpoint + "?" + usageApiQuery(),
        { label, signal: controller.signal },
      );
      if (result.stale) return;
      if (controller.signal.aborted) return;
      if (!result.ok) {
        assign([]);
        return;
      }
      assign(Array.isArray(result.data) ? result.data : []);
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch " + label + ":", e);
      assign([]);
    } finally {
      this.#clearRequest(key, controller);
      if (this.#controllers[key] === null) setLoading(false);
    }
  }

  // Shared request lifecycle for the two paginated fetches (session
  // breakdown, request log): track/abort the in-flight request, reset the
  // offset, normalize the entries array, and fall back to an empty result
  // on error. Mirrors #fetchBreakdown but keeps pagination state and entry
  // normalization on top of it.
  async #fetchPaginated(key, endpoint, label, { resetOffset, pageState, queryParams, assign, setLoading, emptyResult }) {
    const controller = this.#startRequest(key);
    setLoading(true);
    try {
      if (resetOffset) pageState.offset = 0;
      const qs = usageApiQuery() + queryParams();
      const result = await getJSON(endpoint + "?" + qs, { label, signal: controller.signal });
      if (result.stale || controller.signal.aborted) return;
      if (!result.ok || !result.data || typeof result.data !== "object") {
        assign(emptyResult());
        return;
      }
      const payload = result.data;
      payload.entries = Array.isArray(payload.entries) ? payload.entries : [];
      assign(payload);
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch " + label + ":", e);
      assign(emptyResult());
    } finally {
      this.#clearRequest(key, controller);
      if (this.#controllers[key] === null) setLoading(false);
    }
  }

  fetchModelUsage() {
    return this.#fetchBreakdown(
      "modelUsage",
      "/admin/usage/models",
      "usage models",
      (rows) => (this.modelUsage = rows),
      (v) => (this.modelUsageLoading = v),
    );
  }

  fetchUserPathUsage() {
    return this.#fetchBreakdown(
      "userPathUsage",
      "/admin/usage/user-paths",
      "usage user paths",
      (rows) => (this.userPathUsage = rows),
      (v) => (this.userPathUsageLoading = v),
    );
  }

  fetchLabelUsage() {
    return this.#fetchBreakdown(
      "labelUsage",
      "/admin/usage/labels",
      "usage labels",
      (rows) => (this.labelUsage = rows),
      (v) => (this.labelUsageLoading = v),
    );
  }

  fetchEndpointUsage() {
    return this.#fetchBreakdown(
      "endpointUsage",
      "/admin/usage/endpoints",
      "usage endpoints",
      (rows) => (this.endpointUsage = rows),
      (v) => (this.typeUsageLoading = v),
    );
  }

  fetchSessionUsage(resetOffset) {
    return this.#fetchPaginated("sessionUsage", "/admin/usage/sessions", "usage sessions", {
      resetOffset,
      pageState: this.sessionUsage,
      queryParams: () => sessionUsageQueryParams(this.sessionUsage),
      assign: (payload) => (this.sessionUsage = payload),
      setLoading: (v) => (this.sessionUsageLoading = v),
      emptyResult: emptySessionUsage,
    });
  }

  sessionUsageNextPage() {
    if (this.sessionUsage.offset + this.sessionUsage.limit >= this.sessionUsage.total) return;
    this.sessionUsage.offset += this.sessionUsage.limit;
    this.fetchSessionUsage(false);
  }

  sessionUsagePrevPage() {
    if (this.sessionUsage.offset <= 0) return;
    this.sessionUsage.offset = Math.max(0, this.sessionUsage.offset - this.sessionUsage.limit);
    this.fetchSessionUsage(false);
  }

  fetchUsageLog(resetOffset) {
    return this.#fetchPaginated("usageLog", "/admin/usage/log", "usage log", {
      resetOffset,
      pageState: this.usageLog,
      queryParams: () =>
        usageLogQueryParams({
          limit: this.usageLog.limit,
          offset: this.usageLog.offset,
          hideCached: this.usageLogHideCached,
          search: this.usageLogSearch,
        }),
      assign: (payload) => (this.usageLog = payload),
      setLoading: (v) => (this.usageLogLoading = v),
      emptyResult: emptyUsageLog,
    });
  }

  usageLogNextPage() {
    if (this.usageLog.offset + this.usageLog.limit < this.usageLog.total) {
      this.usageLog.offset += this.usageLog.limit;
      this.fetchUsageLog(false);
    }
  }

  usageLogPrevPage() {
    if (this.usageLog.offset > 0) {
      this.usageLog.offset = Math.max(0, this.usageLog.offset - this.usageLog.limit);
      this.fetchUsageLog(false);
    }
  }
}

export const usagePage = new UsagePageState();

// Live-stream reset hook: when the stream's replay window is lost, reload the
// page's data so the request log resyncs. Guarded so usage-page data is only
// refetched while on the usage page.
// The live merge pauses usage previews while any usage filter is set.
liveLogs.usageFilters = usageFilters;

liveLogs.fetchUsage = () => {
  if (router.page === "usage") {
    void usagePage.fetchUsagePage();
  }
};
