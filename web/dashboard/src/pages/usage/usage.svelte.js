// Usage page state: page-level facet filters, filtered summaries backing the
// stat cards, per-model / per-user-path / per-label / per-session breakdowns, and the
// request log. The shared window comes from the dateRange store and the
// shared cache overview lives in usageData (this page passes its filter
// query through fetchCacheOverview).

import { getJSON, isAbortError } from "$lib/api/client.js";
import { router } from "$lib/stores/router.svelte.js";
import { dateRange } from "$lib/stores/dateRange.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { usageData } from "$lib/stores/usageData.svelte.js";
import { providerDisplayValue } from "$lib/utils/format.js";
import * as m from "$lib/paraglide/messages.js";
import { liveLogs } from "../audit-logs/liveLogs.svelte.js";
import {
  emptyUsageLog,
  emptyUsagePageSummary,
  emptySessionUsage,
  facetOptionList,
  sessionUsageQueryParams,
  usageFilterQueryStr,
  usageLogQueryParams,
} from "./usage-helpers.js";

class UsagePageState {
  usageMode = $state("tokens");

  // Page-level data filters: every widget on the page follows them. They live
  // on the liveLogs singleton, which uses them as the insert gate for live
  // request-log previews — delegating keeps the page and the live merge
  // engine in lockstep by construction.
  get usageFilterModel() {
    return liveLogs.usageFilterModel;
  }
  set usageFilterModel(value) {
    liveLogs.usageFilterModel = value;
  }
  get usageFilterProvider() {
    return liveLogs.usageFilterProvider;
  }
  set usageFilterProvider(value) {
    liveLogs.usageFilterProvider = value;
  }
  get usageFilterLabel() {
    return liveLogs.usageFilterLabel;
  }
  set usageFilterLabel(value) {
    liveLogs.usageFilterLabel = value;
  }
  get usageFilterUserPath() {
    return liveLogs.usageFilterUserPath;
  }
  set usageFilterUserPath(value) {
    liveLogs.usageFilterUserPath = value;
  }
  get usageFilterSession() {
    return liveLogs.usageFilterSession;
  }
  set usageFilterSession(value) {
    liveLogs.usageFilterSession = value;
  }
  usageFacetOptions = $state({ models: [], providers: [], labels: [] });

  // Filtered summaries backing the stat cards, fetched in both cache modes:
  // uncached carries real provider spend (cached rows store the avoided
  // cost, which must not inflate the cost card), all carries the row count
  // matching the log's default view.
  usageSummary = $state(emptyUsagePageSummary());
  usageSummaryAll = $state(emptyUsagePageSummary());

  modelUsage = $state([]);
  userPathUsage = $state([]);
  labelUsage = $state([]);
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
  sessionUsageView = $state("table");

  summaryLoading = $state(false);
  modelUsageLoading = $state(false);
  userPathUsageLoading = $state(false);
  labelUsageLoading = $state(false);
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

  filterQueryStr(excludeFacet) {
    return usageFilterQueryStr(
      {
        model: this.usageFilterModel,
        provider: this.usageFilterProvider,
        label: this.usageFilterLabel,
        user_path: this.usageFilterUserPath,
        session_id: this.usageFilterSession,
      },
      excludeFacet,
    );
  }

  onUsageFilterChanged() {
    this.fetchUsagePage();
  }

  // Chip click: filter the whole page by the label, or clear the filter when
  // the chip's label is already active.
  toggleUsageLabelFilter(label) {
    this.usageFilterLabel = this.usageFilterLabel === label ? "" : label;
    this.onUsageFilterChanged();
  }

  filterBySession(sessionID, userPath = "") {
    this.usageFilterModel = "";
    this.usageFilterProvider = "";
    this.usageFilterLabel = "";
    this.usageFilterUserPath = String(userPath || "").trim();
    this.usageFilterSession = String(sessionID || "").trim();
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
    if (this.usageFilterLabel === label) return m.usage_clear_label_filter();
    return m.usage_filter_by_label_value({ label });
  }

  toggleUsageMode(mode) {
    this.usageMode = mode;
    router.navigate("usage", mode === "costs" ? "costs" : null);
  }

  toggleUsageChartView(target, view) {
    if (target === "model") this.modelUsageView = view;
    if (target === "userPath") this.userPathUsageView = view;
    if (target === "label") this.labelUsageView = view;
    if (target === "session") this.sessionUsageView = view;
  }

  usageFilterModelOptions() {
    return facetOptionList(this.usageFacetOptions.models, this.usageFilterModel);
  }

  usageFilterProviderOptions() {
    return facetOptionList(this.usageFacetOptions.providers, this.usageFilterProvider);
  }

  usageFilterLabelOptions() {
    return facetOptionList(this.usageFacetOptions.labels, this.usageFilterLabel);
  }

