<script>
  // Rate Limits page.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { rateLimits } from "./rateLimits.svelte.js";
  import RateLimitEditor from "./RateLimitEditor.svelte";

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
          class="pagination-btn pagination-btn-primary pagination-btn-with-icon"
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
  {#if rateLimits.rateLimitError && !auth.authError && !rateLimits.rateLimitFormOpen}
    <p class="form-error" role="alert" aria-live="assertive">
      {rateLimits.rateLimitError}
    </p>
  {/if}
  {#if rateLimits.rateLimitNotice && !rateLimits.rateLimitFormOpen}
    <div class="alert alert-success" role="status" aria-live="polite">
      {rateLimits.rateLimitNotice}
    </div>
  {/if}
  {#if rateLimits.rateLimitsLoading && !auth.authError}
    <LoadingState label="Loading rate limits..." />
  {/if}

  {#if (rateLimits.rateLimits.length > 0 || rateLimits.rateLimitFilter) && rateLimits.rateLimitsAvailable && !auth.authError && !rateLimits.rateLimitFormOpen}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <div class="filter-input-wrap">
          <Icon name="search" class="filter-input-icon" />
          <input
            type="text"
            id="rate-limit-filter"
            class="filter-input"
            placeholder="Filter by subject, scope, or period..."
            aria-label="Filter rate limits by subject, scope, or period"
            bind:value={rateLimits.rateLimitFilter}
          />
        </div>
      </div>
    </div>
  {/if}

  <RateLimitEditor />

  {#if filtered.length > 0 && rateLimits.rateLimitsAvailable && !auth.authError}
    <div class="budget-list">
      {#each filtered as item (rateLimits.rateLimitKey(item))}
        <section class="budget-row">
          <div class="budget-row-main">
            <div class="budget-row-head">
              <code
                class="budget-user-path"
                title={rateLimits.rateLimitScopeLabel(item) +
                  ": " +
                  rateLimits.rateLimitSubject(item)}
              >
                {rateLimits.rateLimitSubject(item)}
              </code>
              <div class="budget-row-period">
                {#if rateLimits.rateLimitScope(item) !== "user_path"}
                  <span
                    class="budget-period-label"
                    title={"Rule scope: " + rateLimits.rateLimitScopeLabel(item)}
                  >
                    <Icon
                      name={rateLimits.rateLimitScope(item) === "provider"
                        ? "server"
                        : "box"}
                      class="budget-period-icon"
                    />
                    <span>{rateLimits.rateLimitScopeLabel(item)}</span>
                  </span>
                {/if}
                <span class="budget-period-label">
                  <Icon
                    name={rateLimits.rateLimitIsConcurrent(item)
                      ? "activity"
                      : "timer"}
                    class="budget-period-icon"
                  />
                  <span>{rateLimits.rateLimitPeriodLabel(item)}</span>
                </span>
              </div>
              <div class="budget-row-controls">
                <div class="budget-row-meta">
                  <span
                    class="budget-source"
                    title={rateLimits.rateLimitIsReadOnly(item)
                      ? "Declared in configuration; read-only in the dashboard"
                      : "Managed via dashboard or admin API"}
                  >
                    {rateLimits.rateLimitSourceLabel(item)}
                  </span>
                </div>
                <div class="budget-row-actions">
                  {#if !rateLimits.rateLimitIsReadOnly(item)}
                    <TableActionButton
                      label="Edit rate limit"
                      class="budget-action-btn"
                      onclick={() => rateLimits.openRateLimitForm(item)}
                    >
                      <Icon name="pencil" class="budget-action-icon" />
                      <span class="budget-action-label">Edit</span>
                    </TableActionButton>
                  {/if}
                  <TableActionButton
                    label={rateLimits.rateLimitResettingKey === rateLimits.rateLimitKey(item) ? "Resetting counters" : "Reset counters"}
                    class="budget-action-btn budget-action-btn-warning"
                    onclick={() => rateLimits.resetRateLimit(item)}
                    disabled={rateLimits.rateLimitResettingKey === rateLimits.rateLimitKey(item)}
                  >
                    <Icon name="rotate-ccw" class="budget-action-icon" />
                    <span class="budget-action-label">
                      {rateLimits.rateLimitResettingKey ===
                      rateLimits.rateLimitKey(item)
                        ? "Resetting"
                        : "Reset"}
                    </span>
                  </TableActionButton>
                  {#if !rateLimits.rateLimitIsReadOnly(item)}
                    <TableActionButton
                      label={rateLimits.rateLimitDeletingKey === rateLimits.rateLimitKey(item) ? "Deleting rate limit" : "Delete rate limit"}
                      class="table-action-btn-danger budget-action-btn"
                      onclick={() => rateLimits.deleteRateLimit(item)}
                      disabled={rateLimits.rateLimitDeletingKey === rateLimits.rateLimitKey(item)}
                    >
                      <Icon name="trash-2" class="budget-action-icon" />
                      <span class="budget-action-label">
                        {rateLimits.rateLimitDeletingKey ===
                        rateLimits.rateLimitKey(item)
                          ? "Deleting"
                          : "Delete"}
                      </span>
                    </TableActionButton>
                  {/if}
                </div>
              </div>
            </div>
            <div class="budget-bars">
              {#if rateLimits.rateLimitIsConcurrent(item)}
                <div class="budget-bar-line">
                  <div class="budget-bar-label">
                    <span>In-flight</span>
                    <span class="budget-bar-percent">
                      {rateLimits.rateLimitUsagePercent(
                        item.in_flight,
                        item.max_requests,
                      ) + "%"}
                    </span>
                  </div>
                  <div
                    class="budget-bar-track"
                    role="progressbar"
                    aria-valuemin="0"
                    aria-valuemax="100"
                    aria-valuenow={rateLimits.rateLimitUsagePercent(
                      item.in_flight,
                      item.max_requests,
                    )}
                    aria-label={"In-flight requests: " +
                      rateLimits.formatRateLimitNumber(item.in_flight) +
                      " of " +
                      rateLimits.formatRateLimitNumber(item.max_requests)}
                    style={"--budget-progress: " +
                      rateLimits.rateLimitUsagePercent(
                        item.in_flight,
                        item.max_requests,
                      ) +
                      "%"}
                  >
                    <div
                      class="budget-bar-fill budget-bar-fill-usage"
                      class:budget-bar-fill-danger={rateLimits.rateLimitUsagePercent(
                        item.in_flight,
                        item.max_requests,
                      ) >= 100}
                      style="width: var(--budget-progress)"
                    ></div>
                    <span class="budget-bar-text-row">
                      <span class="budget-bar-text budget-bar-text-center">
                        {rateLimits.formatRateLimitNumber(item.in_flight) +
                          " of " +
                          rateLimits.formatRateLimitNumber(item.max_requests) +
                          " in flight"}
                      </span>
                    </span>
                  </div>
                </div>
              {/if}
              {#if !rateLimits.rateLimitIsConcurrent(item) && item.max_requests}
                <div class="budget-bar-line">
                  <div class="budget-bar-label">
                    <span>Requests</span>
                    <span class="budget-bar-percent">
                      {rateLimits.rateLimitUsagePercent(
                        item.requests_used,
                        item.max_requests,
                      ) + "%"}
                    </span>
                  </div>
                  <div
                    class="budget-bar-track"
                    role="progressbar"
                    aria-valuemin="0"
                    aria-valuemax="100"
                    aria-valuenow={rateLimits.rateLimitUsagePercent(
                      item.requests_used,
                      item.max_requests,
                    )}
                    aria-label={"Requests used: " +
                      rateLimits.formatRateLimitNumber(item.requests_used) +
                      " of " +
                      rateLimits.formatRateLimitNumber(item.max_requests)}
                    style={"--budget-progress: " +
                      rateLimits.rateLimitUsagePercent(
                        item.requests_used,
                        item.max_requests,
                      ) +
                      "%"}
                  >
                    <div
                      class="budget-bar-fill budget-bar-fill-usage"
                      class:budget-bar-fill-danger={rateLimits.rateLimitUsagePercent(
                        item.requests_used,
                        item.max_requests,
                      ) >= 100}
                      style="width: var(--budget-progress)"
                    ></div>
                    <span class="budget-bar-text-row">
                      <span class="budget-bar-text budget-bar-text-center">
                        {rateLimits.formatRateLimitNumber(item.requests_used) +
                          " of " +
                          rateLimits.formatRateLimitNumber(item.max_requests) +
                          " requests"}
                      </span>
                      <span class="budget-bar-text budget-bar-text-end">
                        {rateLimits.formatRateLimitNumber(
                          item.requests_remaining,
                        ) + " left"}
                      </span>
                    </span>
                  </div>
                </div>
              {/if}
              {#if !rateLimits.rateLimitIsConcurrent(item) && item.max_tokens}
                <div class="budget-bar-line">
                  <div class="budget-bar-label">
                    <span>Tokens</span>
                    <span class="budget-bar-percent">
                      {rateLimits.rateLimitUsagePercent(
                        item.tokens_used,
                        item.max_tokens,
                      ) + "%"}
                    </span>
                  </div>
                  <div
                    class="budget-bar-track"
                    role="progressbar"
                    aria-valuemin="0"
                    aria-valuemax="100"
                    aria-valuenow={rateLimits.rateLimitUsagePercent(
                      item.tokens_used,
                      item.max_tokens,
                    )}
                    aria-label={"Tokens used: " +
                      rateLimits.formatRateLimitNumber(item.tokens_used) +
                      " of " +
                      rateLimits.formatRateLimitNumber(item.max_tokens)}
                    style={"--budget-progress: " +
                      rateLimits.rateLimitUsagePercent(
                        item.tokens_used,
                        item.max_tokens,
                      ) +
                      "%"}
                  >
                    <div
                      class="budget-bar-fill budget-bar-fill-usage"
                      class:budget-bar-fill-danger={rateLimits.rateLimitUsagePercent(
                        item.tokens_used,
                        item.max_tokens,
                      ) >= 100}
                      style="width: var(--budget-progress)"
                    ></div>
                    <span class="budget-bar-text-row">
                      <span class="budget-bar-text budget-bar-text-center">
                        {rateLimits.formatRateLimitNumber(item.tokens_used) +
                          " of " +
                          rateLimits.formatRateLimitNumber(item.max_tokens) +
                          " tokens"}
                      </span>
                      <span class="budget-bar-text budget-bar-text-end">
                        {rateLimits.formatRateLimitNumber(item.tokens_remaining) +
                          " left"}
                      </span>
                    </span>
                  </div>
                </div>
              {/if}
            </div>
          </div>
        </section>
      {/each}
    </div>
  {/if}

  {#if rateLimits.rateLimits.length === 0 && !rateLimits.rateLimitFilter && !rateLimits.rateLimitsLoading && !auth.authError && !rateLimits.rateLimitError && rateLimits.rateLimitsAvailable && rateLimits.rateLimitsEnabled()}
    <p class="empty-state">No rate limits configured yet.</p>
  {/if}
  {#if rateLimits.rateLimits.length > 0 && filtered.length === 0 && rateLimits.rateLimitFilter && !rateLimits.rateLimitsLoading && !auth.authError && !rateLimits.rateLimitError && rateLimits.rateLimitsAvailable && rateLimits.rateLimitsEnabled()}
    <p class="empty-state">No rate limits match your filter.</p>
  {/if}
</div>
