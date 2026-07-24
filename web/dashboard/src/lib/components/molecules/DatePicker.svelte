<script>
  // Date-range picker over the shared dateRange store. onchange fires when
  // the window changes so the host page can refetch its data.
  import { dateRange } from "$lib/stores/dateRange.svelte.js";
  import { timezone } from "$lib/stores/timezone.svelte.js";

  let { onchange } = $props();

  let open = $state(false);
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
      if (rootEl && !rootEl.contains(event.target)) {
        close();
      }
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

  function selectionHint() {
    return selectingDate === "end" ? "Select end date" : "Select start date";
  }

  function calendarTitle(offset) {
    const d = new Date(
      Date.UTC(
        calendarMonth.getUTCFullYear(),
        calendarMonth.getUTCMonth() + offset,
        1,
      ),
    );
    const months = [
      "January",
      "February",
      "March",
      "April",
      "May",
      "June",
      "July",
      "August",
      "September",
      "October",
      "November",
      "December",
    ];
    return months[d.getUTCMonth()] + " " + d.getUTCFullYear();
  }

  function calendarDays(offset) {
    const year = calendarMonth.getUTCFullYear();
    const month = calendarMonth.getUTCMonth() + offset;
    const first = new Date(Date.UTC(year, month, 1));
    const last = new Date(Date.UTC(year, month + 1, 0));
    const startDay = (first.getUTCDay() + 6) % 7;
    const days = [];

    const prevLast = new Date(Date.UTC(year, month, 0));
    for (let i = startDay - 1; i >= 0; i--) {
      const d = prevLast.getUTCDate() - i;
      const date = new Date(Date.UTC(year, month - 1, d));
      days.push({
        day: d,
        date,
        current: false,
        key: "p-" + timezone.dateToDateKey(date),
      });
    }
    for (let d = 1; d <= last.getUTCDate(); d++) {
      const date = new Date(Date.UTC(year, month, d));
      days.push({
        day: d,
        date,
        current: true,
        key: "c-" + timezone.dateToDateKey(date),
      });
    }
    const remaining = 42 - days.length;
    for (let d = 1; d <= remaining; d++) {
      const date = new Date(Date.UTC(year, month + 1, d));
      days.push({
        day: d,
        date,
        current: false,
        key: "n-" + timezone.dateToDateKey(date),
      });
    }
    return days;
  }

  function prevMonth() {
    calendarMonth = new Date(
      Date.UTC(calendarMonth.getUTCFullYear(), calendarMonth.getUTCMonth() - 1, 1),
    );
  }

  function nextMonth() {
    const next = new Date(
      Date.UTC(calendarMonth.getUTCFullYear(), calendarMonth.getUTCMonth() + 1, 1),
    );
    const currentMonth = timezone.startOfMonthDate(timezone.todayDate());
    if (next.getTime() <= currentMonth.getTime()) {
      calendarMonth = next;
    }
  }

  function isCurrentMonth() {
    const today = timezone.todayDate();
    return (
      calendarMonth.getUTCFullYear() === today.getUTCFullYear() &&
      calendarMonth.getUTCMonth() === today.getUTCMonth()
    );
  }

  function selectCalendarDay(day) {
    if (!day.current || isFutureDay(day)) return;
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
    } else {
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
  }

  function isToday(day) {
    if (!day.current) return false;
    return timezone.dateToDateKey(day.date) === timezone.currentDateKey();
  }

  function isFutureDay(day) {
    return timezone.dateToDateKey(day.date) > timezone.currentDateKey();
  }

  function isRangeStart(day) {
    if (!day.current) return false;
    const start = dateRange.rangeStart();
    if (!start) return false;
    return timezone.dateToDateKey(day.date) === timezone.dateToDateKey(start);
  }

  function isRangeEnd(day) {
    if (!day.current) return false;
    const end = dateRange.rangeEnd();
    if (!end) return false;
    return timezone.dateToDateKey(day.date) === timezone.dateToDateKey(end);
  }

  function isInRange(day) {
    if (!day.current) return false;
    const start = dateRange.rangeStart();
    const end = dateRange.rangeEnd();
    if (!start || !end) return false;
    const dayKey = timezone.dateToDateKey(day.date);
    return (
      dayKey >= timezone.dateToDateKey(start) &&
      dayKey <= timezone.dateToDateKey(end)
    );
  }
</script>

{#snippet calendar(offset)}
  <div class="dp-calendar">
    <div class="dp-cal-header">
      {#if offset === -1}
        <button class="dp-nav-btn" onclick={prevMonth} aria-label="Previous month">
          <svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M8.5 3.5L5 7l3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
        <span class="dp-cal-title">{calendarTitle(offset)}</span>
        <span></span>
      {:else}
        <button class="dp-nav-btn dp-nav-prev-mobile" onclick={prevMonth} aria-label="Previous month">
          <svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M8.5 3.5L5 7l3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
        <span class="dp-cal-title">{calendarTitle(offset)}</span>
        <button class="dp-nav-btn" onclick={nextMonth} disabled={isCurrentMonth()} aria-label="Next month">
          <svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M5.5 3.5L9 7l-3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
      {/if}
    </div>
    <div class="dp-weekdays">
      <span>Mo</span><span>Tu</span><span>We</span><span>Th</span><span>Fr</span><span>Sa</span><span>Su</span>
    </div>
    <div class="dp-days">
      {#each calendarDays(offset) as day (day.key)}
        <button
          class="dp-day"
          class:other-month={!day.current}
          class:today={isToday(day)}
          class:range-start={isRangeStart(day)}
          class:range-end={isRangeEnd(day)}
          class:in-range={isInRange(day)}
          class:disabled={isFutureDay(day)}
          disabled={isFutureDay(day) || !day.current}
          onclick={() => selectCalendarDay(day)}
        >
          {day.day}
        </button>
      {/each}
    </div>
  </div>
{/snippet}

<div class="date-picker" bind:this={rootEl}>
  <button class="date-picker-trigger" onclick={toggle}>
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M5 1v2M11 1v2M2 6h12M3 3h10a1 1 0 011 1v9a1 1 0 01-1 1H3a1 1 0 01-1-1V4a1 1 0 011-1z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
    <span>{dateRange.dateRangeLabel()}</span>
    <svg class="date-picker-chevron" class:open width="12" height="12" viewBox="0 0 12 12" fill="none"><path d="M3 4.5l3 3 3-3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
  </button>
  {#if open}
    <div class="date-picker-dropdown">
      <div class="date-picker-presets">
        {#each ["3", "7", "14", "30", "90"] as preset (preset)}
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
        {@render calendar(-1)}
        {@render calendar(0)}
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
      <span>{selectionHint()}</span>
    </div>
  {/if}
</div>
