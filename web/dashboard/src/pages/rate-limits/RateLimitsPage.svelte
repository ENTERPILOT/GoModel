<script>
  import * as m from "$lib/paraglide/messages.js";
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { rateLimits } from "./rateLimits.svelte.js";
  import RateLimitEditor from "./RateLimitEditor.svelte";
  import RateLimitList from "./RateLimitList.svelte";
  import { Plus } from "lucide";

  const PAGE = "rate-limits";

  const HELP_TEXT =
    "Rate limits cap requests, tokens, and in-flight concurrency for a user path subtree, a provider, or a model. Consumer (user path) breaches return 429 with Retry-After and x-ratelimit-* headers; saturated providers and models are skipped by load balancing and failover while capacity exists elsewhere. Counters are per gateway instance and reset on restart; token limits need usage tracking.";

  // Re-fetch when the page becomes active or the API key changes. The Models
  // page triggers its own fetch via rateLimits.fetchRateLimitsPage().
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) {
      rateLimits.fetchRateLimitsPage();
    }
  });

  const filtered = $derived.by(() => rateLimits.filteredRateLimits());
  const visibleGroup = $derived.by(() => rateLimits.visibleGroup());

  // Tab labels are the same scope names, but the in-group eyebrow was the
  // same copy — the tab now plays that role, so the group header is gone.
  function tabLabel(scope) {
    switch (scope) {
      case "user_path":
        return m.rate_limits_user_path();
      case "provider":
        return m.rate_limits_provider();
      case "model":
        return m.rate_limits_model();
      default:
        return "";
    }
  }
</script>

