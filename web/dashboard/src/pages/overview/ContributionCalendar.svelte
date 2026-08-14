<script>
  // GitHub-style activity calendar over the trailing year, with a tokens /
  // costs mode toggle and a hover tooltip.
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { calendarState } from "./overviewState.svelte.js";
  import {
    buildCalendarGrid,
    calendarMonthLabels,
    calendarLegendLevels,
    calendarSummaryText,
    calendarTooltipText,
  } from "./calendarLogic.js";
  import * as m from "$lib/paraglide/messages.js";

  let tooltip = $state({ show: false, x: 0, y: 0, text: "" });

  const todayKey = $derived(timezone.currentDateKey());
  const weeks = $derived(
    buildCalendarGrid(calendarState.data, calendarState.mode, todayKey),
  );
  const monthLabels = $derived(calendarMonthLabels(todayKey));

  function showTooltip(event, day) {
    if (day.empty) return;
    tooltip = {
      show: true,
      x: event.clientX,
      y: event.clientY,
      text: calendarTooltipText(day, calendarState.mode),
    };
  }

  function hideTooltip() {
    tooltip = { show: false, x: 0, y: 0, text: "" };
  }
</script>

<div class="contribution-calendar-section">
  <div class="contribution-calendar-header">
    <h3>{m.overview_activity()}</h3>
    {#if calendarState.loading && calendarState.data.length === 0}
      <Spinner size={14} label={m.overview_loading_activity()} />
    {/if}
    <SegmentedControl
      ariaLabel={m.overview_activity_mode()}
      options={[
        { value: "tokens", label: m.overview_tokens() },
        { value: "costs", label: m.overview_costs() },
      ]}
      value={calendarState.mode}
      onchange={(mode) => (calendarState.mode = mode)}
    />
  </div>
  <div class="contribution-calendar-grid-wrapper">
    <div class="contribution-calendar-day-labels">
      <span></span>
      <span>{m.overview_weekday_mon()}</span>
      <span></span>
      <span>{m.overview_weekday_wed()}</span>
      <span></span>
      <span>{m.overview_weekday_fri()}</span>
      <span></span>
    </div>
    <div class="contribution-calendar-scroll">
      <div class="contribution-calendar-months">
        {#each monthLabels as ml (ml.key)}
          <span
            class="contribution-calendar-month-label"
            style="grid-column: {ml.col + 1} / span {ml.span}"
          >{ml.label}</span>
        {/each}
      </div>
      <div class="contribution-calendar-grid">
        {#each weeks as week, wi (wi)}
          <div class="contribution-calendar-week">
            {#each week as day, di (wi + "-" + di)}
              <div
                class={["contribution-calendar-cell", day.empty ? "empty" : `level-${day.level}`]}
                role="presentation"
                onmouseenter={(event) => showTooltip(event, day)}
                onmouseleave={hideTooltip}
              ></div>
            {/each}
          </div>
        {/each}
      </div>
    </div>
  </div>
  <div class="contribution-calendar-footer">
    <div class="contribution-calendar-meta">
      <span class="contribution-calendar-summary"
      >{calendarSummaryText(calendarState.data, calendarState.mode)}</span>
    </div>
    <div class="contribution-calendar-legend">
      <span>{m.overview_less()}</span>
      {#each calendarLegendLevels() as lvl (lvl)}
        <div class="contribution-calendar-cell level-{lvl}"></div>
      {/each}
      <span>{m.overview_more()}</span>
    </div>
  </div>
</div>
{#if tooltip.show}
  <div
    class="contribution-calendar-tooltip"
    style="left: {tooltip.x}px; top: {tooltip.y - 40}px"
  >{tooltip.text}</div>
{/if}

<style>
  .contribution-calendar-section {
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 24px;
    margin-top: 24px;
  }

  .contribution-calendar-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 16px;
  }

  .contribution-calendar-header :global(h3) {
    font-size: 16px;
    font-weight: 600;
  }

  .contribution-calendar-grid-wrapper {
    display: flex;
    gap: 8px;
  }

  .contribution-calendar-day-labels {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding-top: 22px;
  }

  .contribution-calendar-day-labels :global(span) {
    height: 13px;
    font-size: 10px;
    line-height: 13px;
    color: var(--text-muted);
    text-align: right;
    min-width: 28px;
  }

  .contribution-calendar-scroll {
    flex: 1;
    overflow-x: auto;
    min-width: 0;
    --contribution-week-size: 13px;
    --contribution-week-gap: 2px;
  }

  .contribution-calendar-months {
    display: grid;
    grid-auto-columns: var(--contribution-week-size);
    grid-auto-flow: column;
    gap: var(--contribution-week-gap);
    margin-bottom: 6px;
    height: 16px;
  }

  .contribution-calendar-month-label {
    font-size: 10px;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .contribution-calendar-grid {
    display: flex;
    gap: var(--contribution-week-gap);
  }

  .contribution-calendar-week {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .contribution-calendar-cell {
    width: var(--contribution-week-size);
    height: var(--contribution-week-size);
    border-radius: 2px;
    background: var(--cal-level-0);
  }

  .contribution-calendar-cell.level-1 {
    background: var(--cal-level-1);
  }

  .contribution-calendar-cell.level-2 {
    background: var(--cal-level-2);
  }

  .contribution-calendar-cell.level-3 {
    background: var(--cal-level-3);
  }

  .contribution-calendar-cell.level-4 {
    background: var(--cal-level-4);
  }

  .contribution-calendar-cell.level-5 {
    background: var(--cal-level-5);
  }

  .contribution-calendar-cell.level-6 {
    background: var(--cal-level-6);
  }

  .contribution-calendar-cell.level-7 {
    background: var(--cal-level-7);
  }

  .contribution-calendar-cell.level-8 {
    background: var(--cal-level-8);
  }

  .contribution-calendar-cell.level-9 {
    background: var(--cal-level-9);
  }

  .contribution-calendar-cell.level-10 {
    background: var(--cal-level-10);
  }

  .contribution-calendar-cell.empty {
    background: transparent;
  }

  .contribution-calendar-footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-top: 12px;
    gap: 12px;
  }

  .contribution-calendar-meta {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .contribution-calendar-summary {
    font-size: 12px;
    color: var(--text-muted);
  }

  .contribution-calendar-legend {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 11px;
    color: var(--text-muted);
  }

  .contribution-calendar-legend .contribution-calendar-cell {
    width: 11px;
    height: 11px;
    cursor: default;
  }

  .contribution-calendar-tooltip {
    position: fixed;
    z-index: 200;
    background: var(--bg-surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 4px 8px;
    font-size: 12px;
    font-family: "SF Mono", Menlo, Consolas, monospace;
    white-space: nowrap;
    pointer-events: none;
    transform: translateX(-50%);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.2);
  }

  @media (max-width: 768px) {
    /* Contribution calendar mobile */
    .contribution-calendar-day-labels {
        display: none;
      }

    .contribution-calendar-section {
        padding: 16px;
      }

    .contribution-calendar-footer {
        align-items: flex-start;
        flex-direction: column;
      }
  }
</style>
