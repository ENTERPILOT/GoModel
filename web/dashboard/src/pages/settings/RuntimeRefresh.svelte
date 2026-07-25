<script>
  // Runtime refresh (POST /admin/runtime/refresh): re-pulls model metadata,
  // provider inventory, API keys, aliases, access rules, guardrails, and
  // workflows, then renders the per-step report. A successful refresh bumps
  // auth.refreshTick so every page refetches.
  import { auth } from "$lib/stores/auth.svelte.js";
  import { flash } from "$lib/stores/flash.svelte.js";
  import { sendJSON } from "$lib/api/client.js";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import {
    runtimeRefreshSummary,
    runtimeRefreshSucceeded,
    runtimeRefreshSteps,
    runtimeRefreshStepLabel,
  } from "./runtime-refresh-logic.js";

  let loading = $state(false);
  let report = $state(null);

  async function refreshRuntime() {
    if (loading) {
      return;
    }
    loading = true;
    report = null;
    try {
      const result = await sendJSON("/admin/runtime/refresh", "POST", undefined, {
        label: "runtime refresh",
      });
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        flash.error("Runtime refresh failed.");
        return;
      }
      report =
        result.data && typeof result.data === "object" ? result.data : null;
      // The per-step report stays rendered below; the summary is transient.
      if (runtimeRefreshSucceeded(report)) {
        flash.success(runtimeRefreshSummary(report));
      } else {
        flash.error(runtimeRefreshSummary(report));
      }
      auth.refresh();
    } catch (e) {
      console.error("Failed to refresh runtime:", e);
      flash.error("Runtime refresh failed.");
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
      class="btn btn-primary btn-with-icon settings-refresh-btn"
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

<style>
/* Styles owned by this component (moved from dashboard.css). */
.settings-refresh-icon {
    width: 16px;
    height: 16px;
    flex: 0 0 16px;
  }

.runtime-refresh-steps {
    display: grid;
    gap: 8px;
    margin-top: 14px;
    padding-left: 18px;
    color: var(--text-muted);
    font-size: 13px;
  }

.runtime-refresh-step.is-ok {
    color: var(--success);
  }

.runtime-refresh-step.is-partial {
    color: var(--warning);
  }

.runtime-refresh-step.is-failed {
    color: var(--danger);
  }
</style>
