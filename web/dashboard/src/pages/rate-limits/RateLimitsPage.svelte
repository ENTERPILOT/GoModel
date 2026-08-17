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
  const groups = $derived.by(() => rateLimits.groupedRateLimits());

  function groupTitle(scope) {
    switch (scope) {
      case "user_path":
        return m.rate_limits_group_user_path();
      case "provider":
        return m.rate_limits_group_provider();
      case "model":
        return m.rate_limits_group_model();
      default:
        return "";
    }
  }

  function groupCount(scope, count) {
    switch (scope) {
      case "user_path":
        return m.rate_limits_group_count_user_path({ count });
      case "provider":
        return m.rate_limits_group_count_provider({ count });
      case "model":
        return m.rate_limits_group_count_model({ count });
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
    {#each groups as group (group.key)}
      <section class="rate-limit-group scope-{group.scope}">
        <div class="rate-limit-group-header">
          <span class="rate-limit-group-title">{groupTitle(group.scope)}</span>
          <span class="rate-limit-group-count">{groupCount(group.scope, group.count)}</span>
        </div>
        {#if group.scope === "provider" && group.subGroups && group.subGroups.length > 0}
          {#each group.subGroups as sub (sub.key)}
            <div class="rate-limit-subgroup">
              <h4 class="rate-limit-subgroup-title">{sub.subject}</h4>
              <RateLimitList rules={sub.rows} />
            </div>
          {/each}
        {:else if group.rows.length > 0}
          <RateLimitList rules={group.rows} />
        {:else}
          <p class="rate-limit-group-empty">{m.rate_limits_no_rules()}</p>
        {/if}
      </section>
    {/each}
  {/if}

  {#if rateLimits.rateLimits.length === 0 && !rateLimits.rateLimitFilter && !rateLimits.rateLimitsLoading && !auth.authError && !rateLimits.rateLimitError && rateLimits.rateLimitsAvailable && rateLimits.rateLimitsEnabled()}
    <p class="empty-state">{m.rate_limits_empty()}</p>
  {/if}
  {#if rateLimits.rateLimits.length > 0 && filtered.length === 0 && rateLimits.rateLimitFilter && !rateLimits.rateLimitsLoading && !auth.authError && !rateLimits.rateLimitError && rateLimits.rateLimitsAvailable && rateLimits.rateLimitsEnabled()}
    <p class="empty-state">{m.rate_limits_no_match()}</p>
  {/if}
</div>

<style>
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
