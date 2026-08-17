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
      <section class="rate-limit-group">
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
  .rate-limit-group {
    margin-bottom: 20px;
  }

  .rate-limit-group-header {
    display: flex;
    align-items: baseline;
    gap: 12px;
    padding: 10px 14px;
    background: color-mix(in srgb, var(--accent) 6%, var(--bg));
    border-radius: 6px;
    margin-bottom: 8px;
  }

  .rate-limit-group-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--text);
  }

  .rate-limit-group-count {
    font-size: 12px;
    color: var(--text-muted);
  }

  .rate-limit-subgroup {
    margin-left: 16px;
    margin-bottom: 12px;
  }

  .rate-limit-subgroup-title {
    font-size: 13px;
    font-weight: 500;
    color: var(--text-muted);
    margin: 8px 0 6px;
    font-family: var(--mono);
  }

  .rate-limit-group-empty {
    padding: 12px 16px;
    color: var(--text-muted);
    font-size: 13px;
    margin: 0;
  }
</style>
