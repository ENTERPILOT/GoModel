// Audit-side workflow-version plumbing (prefetchAuditWorkflows /
// fetchWorkflowVersion / cacheWorkflowVersion* / auditEntryWorkflow).
// The workflow pipeline chart itself lives with the
// workflows page ($pages/workflows/WorkflowChart.svelte +
// workflowChartLogic.workflowAuditChart(entry, source, caps)); this store owns
// the version cache the audit page resolves `source` from.

import { getJSON } from "$lib/api/client.ts";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.ts";

class AuditWorkflowsStore {
  // workflow_version_id → workflow version, or null for known-missing
  // versions (the cacheMissingWorkflowVersion sentinel).
  workflowVersionsByID = $state({});
  // In-flight fetches by id so concurrent prefetches share one request.
  workflowVersionRequests = {};

  // Global feature caps for the chart (workflowApplyGlobalFeatureCaps
  // inputs; same shape as the workflows page's featureCaps()).
  workflowFeatureCaps() {
    return {
      cache: runtimeConfig.cacheVisible(),
      audit: runtimeConfig.auditVisible(),
      usage: runtimeConfig.usageVisible(),
      budget: runtimeConfig.budgetsVisible(),
      guardrails: runtimeConfig.guardrailsVisible(),
      failover: runtimeConfig.booleanFlag("FAILOVER_ENABLED", true),
    };
  }

  cacheWorkflowVersion(workflow) {
    const workflowID = String((workflow && workflow.id) || "").trim();
    if (!workflowID) {
      return null;
    }
    this.workflowVersionsByID = {
      ...(this.workflowVersionsByID || {}),
      [workflowID]: workflow,
    };
    return workflow;
  }

  cacheMissingWorkflowVersion(workflowID) {
    const normalizedID = String(workflowID || "").trim();
    if (!normalizedID) {
      return;
    }
    this.workflowVersionsByID = {
      ...(this.workflowVersionsByID || {}),
      [normalizedID]: null,
    };
  }

  workflowVersionCacheHas(workflowID) {
    return Object.prototype.hasOwnProperty.call(
      this.workflowVersionsByID || {},
      String(workflowID || "").trim(),
    );
  }

  workflowVersionByID(workflowID) {
    const normalizedID = String(workflowID || "").trim();
    if (!normalizedID) {
      return null;
    }
    if (this.workflowVersionCacheHas(normalizedID)) {
      return this.workflowVersionsByID[normalizedID];
    }
    // On the audit page every id goes through prefetch, so the cache is the
    // source of truth here.
    return null;
  }

  async fetchWorkflowVersion(workflowID) {
    const normalizedID = String(workflowID || "").trim();
    if (!normalizedID) {
      return null;
    }
    if (this.workflowVersionCacheHas(normalizedID)) {
      return this.workflowVersionsByID[normalizedID];
    }
    if (this.workflowVersionRequests[normalizedID]) {
      return this.workflowVersionRequests[normalizedID];
    }

    const request = (async () => {
      const controller =
        typeof AbortController === "function" ? new AbortController() : null;
      const timeoutID = controller
        ? setTimeout(() => controller.abort(), 10000)
        : null;
      try {
        const result = await getJSON(
          "/admin/workflows/" + encodeURIComponent(normalizedID),
          {
            label: "workflow",
            signal: controller ? controller.signal : undefined,
          },
        );
        if (result.stale) {
          return null;
        }
        if (result.status === 404) {
          this.cacheMissingWorkflowVersion(normalizedID);
          return null;
        }
        if (!result.ok) {
          return null;
        }
        const payload = result.data;
        if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
          this.cacheMissingWorkflowVersion(normalizedID);
          return null;
        }
        return this.cacheWorkflowVersion(payload);
      } catch (e) {
        if (e && e.name === "AbortError") {
          return null;
        }
        console.error("Failed to fetch workflow version:", e);
        return null;
      } finally {
        if (timeoutID !== null) {
          clearTimeout(timeoutID);
        }
        delete this.workflowVersionRequests[normalizedID];
      }
    })();

    this.workflowVersionRequests[normalizedID] = request;
    return request;
  }

  async prefetchAuditWorkflows(entries) {
    const uniqueWorkflowIDs = [
      ...new Set(
        (Array.isArray(entries) ? entries : [])
          .map((entry) =>
            String((entry && entry.workflow_version_id) || "").trim(),
          )
          .filter(Boolean),
      ),
    ];
    if (uniqueWorkflowIDs.length === 0) {
      return;
    }
    await Promise.all(
      uniqueWorkflowIDs.map((workflowID) => this.fetchWorkflowVersion(workflowID)),
    );
  }

  auditEntryWorkflow(entry) {
    const workflowID = String(
      (entry && entry.workflow_version_id) || "",
    ).trim();
    if (!workflowID) {
      return null;
    }
    return this.workflowVersionByID(workflowID);
  }
}

export const auditWorkflows = new AuditWorkflowsStore();
