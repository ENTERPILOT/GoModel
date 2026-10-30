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
  import { marqueeOnOverflow } from "$lib/utils/attachments.js";
  import { Check } from "lucide";
  import { tick } from "svelte";
  import * as m from "$lib/paraglide/messages.js";
  import SearchPopover from "./SearchPopover.svelte";
  import {
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
  let activeIndex = $state(-1);
  let shell = $state(null);

  // Multi-select rows list the current selection first so it can be
  // unchecked without hunting through the list.
  const filtered = $derived(
    multiple ? selectedFirst(filterSearchOptions(options, query), values) : filterSearchOptions(options, query),
  );
  const customValue = $derived(customSearchValue(options, query, allowCustom));
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
        <span class="search-select-text">{option.label}</span>
      </span>
      {#if option.description}
        <span class="search-select-option-description">{option.description}</span>
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
