<script>
  // Budget rows with usage/period progress bars.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { formatCost } from "$lib/utils/format.js";
  import { labelColor } from "$lib/utils/chartTheme.js";
  import { budgetsStore as store } from "./budgets.svelte.js";
  import {
    budgetKey,
    budgetPeriodBarClass,
    budgetPeriodClass,
    budgetPeriodDurationLabel,
    budgetPeriodIcon,
    budgetPeriodLabel,
    budgetPeriodPercent,
    budgetPeriodPercentLabel,
    budgetPeriodTrackClass,
    budgetRemainingLabel,
    budgetScope,
    budgetScopeLabel,
    budgetScopeValueClass,
    budgetSourceLabel,
    budgetSubject,
    budgetSourceTitle,
    budgetUsagePercent,
    budgetUsagePercentLabel,
    budgetUsageRatio,
  } from "./budgets-helpers.js";
  import { Pencil, RotateCcw, Tag, Trash2 } from "lucide";

  let { budgets = [] } = $props();

  // Formatted timestamp + effective zone label.
  function timestampTimeZoneTitle(ts) {
    if (!ts) {
      return "";
    }
    const formatted = timezone.formatTimestamp(ts);
    if (!formatted || formatted === "-") {
      return "";
    }
    return formatted + " " + timezone.effectiveTimeZoneLabel();
  }
</script>

