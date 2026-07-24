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
    <h3>Activity</h3>
    {#if calendarState.loading && calendarState.data.length === 0}
      <Spinner size={14} label="Loading activity" />
    {/if}
    <SegmentedControl
      ariaLabel="Activity calendar mode"
      options={[
        { value: "tokens", label: "Tokens" },
        { value: "costs", label: "Costs" },
      ]}
      value={calendarState.mode}
      onchange={(mode) => (calendarState.mode = mode)}
    />
  </div>
  <div class="contribution-calendar-grid-wrapper">
    <div class="contribution-calendar-day-labels">
      <span></span>
      <span>Mon</span>
      <span></span>
      <span>Wed</span>
      <span></span>
      <span>Fri</span>
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
                class="contribution-calendar-cell {day.empty
                  ? 'empty'
                  : 'level-' + day.level}"
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
      <span>Less</span>
      {#each calendarLegendLevels() as lvl (lvl)}
        <div class="contribution-calendar-cell level-{lvl}"></div>
      {/each}
      <span>More</span>
    </div>
  </div>
</div>
{#if tooltip.show}
  <div
    class="contribution-calendar-tooltip"
    style="left: {tooltip.x}px; top: {tooltip.y - 40}px"
  >{tooltip.text}</div>
{/if}
