<script>
  // Date-range picker over the shared dateRange store. onchange fires when
  // the window changes so the host page can refetch its data.
  import { dateRange } from "$lib/stores/dateRange.svelte.js";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import DatePickerCalendar from "./DatePickerCalendar.svelte";
  import { shiftMonth } from "./datePickerLogic.js";

  let { onchange } = $props();

  const PRESETS = ["3", "7", "14", "30", "90"];

  let open = $state(false);
  // Which end of the range the next calendar click sets.
  let selectingDate = $state("start");
  let calendarMonth = $state(new Date());
  let cursorHint = $state({ show: false, x: 0, y: 0 });
  let rootEl = $state(null);

  function toggle() {
    open = !open;
    if (open) {
      calendarMonth = timezone.startOfMonthDate(
        dateRange.customEndDate || timezone.todayDate(),
      );
      selectingDate = "start";
    }
  }

  function close() {
    open = false;
    cursorHint = { show: false, x: 0, y: 0 };
  }

  $effect(() => {
    if (!open) return;
    const onDocClick = (event) => {
      if (rootEl && !rootEl.contains(event.target)) close();
    };
    const onKeydown = (event) => {
      if (event.key === "Escape") close();
    };
    document.addEventListener("click", onDocClick, true);
    window.addEventListener("keydown", onKeydown);
    return () => {
      document.removeEventListener("click", onDocClick, true);
      window.removeEventListener("keydown", onKeydown);
    };
  });

  function selectPreset(days) {
    dateRange.selectPreset(days);
    selectingDate = "start";
    onchange?.();
    close();
  }

  const prevMonth = () => (calendarMonth = shiftMonth(calendarMonth, -1));
  const nextMonth = () =>
    (calendarMonth = shiftMonth(
      calendarMonth,
      1,
      timezone.startOfMonthDate(timezone.todayDate()),
    ));

  // First click sets the start (and keeps the picker open for the end date);
  // second click sets the end, swapping the pair if it lands before the start.
  function selectCalendarDay(day) {
    const clicked = new Date(day.date);
    dateRange.selectedPreset = null;

    if (selectingDate === "start") {
      dateRange.customStartDate = clicked;
      if (dateRange.customEndDate && dateRange.customEndDate < clicked) {
        dateRange.customEndDate = clicked;
      }
      if (!dateRange.customEndDate) {
        dateRange.customEndDate = timezone.todayDate();
      }
      selectingDate = "end";
      onchange?.();
      return;
    }

    if (clicked < dateRange.customStartDate) {
      dateRange.customEndDate = dateRange.customStartDate;
      dateRange.customStartDate = clicked;
    } else {
      dateRange.customEndDate = clicked;
    }
    selectingDate = "start";
    onchange?.();
    close();
  }
</script>

<div class="date-picker" bind:this={rootEl}>
  <button class="date-picker-trigger" onclick={toggle}>
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M5 1v2M11 1v2M2 6h12M3 3h10a1 1 0 011 1v9a1 1 0 01-1 1H3a1 1 0 01-1-1V4a1 1 0 011-1z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
    <span>{dateRange.dateRangeLabel()}</span>
    <svg class="date-picker-chevron" class:open width="12" height="12" viewBox="0 0 12 12" fill="none"><path d="M3 4.5l3 3 3-3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
  </button>
  {#if open}
    <div class="date-picker-dropdown">
      <div class="date-picker-presets">
        {#each PRESETS as preset (preset)}
          <button
            class="preset-btn"
            class:active={dateRange.selectedPreset === preset}
            onclick={() => selectPreset(preset)}>Last {preset} days</button
          >
        {/each}
      </div>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        class="date-picker-calendars"
        onmousemove={(e) => (cursorHint = { show: true, x: e.clientX, y: e.clientY })}
        onmouseleave={() => (cursorHint = { show: false, x: 0, y: 0 })}
      >
        {#each [-1, 0] as offset (offset)}
          <DatePickerCalendar
            {calendarMonth}
            {offset}
            onprev={prevMonth}
            onnext={nextMonth}
            onselect={selectCalendarDay}
          />
        {/each}
      </div>
    </div>
  {/if}
  {#if cursorHint.show}
    <div class="dp-cursor-hint" style="left:{cursorHint.x}px;top:{cursorHint.y}px">
      {#if selectingDate === "start"}
        <svg viewBox="0 0 12 12" fill="none"><path d="M2 2v8M2 6h7M6.5 3.5L9 6 6.5 8.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
      {:else}
        <svg viewBox="0 0 12 12" fill="none"><path d="M10 2v8M3 6h7M5.5 3.5L3 6l2.5 2.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
      {/if}
      <span>{selectingDate === "end" ? "Select end date" : "Select start date"}</span>
    </div>
  {/if}
</div>

<style>
  .date-picker {
    position: relative;
  }

  .date-picker-trigger {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    font-size: 13px;
    font-family: inherit;
    cursor: pointer;
    transition: all 0.15s;
    white-space: nowrap;
  }

  .date-picker-trigger:hover {
    background: var(--bg-surface-hover);
  }

  .date-picker-trigger svg {
    flex-shrink: 0;
    color: var(--text-muted);
  }

  .date-picker-chevron {
    transition: transform 0.15s;
  }

  .date-picker-chevron.open {
    transform: rotate(180deg);
  }

  .date-picker-dropdown {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    z-index: 100;
    display: flex;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
    overflow: hidden;
  }

  .date-picker-presets {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 8px;
    border-right: 1px solid var(--border);
    min-width: 140px;
  }

  .preset-btn {
    padding: 8px 12px;
    background: transparent;
    border: none;
    border-radius: 6px;
    color: var(--text);
    font-size: 13px;
    font-family: inherit;
    text-align: left;
    cursor: pointer;
    transition: all 0.15s;
    white-space: nowrap;
  }

  .preset-btn:hover {
    background: var(--bg-surface-hover);
  }

  .preset-btn.active {
    background: var(--accent);
    color: #fff;
  }

  .date-picker-calendars {
    display: flex;
    flex-wrap: nowrap;
    padding: 12px;
    gap: 16px;
  }

  .dp-cursor-hint {
    position: fixed;
    pointer-events: none;
    z-index: 101;
    background: var(--bg-surface);
    color: var(--text);
    border: 1px solid var(--accent);
    font-size: 11px;
    font-weight: 500;
    padding: 3px 8px;
    border-radius: 4px;
    white-space: nowrap;
    transform: translate(12px, -50%);
    display: flex;
    align-items: center;
    gap: 5px;
  }

  .dp-cursor-hint svg {
    width: 12px;
    height: 12px;
    color: var(--accent);
    flex-shrink: 0;
  }

  @media (max-width: 768px) {
    /* Dock the dropdown to the bottom of the viewport and show one month. */
    .date-picker-dropdown {
        flex-direction: column;
        right: 0;
        left: 0;
        position: fixed;
        top: auto;
        bottom: 0;
        border-radius: var(--radius) var(--radius) 0 0;
        max-height: 80vh;
        overflow-y: auto;
      }

    .date-picker-presets {
        flex-direction: row;
        flex-wrap: nowrap;
        overflow-x: auto;
        -webkit-overflow-scrolling: touch;
        border-right: none;
        border-bottom: 1px solid var(--border);
        min-width: 0;
      }

    .date-picker-calendars {
        justify-content: center;
      }

    /* The calendar is a child component, hence :global. */
    .date-picker-calendars > :global(.dp-calendar:first-child) {
        display: none;
      }
  }
</style>
