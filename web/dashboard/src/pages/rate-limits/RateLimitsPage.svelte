<script>
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
</script>

<div>
  <div class="page-header">
    <div>
      <InlineHelpSection copyId="rate-limits-help-copy" label="rate limits help" text={HELP_TEXT}>
        {#snippet title()}<h2>Rate Limits</h2>{/snippet}
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
          <Icon name="plus" class="form-action-icon" />
          <span>Create Rate Limit</span>
        </button>
      {/if}
    </div>
  </div>

  <AuthBanner />

  {#if (!rateLimits.rateLimitsEnabled() || !rateLimits.rateLimitsAvailable) && !auth.authError}
    <div class="alert alert-warning">Rate limit management is unavailable.</div>
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
          placeholder="Filter by subject, scope, or period..."
          label="Filter rate limits by subject, scope, or period"
          bind:value={rateLimits.rateLimitFilter}
        />
      </div>
    </div>
  {/if}

  <RateLimitEditor />

  {#if filtered.length > 0 && rateLimits.rateLimitsAvailable && !auth.authError}
    <RateLimitList rules={filtered} />
  {/if}

  {#if rateLimits.rateLimits.length === 0 && !rateLimits.rateLimitFilter && !rateLimits.rateLimitsLoading && !auth.authError && !rateLimits.rateLimitError && rateLimits.rateLimitsAvailable && rateLimits.rateLimitsEnabled()}
    <p class="empty-state">No rate limits configured yet.</p>
  {/if}
  {#if rateLimits.rateLimits.length > 0 && filtered.length === 0 && rateLimits.rateLimitFilter && !rateLimits.rateLimitsLoading && !auth.authError && !rateLimits.rateLimitError && rateLimits.rateLimitsAvailable && rateLimits.rateLimitsEnabled()}
    <p class="empty-state">No rate limits match your filter.</p>
  {/if}
</div>
