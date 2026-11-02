<script>
  // Searchable dropdown (combobox) for picking one value from a longer list —
  // models, providers, keys. The trigger shows the current selection; opening
  // it reveals a search box with the filtered list underneath.
  //
  // Props:
  //   options      array of {value, label?, description?} (or plain strings)
  //   value        selected value (bindable); may be a value outside `options`
  //   onchange     (value) => void, fired on every selection
  //   placeholder  trigger text when nothing is selected
  //   searchPlaceholder, ariaLabel, id
  //   allowCustom  offer "Use “<typed text>”" so free-form values still work
  //   multiple     multi-select: `values` (bindable array) replaces `value`,
  //                picks toggle membership and keep the list open, the trigger
  //                lists the selection comma-separated, a typed custom entry
  //                may hold several comma-separated values, and Backspace in
  //                an empty search removes the last value
  //   values       selected values in multiple mode (bindable)
  //   disabled, mono (monospace option values), class (trigger wrapper)
  import Icon from "$lib/components/atoms/Icon.svelte";
  import HighlightText from "$lib/components/atoms/HighlightText.svelte";
  import { marqueeOnOverflow } from "$lib/utils/attachments.js";
  import { Check } from "lucide";
  import { tick } from "svelte";
  import * as m from "$lib/paraglide/messages.js";
  import SearchPopover from "./SearchPopover.svelte";
  import {
    MAX_VISIBLE_OPTIONS,
    capSearchOptions,
    customSearchValue,
    filterSearchOptions,
    normalizeSearchOption,
    selectedFirst,
    splitSearchValues,
    toggleSearchValue,
  } from "./searchSelectLogic.js";

  let {
    options = [],
    value = $bindable(""),
    onchange,
    placeholder = "",
    searchPlaceholder = "",
    ariaLabel = "",
    id = undefined,
    allowCustom = false,
    multiple = false,
    values = $bindable([]),
    disabled = false,
    mono = false,
    class: className = "",
  } = $props();

  const uid = $props.id();
  const listID = uid + "-search-select-list";

  let open = $state(false);
  let query = $state("");
  // What the list filters on. Typing must never wait for the list: `query`
  // updates with the input so the caret and the text never lag, and the filter
  // runs on the next frame with its latest value, at most once per frame.
  // Without this every keystroke filters and rebuilds rows inside the input
  // handler, and a fast typist's events queue behind that work.
  let listQuery = $state("");
  let activeIndex = $state(-1);
  let shell = $state(null);

  $effect(() => {
    const pending = query;
    if (typeof requestAnimationFrame !== "function") {
      listQuery = pending;
      return;
    }
    const frame = requestAnimationFrame(() => {
      listQuery = pending;
    });
    return () => cancelAnimationFrame(frame);
  });

  // A shrinking list must not leave the highlight past its end, or Enter
  // would resolve to no row at all.
  $effect(() => {
    const count = rowCount;
    if (activeIndex >= count) activeIndex = count > 0 ? count - 1 : -1;
  });

  // Multi-select rows list the current selection first so it can be
  // unchecked without hunting through the list.
  const matched = $derived(
    multiple ? selectedFirst(filterSearchOptions(options, listQuery), values) : filterSearchOptions(options, listQuery),
  );
  // Only the first page of matches is rendered: the model picker holds ~1000
  // options and rebuilding every row per keystroke blocked typing. A multi
  // select never cuts a chosen row, so its page grows with the selection.
  const limit = $derived(multiple ? Math.max(MAX_VISIBLE_OPTIONS, values.length) : MAX_VISIBLE_OPTIONS);
  const page = $derived(capSearchOptions(matched, limit, multiple ? "" : value));
  const filtered = $derived(page.visible);
  const hiddenCount = $derived(page.hidden);
  // Everything the list decides derives from `listQuery`, never from `query`:
  // mixing the two would let a keypress commit a row from the previous frame.
  const customValue = $derived(customSearchValue(options, listQuery, allowCustom));
  // Rows in keyboard order: the custom suggestion first, then matches.
  const rowCount = $derived(filtered.length + (customValue ? 1 : 0));
  const selected = $derived(
    options.map(normalizeSearchOption).find((option) => option && option.value === value) ||
      (value ? { value, label: value, description: "" } : null),
  );
  // In multiple mode the trigger lists every selected value; an option's
  // label is used when one exists, the raw value otherwise.
  const selectedList = $derived(
    multiple
      ? values.map((item) => {
          const option = options.map(normalizeSearchOption).find((entry) => entry && entry.value === item);
          return option ? option.label : item;
        })
      : [],
  );
  const hasSelection = $derived(multiple ? selectedList.length > 0 : Boolean(selected));
  const triggerText = $derived(
    multiple ? (selectedList.length ? selectedList.join(", ") : placeholder) : selected ? selected.label : placeholder,
  );
  const triggerTitle = $derived(
    multiple
      ? selectedList.length
        ? selectedList.join(", ")
        : undefined
      : selected
        ? [selected.label, selected.description].filter(Boolean).join(" — ")
        : undefined,
  );

  function isChosen(optionValue) {
    return multiple ? values.includes(optionValue) : optionValue === value;
  }

  function rowValue(index) {
    if (customValue) return index === 0 ? customValue : filtered[index - 1]?.value;
    return filtered[index]?.value;
  }

  // Opening highlights the current single value so Enter keeps it.
  function onopen() {
    listQuery = query;
    const current = multiple ? -1 : filtered.findIndex((option) => option.value === value);
    activeIndex = current >= 0 ? current : -1;
  }

  async function choose(next) {
    if (next === undefined || next === null) return;
    if (multiple) {
      // A custom entry may carry several comma-separated values; each one
      // toggles on its own. The list stays open for further picks.
      const pieces = customValue && String(next) === customValue ? splitSearchValues(next) : [String(next)];
      let updated = values;
      for (const piece of pieces) updated = toggleSearchValue(updated, piece);
      values = updated;
      onchange?.(values);
      shell?.notifyChanged();
      query = "";
      await tick();
      // The pick moves (selected values list first), so the highlight
      // follows it instead of landing on whatever took its old place.
      const moved = filtered.findIndex((option) => option.value === pieces[0]);
      if (moved >= 0) activeIndex = moved;
      shell?.place();
      shell?.focusSearch();
      return;
    }
    value = String(next);
    onchange?.(value);
    shell?.notifyChanged();
    shell?.close();
  }

  function onkeydown(event) {
    switch (event.key) {
      case "Enter":
        event.preventDefault();
        // The list trails the input by a frame on purpose. Committing from it
        // without catching up first would pick a row the user already filtered
        // away — typing an exact id and pressing Enter could select the model
        // that happened to sit first in the previous list.
        listQuery = query;
        if (activeIndex >= 0) choose(rowValue(activeIndex));
        else if (rowCount > 0) choose(rowValue(0));
        break;
      case "Backspace":
        // Backspace in an empty search removes the last value.
        if (multiple && !query && values.length) {
          event.preventDefault();
          values = values.slice(0, -1);
          onchange?.(values);
          shell?.notifyChanged();
        }
        break;
    }
  }
