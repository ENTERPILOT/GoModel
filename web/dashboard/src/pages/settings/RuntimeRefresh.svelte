<script>
  // Runtime refresh (POST /admin/runtime/refresh): re-pulls model metadata,
  // provider inventory, API keys, aliases, access rules, guardrails, and
  // workflows, then renders the per-step report. A successful refresh bumps
  // auth.refreshTick so every page refetches.
  import { auth } from "$lib/stores/auth.svelte.js";
  import { sendJSON } from "$lib/api/client.js";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import {
    runtimeRefreshSummary,
    runtimeRefreshSucceeded,
    runtimeRefreshWarnings,
    runtimeRefreshSteps,
    runtimeRefreshStepLabel,
  } from "./runtime-refresh-logic.js";

  let loading = $state(false);
  let notice = $state("");
  let error = $state("");
  let report = $state(null);

  async function refreshRuntime() {
    if (loading) {
      return;
    }
    loading = true;
    notice = "";
    error = "";
    report = null;
    try {
      const result = await sendJSON("/admin/runtime/refresh", "POST", undefined, {
        label: "runtime refresh",
      });
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        notice = "Runtime refresh failed.";
        error = notice;
        return;
      }
      report =
        result.data && typeof result.data === "object" ? result.data : null;
      notice = runtimeRefreshSummary(report);
      auth.refresh();
    } catch (e) {
      console.error("Failed to refresh runtime:", e);
      notice = "Runtime refresh failed.";
      error = notice;
    } finally {
      loading = false;
    }
  }
</script>

<div class="settings-refresh-section">
  <InlineHelpSection
    copyId="runtime-refresh-help-copy"
    label="runtime refresh help"
    text="Pull the latest model metadata, provider inventory, API keys, aliases, model access rules, guardrails, and workflows."
  >
    {#snippet title()}<h3>Runtime Refresh</h3>{/snippet}
  </InlineHelpSection>
  <div class="settings-refresh-actions">
    <button
      type="button"
      class="pagination-btn pagination-btn-primary pagination-btn-with-icon settings-refresh-btn"
      class:is-refreshing={loading}
      disabled={loading}
      aria-busy={loading ? "true" : "false"}
      aria-describedby="runtime-refresh-help-copy"
      onclick={refreshRuntime}
    >
      <Icon name="refresh-cw" class="settings-refresh-icon" />
      <span>Refresh</span>
    </button>
  </div>
</div>
<div>
  {#if notice && runtimeRefreshSucceeded(report)}
    <div
      class="alert alert-success settings-refresh-alert"
      role="status"
      aria-live="polite"
      aria-atomic="true"
    >
      {notice}
    </div>
  {/if}
  {#if error || (notice && runtimeRefreshWarnings(report))}
    <div
      class="alert alert-warning settings-refresh-alert"
      role="alert"
      aria-live="assertive"
      aria-atomic="true"
    >
      {error || notice}
    </div>
  {/if}
  {#if runtimeRefreshSteps(report).length > 0}
    <ul class="runtime-refresh-steps" role="status" aria-live="polite">
      {#each runtimeRefreshSteps(report) as step (step.name)}
        <li class={"runtime-refresh-step is-" + step.status}>
          {runtimeRefreshStepLabel(step)}
        </li>
      {/each}
    </ul>
  {/if}
</div>
