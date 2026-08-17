<script>
  import * as m from "$lib/paraglide/messages.js";
  // Rate limit rule rows (window chips, source, actions) — shares the
  // budget-row look, like the Budgets page list. Scope lives in the page's
  // group header now, so this component no longer renders a per-row scope
  // badge.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { rateLimits } from "./rateLimits.svelte.js";
  import { Activity, Pencil, RotateCcw, Timer, Trash2 } from "lucide";

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
                {#if item.per_child}
                  <span
                    class="budget-period-label"
                    title={m.rate_limits_per_child_title()}
                  >
                    <span>{m.rate_limits_per_child_badge()}</span>
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
                      ? m.rate_limits_config_read_only()
                      : m.rate_limits_managed()}
                  >
                    {rateLimits.rateLimitSourceLabel(item)}
                  </span>
                </div>
                <div class="budget-row-actions">
                  {#if !rateLimits.rateLimitIsReadOnly(item)}
                    <TableActionButton
                      label={m.rate_limits_edit_action()}
                      class="budget-action-btn"
                      onclick={() => rateLimits.openRateLimitForm(item)}
                    >
                      <Icon icon={Pencil} class="budget-action-icon" />
                      <span class="budget-action-label">{m.rate_limits_edit()}</span>
                    </TableActionButton>
                  {/if}
                  <TableActionButton
                    label={rateLimits.rateLimitResettingKey === rateLimits.rateLimitKey(item)
                      ? m.rate_limits_resetting_counters()
                      : m.rate_limits_reset_counters()}
                    class="budget-action-btn budget-action-btn-warning"
                    onclick={() => rateLimits.resetRateLimit(item)}
                    disabled={rateLimits.rateLimitResettingKey === rateLimits.rateLimitKey(item)}
                  >
                    <Icon icon={RotateCcw} class="budget-action-icon" />
                    <span class="budget-action-label">
                      {rateLimits.rateLimitResettingKey ===
                      rateLimits.rateLimitKey(item)
                        ? m.rate_limits_resetting()
                        : m.rate_limits_reset()}
                    </span>
                  </TableActionButton>
                  {#if !rateLimits.rateLimitIsReadOnly(item)}
                    <TableActionButton
                      label={rateLimits.rateLimitDeletingKey === rateLimits.rateLimitKey(item)
                        ? m.rate_limits_deleting_action()
                        : m.rate_limits_delete_action()}
                      class="table-action-btn-danger budget-action-btn"
                      onclick={() => rateLimits.deleteRateLimit(item)}
                      disabled={rateLimits.rateLimitDeletingKey === rateLimits.rateLimitKey(item)}
                    >
                      <Icon icon={Trash2} class="budget-action-icon" />
                      <span class="budget-action-label">
                        {rateLimits.rateLimitDeletingKey ===
                        rateLimits.rateLimitKey(item)
                          ? m.rate_limits_deleting()
                          : m.rate_limits_delete()}
                      </span>
                    </TableActionButton>
                  {/if}
                </div>
              </div>
            </div>
            <div class="budget-bars">
              {#if item.per_child}
                <div class="per-child-summary">
                  {m.rate_limits_per_child_before_subject()}<code
                    >{rateLimits.rateLimitSubject(item)}</code
                  >{m.rate_limits_per_child_before_usage()}<code>/v1/usage</code
                  >{m.rate_limits_per_child_after_usage()}
                </div>
              {:else}
                {#if rateLimits.rateLimitIsConcurrent(item)}
                  <div class="budget-bar-line">
                    <div class="budget-bar-label">
                      <span>{m.rate_limits_in_flight()}</span>
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
                      aria-label={m.rate_limits_in_flight_progress({
                        used: rateLimits.formatRateLimitNumber(item.in_flight),
                        limit: rateLimits.formatRateLimitNumber(item.max_requests),
                      })}
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
                          {m.rate_limits_in_flight_progress({
                            used: rateLimits.formatRateLimitNumber(item.in_flight),
                            limit: rateLimits.formatRateLimitNumber(item.max_requests),
                          })}
                        </span>
                      </span>
                    </div>
                  </div>
                {/if}
                {#if !rateLimits.rateLimitIsConcurrent(item) && item.max_requests}
                  <div class="budget-bar-line">
                    <div class="budget-bar-label">
                      <span>{m.rate_limits_requests()}</span>
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
                      aria-label={m.rate_limits_requests_progress({
                        used: rateLimits.formatRateLimitNumber(item.requests_used),
                        limit: rateLimits.formatRateLimitNumber(item.max_requests),
                      })}
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
                          {m.rate_limits_requests_progress({
                            used: rateLimits.formatRateLimitNumber(item.requests_used),
                            limit: rateLimits.formatRateLimitNumber(item.max_requests),
                          })}
                        </span>
                        <span class="budget-bar-text budget-bar-text-end">
                          {m.rate_limits_left({ count: rateLimits.formatRateLimitNumber(item.requests_remaining) })}
                        </span>
                      </span>
                    </div>
                  </div>
                {/if}
                {#if !rateLimits.rateLimitIsConcurrent(item) && item.max_tokens}
                  <div class="budget-bar-line">
                    <div class="budget-bar-label">
                      <span>{m.rate_limits_tokens()}</span>
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
                      aria-label={m.rate_limits_tokens_progress({
                        used: rateLimits.formatRateLimitNumber(item.tokens_used),
                        limit: rateLimits.formatRateLimitNumber(item.max_tokens),
                      })}
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
                          {m.rate_limits_tokens_progress({
                            used: rateLimits.formatRateLimitNumber(item.tokens_used),
                            limit: rateLimits.formatRateLimitNumber(item.max_tokens),
                          })}
                        </span>
                        <span class="budget-bar-text budget-bar-text-end">
                          {m.rate_limits_left({ count: rateLimits.formatRateLimitNumber(item.tokens_remaining) })}
                        </span>
                      </span>
                    </div>
                  </div>
                {/if}
              {/if}
            </div>
          </div>
        </section>
      {/each}
</div>
