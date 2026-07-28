<script>
  // One month grid inside the DatePicker dropdown. `offset` is the month
  // shift from the picker's anchor month (-1 = left pane, 0 = right pane).
  import { dateRange } from "$lib/stores/dateRange.svelte.ts";
  import { timezone } from "$lib/stores/timezone.svelte.ts";
  import { calendarDays, calendarTitle, isSameMonth } from "./datePickerLogic.ts";

  let { calendarMonth, offset = 0, onprev, onnext, onselect } = $props();

  const days = $derived(
    calendarDays(calendarMonth, offset, (d) => timezone.dateToDateKey(d)),
  );
  // The right pane is the anchor month, so "next" is capped at the real month.
  const atCurrentMonth = $derived(isSameMonth(calendarMonth, timezone.todayDate()));

  const key = (day) => timezone.dateToDateKey(day.date);
  const isFuture = (day) => key(day) > timezone.currentDateKey();
  const isToday = (day) => day.current && key(day) === timezone.currentDateKey();

  function boundary(day, edge) {
    const at = edge === "start" ? dateRange.rangeStart() : dateRange.rangeEnd();
    return day.current && !!at && key(day) === timezone.dateToDateKey(at);
  }

  function inRange(day) {
    const start = dateRange.rangeStart();
    const end = dateRange.rangeEnd();
    if (!day.current || !start || !end) return false;
    return (
      key(day) >= timezone.dateToDateKey(start) &&
      key(day) <= timezone.dateToDateKey(end)
    );
  }
</script>

<div class="dp-calendar">
  <div class="dp-cal-header">
    <button
      class="dp-nav-btn"
      class:dp-nav-prev-mobile={offset !== -1}
      onclick={onprev}
      aria-label="Previous month"
    >
      <svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M8.5 3.5L5 7l3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
    </button>
    <span class="dp-cal-title">{calendarTitle(calendarMonth, offset)}</span>
    {#if offset === -1}
      <span></span>
    {:else}
      <button
        class="dp-nav-btn"
        onclick={onnext}
        disabled={atCurrentMonth}
        aria-label="Next month"
      >
        <svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M5.5 3.5L9 7l-3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
      </button>
    {/if}
  </div>
  <div class="dp-weekdays">
    <span>Mo</span><span>Tu</span><span>We</span><span>Th</span><span>Fr</span><span>Sa</span><span>Su</span>
  </div>
  <div class="dp-days">
    {#each days as day (day.key)}
      <button
        class="dp-day"
        class:other-month={!day.current}
        class:today={isToday(day)}
        class:range-start={boundary(day, "start")}
        class:range-end={boundary(day, "end")}
        class:in-range={inRange(day)}
        class:disabled={isFuture(day)}
        disabled={isFuture(day) || !day.current}
        onclick={() => onselect?.(day)}
      >
        {day.day}
      </button>
    {/each}
  </div>
</div>

<style>
  .dp-calendar {
    width: 224px;
    flex-shrink: 0;
  }

  .dp-cal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 8px;
    padding: 0 4px;
  }

  .dp-cal-title {
    font-size: 13px;
    font-weight: 600;
  }

  .dp-nav-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    background: transparent;
    border: none;
    border-radius: 6px;
    color: var(--text-muted);
    cursor: pointer;
    transition: all 0.15s;
  }

  .dp-nav-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text);
  }

  .dp-nav-btn:disabled {
    opacity: 0.3;
    cursor: default;
    pointer-events: none;
  }

  /* The right pane owns the only "previous" button once the left pane is
     hidden on narrow screens. */
  .dp-nav-prev-mobile {
    display: none;
  }

  .dp-weekdays {
    display: grid;
    grid-template-columns: repeat(7, 1fr);
    text-align: center;
    margin-bottom: 4px;
  }

  .dp-weekdays span {
    font-size: 11px;
    font-weight: 600;
    color: var(--text-muted);
    padding: 4px 0;
  }

  .dp-days {
    display: grid;
    grid-template-columns: repeat(7, 1fr);
    gap: 1px;
  }

  .dp-day {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    background: transparent;
    border: none;
    border-radius: 6px;
    color: var(--text);
    font-size: 12px;
    font-family: inherit;
    cursor: pointer;
    transition: all 0.1s;
  }

  .dp-day:hover:not(.disabled):not(.other-month) {
    background: var(--bg-surface-hover);
  }

  .dp-day.other-month {
    color: var(--text-muted);
    opacity: 0.3;
    cursor: default;
  }

  .dp-day.today {
    color: var(--accent);
    font-weight: 700;
    box-shadow: inset 0 0 0 1.5px var(--accent);
  }

  .dp-day.in-range {
    background: color-mix(in srgb, var(--accent) 15%, transparent);
    border-radius: 0;
  }

  .dp-day.range-start {
    background: var(--accent);
    color: #fff;
    border-radius: 6px 0 0 6px;
    font-weight: 600;
  }

  .dp-day.range-end {
    background: var(--accent);
    color: #fff;
    border-radius: 0 6px 6px 0;
    font-weight: 600;
  }

  .dp-day.range-start.range-end {
    border-radius: 6px;
  }

  .dp-day.range-start.today,
  .dp-day.range-end.today {
    box-shadow: none;
  }

  .dp-day.disabled {
    color: var(--text-muted);
    opacity: 0.3;
    cursor: default;
    pointer-events: none;
  }

  @media (max-width: 768px) {
    .dp-nav-prev-mobile {
        display: flex;
      }
  }
</style>