<div>
  <div class="page-header">
    <div>
      <InlineHelpSection copyId="rate-limits-help-copy" label="rate limits help" text={HELP_TEXT}>
        {#snippet title()}<h2>{m.rate_limits_title()}</h2>{/snippet}
      </InlineHelpSection>
    </div>
    <div class="page-header-controls">
      {#if rateLimits.rateLimitsEnabled() && rateLimits.rateLimitsAvailable && !auth.authError}
        <button
          type="button"
          class="btn btn-primary btn-with-icon"
          disabled={rateLimits.rateLimitFormSubmitting}
          onclick={() => rateLimits.openRateLimitForm()}
        >
          <Icon icon={Plus} class="form-action-icon" />
          <span>{m.rate_limits_create()}</span>
        </button>
      {/if}
    </div>
  </div>

  <AuthBanner />

  {#if (!rateLimits.rateLimitsEnabled() || !rateLimits.rateLimitsAvailable) && !auth.authError}
    <div class="alert alert-warning">{m.rate_limits_unavailable()}</div>
  {/if}
  {#if rateLimits.rateLimitError && !auth.authError}
    <p class="form-error" role="alert" aria-live="assertive">
      {rateLimits.rateLimitError}
    </p>
  {/if}
  {#if rateLimits.rateLimitsLoading && !auth.authError}
    <LoadingState label="Loading rate limits..." />
  {/if}

  {#if (rateLimits.rateLimits.length > 0 || rateLimits.rateLimitFilter) && rateLimits.rateLimitsAvailable && !auth.authError && !rateLimits.rateLimitFormOpen}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          id="rate-limit-filter"
          placeholder={m.rate_limits_filter_placeholder()}
          label={m.rate_limits_filter_label()}
          bind:value={rateLimits.rateLimitFilter}
        />
      </div>
    </div>
  {/if}

  <RateLimitEditor />

  {#if filtered.length > 0 && rateLimits.rateLimitsAvailable && !auth.authError}
    <nav class="rate-limit-tabs" aria-label="Rate limit scope">
      {#each rateLimits.tabCounts() as tab (tab.scope)}
        <button
          type="button"
          class="rate-limit-tab scope-{tab.scope}"
          class:active={rateLimits.rateLimitActiveScope === tab.scope}
          onclick={() => rateLimits.setActiveScope(tab.scope)}
        >
          <span class="rate-limit-tab-label">{tabLabel(tab.scope)}</span>
          <span class="rate-limit-tab-count">{tab.count}</span>
        </button>
      {/each}
    </nav>

    {#if visibleGroup}
      <section class="rate-limit-group scope-{visibleGroup.scope}">
        {#if visibleGroup.scope === "provider" && visibleGroup.subGroups && visibleGroup.subGroups.length > 0}
          {#each visibleGroup.subGroups as sub (sub.key)}
            <div class="rate-limit-subgroup">
              <h4 class="rate-limit-subgroup-title">{sub.subject}</h4>
              <RateLimitList rules={sub.rows} />
            </div>
          {/each}
        {:else if visibleGroup.rows.length > 0}
          <RateLimitList rules={visibleGroup.rows} />
        {:else}
          <p class="rate-limit-group-empty">{m.rate_limits_no_rules()}</p>
        {/if}
      </section>
    {/if}
  {/if}

  {#if rateLimits.rateLimits.length === 0 && !rateLimits.rateLimitFilter && !rateLimits.rateLimitsLoading && !auth.authError && !rateLimits.rateLimitError && rateLimits.rateLimitsAvailable && rateLimits.rateLimitsEnabled()}
    <p class="empty-state">{m.rate_limits_empty()}</p>
  {/if}
  {#if rateLimits.rateLimits.length > 0 && filtered.length === 0 && rateLimits.rateLimitFilter && !rateLimits.rateLimitsLoading && !auth.authError && !rateLimits.rateLimitError && rateLimits.rateLimitsAvailable && rateLimits.rateLimitsEnabled()}
    <p class="empty-state">{m.rate_limits_no_match()}</p>
  {/if}
</div>

<style>
  /* Tab strip: pill idiom from the Models page category tabs, adapted to the
     scope-hue system. flex: 1 spreads the three tabs evenly across the row —
     first at the start, middle centered, last at the end. The active tab
     carries its scope hue (tinted background, hue border, hue-mixed text) so
     the strip reads as part of the rail design below, not a separate widget. */
  .rate-limit-tabs {
    display: flex;
    gap: 6px;
    margin-bottom: 16px;
  }

  .rate-limit-tab {
    --scope-hue: var(--accent);
    flex: 1 1 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    min-width: 0;
    padding: 8px 14px;
    font: inherit;
    font-size: 13px;
    font-weight: 500;
    color: var(--text-muted);
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    cursor: pointer;
    transition:
      color 0.15s,
      background 0.15s,
      border-color 0.15s;
  }

  .rate-limit-tab.scope-provider {
    --scope-hue: var(--info);
  }

  .rate-limit-tab.scope-model {
    --scope-hue: #68765c;
  }

  .rate-limit-tab:hover {
    color: var(--text);
    background: var(--bg-surface-hover);
  }

  .rate-limit-tab:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--scope-hue) 70%, var(--border));
    outline-offset: 1px;
  }

  .rate-limit-tab.active {
    color: color-mix(in srgb, var(--scope-hue) 62%, var(--text));
    background: color-mix(in srgb, var(--scope-hue) 12%, var(--bg-surface));
    border-color: color-mix(in srgb, var(--scope-hue) 45%, var(--border));
  }

  .rate-limit-tab-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .rate-limit-tab-count {
    flex: 0 0 auto;
    min-width: 20px;
    padding: 1px 7px;
    font-size: 11px;
    font-weight: 600;
    text-align: center;
    color: var(--text-muted);
    background: var(--bg);
    border-radius: 999px;
  }

  .rate-limit-tab.active .rate-limit-tab-count {
    color: inherit;
    background: color-mix(in srgb, var(--scope-hue) 18%, var(--bg));
  }

  @media (max-width: 768px) {
    .rate-limit-tab {
      padding: 6px 10px;
      font-size: 12px;
      gap: 6px;
    }
  }

  /* Scope rail: each group gets a categorical hue drawn from the dashboard's
     own palette (user_path = accent tan, provider = info blue, model = olive
     from the budget period badges). The rail runs the group's full height so
     the scope identity is scannable without reading the header. */
  .rate-limit-group {
    --scope-hue: var(--accent);
    position: relative;
    margin-bottom: 24px;
    padding-left: 16px;
  }

  .rate-limit-group::before {
    content: "";
    position: absolute;
    left: 0;
    top: 2px;
    bottom: 2px;
    width: 3px;
    border-radius: 999px;
    background: var(--scope-hue);
  }

  .rate-limit-group.scope-provider {
    --scope-hue: var(--info);
  }

  .rate-limit-group.scope-model {
    --scope-hue: #68765c;
  }

  .rate-limit-group-header {
    display: flex;
    align-items: baseline;
    gap: 10px;
    margin-bottom: 10px;
  }

  .rate-limit-group-title {
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.12em;
    text-transform: uppercase;
    /* Mix toward --text so the hue stays readable in both themes: it
       lightens the tan/olive on dark, darkens the blue on light. */
    color: color-mix(in srgb, var(--scope-hue) 62%, var(--text));
  }

  .rate-limit-group-count {
    font-size: 12px;
    color: var(--text-muted);
  }

  /* Subject chips inside a group inherit the rail hue, reinforcing scope
     identity on every row. The shared inspector is untouched — it does not
     render inside .rate-limit-group. */
  .rate-limit-group :global(.budget-scope-value) {
    border-color: color-mix(in srgb, var(--scope-hue) 40%, var(--border));
    background: color-mix(in srgb, var(--scope-hue) 12%, var(--bg));
  }

  .rate-limit-subgroup {
    margin-bottom: 12px;
  }

  .rate-limit-subgroup-title {
    font-size: 12px;
    font-weight: 600;
    color: color-mix(in srgb, var(--scope-hue) 45%, var(--text-muted));
    margin: 10px 0 6px;
    font-family: "SF Mono", Menlo, Consolas, monospace;
  }

  .rate-limit-group-empty {
    padding: 4px 0 8px;
    color: var(--text-muted);
    font-size: 13px;
    margin: 0;
  }
</style>