</script>

<SearchPopover
  bind:this={shell}
  bind:open
  bind:query
  bind:activeIndex
  {rowCount}
  notice={hiddenCount > 0
    ? m.search_select_more_results({ shown: Math.min(matched.length, limit), total: matched.length })
    : ""}
  {listID}
  {triggerText}
  {triggerTitle}
  {hasSelection}
  {multiple}
  {mono}
  {disabled}
  {ariaLabel}
  {searchPlaceholder}
  {id}
  class={className}
  {onopen}
  {onkeydown}
>
  {#if customValue}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <li
      id={listID + "-0"}
      class="search-select-option search-select-option-custom"
      class:mono
      role="option"
      class:search-select-option-active={activeIndex === 0}
      aria-selected="false"
      onmouseenter={() => (activeIndex = 0)}
      onclick={() => choose(customValue)}
    >
      <span class="search-select-option-label search-select-clip" {@attach marqueeOnOverflow()}>
        <span class="search-select-text">{m.search_select_use_custom({ value: customValue })}</span>
      </span>
    </li>
  {/if}
  {#each filtered as option, index (option.value)}
    {@const row = index + (customValue ? 1 : 0)}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <li
      id={listID + "-" + row}
      class="search-select-option"
      class:mono
      class:search-select-option-current={isChosen(option.value)}
      class:search-select-option-active={activeIndex === row}
      role="option"
      aria-selected={isChosen(option.value)}
      onmouseenter={() => (activeIndex = row)}
      onclick={() => choose(option.value)}
    >
      <span class="search-select-option-label search-select-clip" title={option.label} {@attach marqueeOnOverflow()}>
        <span class="search-select-text"><HighlightText text={option.label} needle={listQuery} /></span>
      </span>
      {#if option.description}
        <span class="search-select-option-description"><HighlightText text={option.description} needle={listQuery} /></span>
      {/if}
      {#if isChosen(option.value)}
        <Icon icon={Check} class="search-select-check" />
      {/if}
    </li>
  {/each}
  {#if rowCount === 0}
    <li class="search-select-empty" role="presentation">{m.search_select_no_matches()}</li>
  {/if}
</SearchPopover>
