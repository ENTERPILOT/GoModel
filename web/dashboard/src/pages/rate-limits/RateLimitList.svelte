<script>
  // Rate limit rule rows (scope badge, window chips, source, actions) —
  // shares the budget-row look, like the Budgets page list.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { rateLimits } from "./rateLimits.svelte.js";
  import { Activity, Box, Pencil, RotateCcw, Server, Timer, Trash2 } from "lucide";

  // rules: the filtered rule list to render (filtering stays on the page).
  let { rules } = $props();
</script>

<div class="budget-list">
      {#each rules as item (rateLimits.rateLimitKey(item))}
        <section class="budget-row">
          <div class="budget-row-main">
            <div class="budget-row-head">
              <code
                class="budget-scope-value budget-user-path"
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
                      icon={rateLimits.rateLimitScope(item) === "provider"
                        ? Server
                        : Box}
                      class="budget-period-icon"
                    />
                    <span>{rateLimits.rateLimitScopeLabel(item)}</span>
                  </span>
                {/if}
                <span class="budget-period-label">
                  <Icon
                    icon={rateLimits.rateLimitIsConcurrent(item)
                      ? Activity
                      : Timer}
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
                      <Icon icon={Pencil} class="budget-action-icon" />
                      <span class="budget-action-label">Edit</span>
                    </TableActionButton>
                  {/if}
                  <TableActionButton
                    label={rateLimits.rateLimitResettingKey === rateLimits.rateLimitKey(item) ? "Resetting counters" : "Reset counters"}
                    class="budget-action-btn budget-action-btn-warning"
                    onclick={() => rateLimits.resetRateLimit(item)}
                    disabled={rateLimits.rateLimitResettingKey === rateLimits.rateLimitKey(item)}
                  >
                    <Icon icon={RotateCcw} class="budget-action-icon" />
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
                      <Icon icon={Trash2} class="budget-action-icon" />
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