<div class="budget-list">
  {#each budgets as item (budgetKey(item))}
    <section class="budget-row">
      <div class="budget-row-main">
        <div class="budget-row-head">
          <code
            class="budget-scope-value {budgetScopeValueClass(item)}"
            style={budgetScope(item) === "label"
              ? "--label-color: " + labelColor(budgetSubject(item))
              : undefined}
            title={budgetScopeLabel(item) + ": " + budgetSubject(item)}
          >
            {#if budgetScope(item) === "label"}
              <Icon icon={Tag} class="budget-scope-icon" />
            {/if}
            {budgetSubject(item)}
          </code>
          <div class="budget-row-period">
            {#if item.per_child}
              <span
                class="budget-period-label"
                title="An independent budget is created for each direct child path"
              >
                <span>per child</span>
              </span>
            {/if}
            <span class="budget-period-label {budgetPeriodClass(item)}">
              <Icon icon={budgetPeriodIcon(item)} class="budget-period-icon" />
              <span>{budgetPeriodLabel(item)}</span>
            </span>
          </div>
          <div class="budget-row-controls">
            <div class="budget-row-meta">
              <span class="budget-source" title={budgetSourceTitle(item)}>
                {budgetSourceLabel(item)}
              </span>
            </div>
            <div class="budget-row-actions">
              <TableActionButton
                label="Edit budget"
                class="budget-action-btn"
                onclick={() => store.openForm(item)}
              >
                <Icon icon={Pencil} class="budget-action-icon" />
                <span class="budget-action-label">Edit</span>
              </TableActionButton>
              <TableActionButton
                label={store.resettingKey === budgetKey(item)
                  ? "Resetting budget"
                  : "Reset budget"}
                class="budget-action-btn budget-action-btn-warning"
                onclick={() => store.resetBudget(item)}
                disabled={store.resettingKey === budgetKey(item)}
              >
                <Icon icon={RotateCcw} class="budget-action-icon" />
                <span class="budget-action-label">
                  {store.resettingKey === budgetKey(item)
                    ? "Resetting"
                    : "Reset"}
                </span>
              </TableActionButton>
              <TableActionButton
                label={store.deletingKey === budgetKey(item)
                  ? "Deleting budget"
                  : "Delete budget"}
                class="table-action-btn-danger budget-action-btn"
                onclick={() => store.deleteBudget(item)}
                disabled={store.deletingKey === budgetKey(item)}
              >
                <Icon icon={Trash2} class="budget-action-icon" />
                <span class="budget-action-label">
                  {store.deletingKey === budgetKey(item)
                    ? "Deleting"
                    : "Delete"}
                </span>
              </TableActionButton>
            </div>
          </div>
        </div>
        <div class="budget-bars">
          {#if item.per_child}
            <div class="per-child-summary">
              Usage is tracked independently for each direct child of <code
                >{budgetSubject(item)}</code
              >. Query <code>/v1/usage</code> as a child to inspect its budget.
            </div>
          {:else}
            <div class="budget-bar-line">
              <div class="budget-bar-label">
                <span>Usage</span>
                <span class="budget-bar-percent"
                  >{budgetUsagePercentLabel(item)}</span
                >
              </div>
              <div
                class="budget-bar-track"
                role="progressbar"
                aria-valuemin="0"
                aria-valuemax="100"
                aria-valuenow={budgetUsagePercent(item)}
                aria-label={"Budget usage: " +
                  formatCost(item.spent) +
                  " of " +
                  formatCost(item.amount) +
                  ", " +
                  budgetRemainingLabel(item)}
                style="--budget-progress: {budgetUsagePercent(item)}%"
              >
                <div
                  class="budget-bar-fill budget-bar-fill-usage"
                  class:budget-bar-fill-danger={budgetUsageRatio(item) >= 1}
                  style="width: var(--budget-progress)"
                ></div>
                <span class="budget-bar-text-row">
                  <span class="budget-bar-text budget-bar-text-center">
                    {formatCost(item.spent) + " of " + formatCost(item.amount)}
                  </span>
                  <span class="budget-bar-text budget-bar-text-end">
                    {budgetRemainingLabel(item)}
                  </span>
                </span>
                <span
                  class="budget-bar-text-row budget-bar-text-row-on-fill"
                  aria-hidden="true"
                >
                  <span class="budget-bar-text budget-bar-text-center">
                    {formatCost(item.spent) + " of " + formatCost(item.amount)}
                  </span>
                  <span class="budget-bar-text budget-bar-text-end">
                    {budgetRemainingLabel(item)}
                  </span>
                </span>
              </div>
            </div>
          {/if}
          <div class="budget-bar-line">
            <div class="budget-bar-label">
              <span>Period</span>
              <span class="budget-bar-percent"
                >{budgetPeriodPercentLabel(item)}</span
              >
            </div>
            <div
              class="budget-bar-track {budgetPeriodTrackClass(item)}"
              role="progressbar"
              aria-label="Budget period elapsed"
              aria-valuemin="0"
              aria-valuemax="100"
              aria-valuenow={budgetPeriodPercent(item)}
              style="--budget-progress: {budgetPeriodPercent(item)}%"
            >
              <div
                class="budget-bar-fill budget-bar-fill-period {budgetPeriodBarClass(
                  item,
                )}"
                style="width: var(--budget-progress)"
              ></div>
              <span class="budget-bar-text-row">
                <span
                  class="budget-bar-text budget-bar-text-start"
                  title={timestampTimeZoneTitle(item.period_start)}
                >
                  {timezone.formatTimestamp(item.period_start)}
                </span>
                <span class="budget-bar-text budget-bar-text-center">
                  {budgetPeriodDurationLabel(item)}
                </span>
                <span
                  class="budget-bar-text budget-bar-text-end"
                  title={timestampTimeZoneTitle(item.period_end)}
                >
                  {timezone.formatTimestamp(item.period_end)}
                </span>
              </span>
              <span
                class="budget-bar-text-row budget-bar-text-row-on-fill"
                aria-hidden="true"
              >
                <span class="budget-bar-text budget-bar-text-start">
                  {timezone.formatTimestamp(item.period_start)}
                </span>
                <span class="budget-bar-text budget-bar-text-center">
                  {budgetPeriodDurationLabel(item)}
                </span>
                <span class="budget-bar-text budget-bar-text-end">
                  {timezone.formatTimestamp(item.period_end)}
                </span>
              </span>
            </div>
          </div>
        </div>
      </div>
    </section>
  {/each}
</div>

<style>
  .per-child-summary {
    padding: 0.75rem 0.9rem;
    border: 1px solid var(--border);
    border-radius: 0.5rem;
    color: var(--text-muted);
    line-height: 1.45;
  }

  .budget-bar-text-row-on-fill {
    color: #fff;
    clip-path: inset(0 calc(100% - var(--budget-progress, 0%)) 0 0);
  }

  .budget-bar-text-start {
    left: 8px;
    transform: translateY(-50%);
  }

  /* budget-bar-track-period-* is composed dynamically; this must sit
     after the base text-row rule to win the cascade. */
  .budget-bar-track-period-custom .budget-bar-text-row-on-fill {
    color: #3f332a;
  }
</style>