  // Refetch everything the page shows for the current window + filters.
  async fetchUsagePage() {
    await runtimeConfig.ensureLoaded();
    const requests = [
      this.fetchUsagePageSummary(),
      this.fetchUsageFacetOptions(),
      this.fetchModelUsage(),
      this.fetchUserPathUsage(),
      this.fetchLabelUsage(),
      this.fetchSessionUsage(true),
      this.fetchUsageLog(true),
    ];
    if (usageData.cacheAnalyticsEnabled()) {
      // The usage page filters its cache cards along with the rest of the
      // page; the overview page stays unfiltered.
      requests.push(usageData.fetchCacheOverview(this.filterQueryStr()));
    }
    await Promise.all(requests);
  }

  async fetchUsagePageSummary() {
    const controller = this.#startRequest("summary");
    this.summaryLoading = true;
    try {
      const baseQs = dateRange.queryStr() + this.filterQueryStr();
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

  // Facet dropdown options follow the faceted-search rule: each facet's
  // choices honor every active filter except its own, so a selected value
  // never erases its alternatives.
  async fetchUsageFacetOptions() {
    const controller = this.#startRequest("facets");
    try {
      const fetchRows = async (endpoint, excludeFacet) => {
        const result = await getJSON(
          endpoint + "?" + dateRange.queryStr() + this.filterQueryStr(excludeFacet),
          { label: "usage facet options", signal: controller.signal },
        );
        if (result.stale) return null;
        if (!result.ok) return [];
        return Array.isArray(result.data) ? result.data : [];
      };
      // Without a model or provider filter, the two by-model queries are
      // identical; fetch once and reuse.
      const modelRowsPromise = fetchRows("/admin/usage/models", "model");
      const sharedByModel = !this.usageFilterModel && !this.usageFilterProvider;
      const [modelRows, providerRows, labelRows] = await Promise.all([
        modelRowsPromise,
        sharedByModel ? modelRowsPromise : fetchRows("/admin/usage/models", "provider"),
        fetchRows("/admin/usage/labels", "label"),
      ]);
      if (
        controller.signal.aborted ||
        modelRows === null ||
        providerRows === null ||
        labelRows === null
      ) {
        return;
      }
      this.usageFacetOptions = {
        models: modelRows.map((row) => row && row.model).filter(Boolean),
        providers: providerRows.map((row) => providerDisplayValue(row)).filter(Boolean),
        labels: labelRows.map((row) => row && row.label).filter(Boolean),
      };
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch usage facet options:", e);
      this.usageFacetOptions = { models: [], providers: [], labels: [] };
    } finally {
      this.#clearRequest("facets", controller);
    }
  }

  async #fetchBreakdown(key, endpoint, label, assign, setLoading) {
    const controller = this.#startRequest(key);
    setLoading(true);
    try {
      const result = await getJSON(
        endpoint + "?" + dateRange.queryStr() + this.filterQueryStr(),
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

  async fetchSessionUsage(resetOffset) {
    const controller = this.#startRequest("sessionUsage");
    this.sessionUsageLoading = true;
    try {
      if (resetOffset) this.sessionUsage.offset = 0;
      let qs = dateRange.queryStr() + this.filterQueryStr();
      qs += sessionUsageQueryParams(this.sessionUsage);
      const result = await getJSON("/admin/usage/sessions?" + qs, {
        label: "usage sessions",
        signal: controller.signal,
      });
      if (result.stale || controller.signal.aborted) return;
      if (!result.ok || !result.data || typeof result.data !== "object") {
        this.sessionUsage = emptySessionUsage();
        return;
      }
      const payload = result.data;
      payload.entries = Array.isArray(payload.entries) ? payload.entries : [];
      this.sessionUsage = payload;
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch usage sessions:", e);
      this.sessionUsage = emptySessionUsage();
    } finally {
      this.#clearRequest("sessionUsage", controller);
      if (this.#controllers["sessionUsage"] === null) this.sessionUsageLoading = false;
    }
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

  async fetchUsageLog(resetOffset) {
    const controller = this.#startRequest("usageLog");
    this.usageLogLoading = true;
    try {
      if (resetOffset) this.usageLog.offset = 0;
      let qs = dateRange.queryStr() + this.filterQueryStr();
      qs += usageLogQueryParams({
        limit: this.usageLog.limit,
        offset: this.usageLog.offset,
        hideCached: this.usageLogHideCached,
        search: this.usageLogSearch,
      });
      const result = await getJSON("/admin/usage/log?" + qs, {
        label: "usage log",
        signal: controller.signal,
      });
      if (result.stale) return;
      if (controller.signal.aborted) return;
      if (!result.ok) {
        this.usageLog = emptyUsageLog();
        return;
      }
      const payload =
        result.data && typeof result.data === "object" ? result.data : emptyUsageLog();
      if (!payload.entries) payload.entries = [];
      this.usageLog = payload;
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch usage log:", e);
      this.usageLog = emptyUsageLog();
    } finally {
      this.#clearRequest("usageLog", controller);
      if (this.#controllers["usageLog"] === null) this.usageLogLoading = false;
    }
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
liveLogs.fetchUsage = () => {
  if (router.page === "usage") {
    void usagePage.fetchUsagePage();
  }
};
