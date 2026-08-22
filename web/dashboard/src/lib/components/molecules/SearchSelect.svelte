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
  //   disabled, mono (monospace option values), class (trigger wrapper)
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { dismissOnOutside, marqueeOnOverflow } from "$lib/utils/attachments.js";
  import { Check, ChevronDown, Search } from "lucide";
  import { tick } from "svelte";
  import * as m from "$lib/paraglide/messages.js";
  import {
    customSearchValue,
    filterSearchOptions,
    moveActiveIndex,
    normalizeSearchOption,
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
    disabled = false,
    mono = false,
    class: className = "",
  } = $props();

  const uid = $props.id();
  const listID = uid + "-search-select-list";

  let open = $state(false);
  let query = $state("");
  let activeIndex = $state(-1);
  let searchEl = $state(null);
  let listEl = $state(null);

  const filtered = $derived(filterSearchOptions(options, query));
  const customValue = $derived(customSearchValue(options, query, allowCustom));
  // Rows in keyboard order: the custom suggestion first, then matches.
  const rowCount = $derived(filtered.length + (customValue ? 1 : 0));
  const selected = $derived(
    options.map(normalizeSearchOption).find((option) => option && option.value === value) ||
      (value ? { value, label: value, description: "" } : null),
  );

  function rowValue(index) {
    if (customValue) return index === 0 ? customValue : filtered[index - 1]?.value;
    return filtered[index]?.value;
  }

  async function openList() {
    if (disabled) return;
    open = true;
    query = "";
    const current = filtered.findIndex((option) => option.value === value);
    activeIndex = current >= 0 ? current : -1;
    await tick();
    searchEl?.focus();
    scrollActiveIntoView();
  }

  function close() {
    open = false;
    query = "";
    activeIndex = -1;
  }

  function toggle() {
    if (open) close();
    else openList();
  }

  function choose(next) {
    if (next === undefined || next === null) return;
    value = String(next);
    onchange?.(value);
    close();
  }

  function scrollActiveIntoView() {
    const row = listEl?.querySelector(".search-select-option-active");
    row?.scrollIntoView?.({ block: "nearest" });
  }

  async function onSearchKeydown(event) {
    switch (event.key) {
      case "ArrowDown":
      case "ArrowUp":
        event.preventDefault();
        activeIndex = moveActiveIndex(activeIndex, event.key === "ArrowDown" ? 1 : -1, rowCount);
        await tick();
        scrollActiveIntoView();
        break;
      case "Enter":
        event.preventDefault();
        if (activeIndex >= 0) choose(rowValue(activeIndex));
        else if (rowCount > 0) choose(rowValue(0));
        break;
      case "Tab":
        close();
        break;
      case "Escape":
        // Dismiss only the listbox; a parent dialog must not treat this
        // Escape as its own.
        event.preventDefault();
        event.stopPropagation();
        close();
        break;
      default:
        break;
    }
  }

  // Typing resets the highlight to the best match.
  $effect(() => {
    void query;
    activeIndex = rowCount > 0 ? 0 : -1;
  });
</script>

<div
  class={["search-select", className]}
  class:search-select-open={open}
  {@attach open ? dismissOnOutside(close) : undefined}
>
  <button
    type="button"
    {id}
    class="search-select-trigger"
    class:mono
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-controls={open ? listID : undefined}
    aria-label={ariaLabel || undefined}
    {disabled}
    onclick={toggle}
    onkeydown={(event) => {
      if (!open && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
        event.preventDefault();
        openList();
      }
    }}
  >
    <span
      class="search-select-value search-select-clip"
      class:search-select-placeholder={!selected}
      title={selected ? [selected.label, selected.description].filter(Boolean).join(" — ") : undefined}
      {@attach marqueeOnOverflow()}
    >
      <span class="search-select-text">{selected ? selected.label : placeholder}</span>
    </span>
    <Icon icon={ChevronDown} class="search-select-chevron" />
  </button>

  {#if open}
    <div class="search-select-popover">
      <div class="search-select-search">
        <Icon icon={Search} class="search-select-search-icon" />
        <input
          type="text"
          class="search-select-search-input"
          role="combobox"
          aria-autocomplete="list"
          aria-expanded="true"
          aria-controls={listID}
          aria-activedescendant={activeIndex >= 0 ? listID + "-" + activeIndex : undefined}
          aria-label={searchPlaceholder || m.search_select_search_placeholder()}
          placeholder={searchPlaceholder || m.search_select_search_placeholder()}
          autocomplete="off"
          spellcheck="false"
          bind:this={searchEl}
          bind:value={query}
          onkeydown={onSearchKeydown}
        />
      </div>
      <ul class="search-select-list" id={listID} role="listbox" bind:this={listEl}>
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
            class:search-select-option-current={option.value === value}
            class:search-select-option-active={activeIndex === row}
            role="option"
            aria-selected={option.value === value}
            onmouseenter={() => (activeIndex = row)}
            onclick={() => choose(option.value)}
          >
            <span
              class="search-select-option-label search-select-clip"
              title={option.label}
              {@attach marqueeOnOverflow()}
            >
              <span class="search-select-text">{option.label}</span>
            </span>
            {#if option.description}
              <span class="search-select-option-description">{option.description}</span>
            {/if}
            {#if option.value === value}
              <Icon icon={Check} class="search-select-check" />
            {/if}
          </li>
        {/each}
        {#if rowCount === 0}
          <li class="search-select-empty" role="presentation">{m.search_select_no_matches()}</li>
        {/if}
      </ul>
    </div>
  {/if}
</div>

<style>
  .search-select {
    position: relative;
    display: inline-flex;
    min-width: 0;
  }

  .search-select-trigger {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    min-width: 0;
    min-height: 32px;
    padding: 5px 8px 5px 10px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    font-size: 12px;
    font-family: inherit;
    text-align: left;
    cursor: pointer;
    transition: border-color 0.15s, background 0.15s;
  }

  .search-select-trigger.mono {
    font-family: 'SF Mono', Menlo, Consolas, monospace;
  }

  .search-select-trigger:hover:not(:disabled),
  .search-select-open .search-select-trigger {
    border-color: var(--accent);
  }

  .search-select-trigger:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .search-select-value {
    flex: 1 1 auto;
    min-width: 0;
  }

  /* Clipped labels show an ellipsis at rest; on hover marqueeOnOverflow
     marks the ones that really overflow and the text glides to the far end
     and back so the whole name can be read. */
  .search-select-clip {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .search-select-clip:global(.marquee-active) {
    text-overflow: clip;
  }

  .search-select-clip:global(.marquee-active) .search-select-text {
    display: inline-block;
    animation: search-select-marquee var(--marquee-duration, 3s) ease-in-out infinite alternate;
  }

  @keyframes search-select-marquee {
    0%,
    12% {
      transform: translateX(0);
    }
    88%,
    100% {
      transform: translateX(var(--marquee-shift, 0));
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .search-select-clip:global(.marquee-active) .search-select-text {
      animation: none;
      transform: translateX(var(--marquee-shift, 0));
      transition: transform 0.6s ease;
    }
  }

  .search-select-placeholder {
    color: var(--text-muted);
  }

  /* Icon classes render inside the Icon atom's <svg>, hence :global. */
  .search-select :global(.search-select-chevron) {
    flex-shrink: 0;
    width: 14px;
    height: 14px;
    color: var(--text-muted);
    transition: transform 0.15s;
  }

  .search-select-open :global(.search-select-chevron) {
    transform: rotate(180deg);
  }

  .search-select-popover {
    position: absolute;
    top: calc(100% + 6px);
    left: 0;
    z-index: 100;
    display: flex;
    flex-direction: column;
    width: max(100%, 280px);
    max-width: min(480px, 90vw);
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
    overflow: hidden;
  }

  .search-select-search {
    position: relative;
    display: flex;
    padding: 8px;
    border-bottom: 1px solid var(--border);
  }

  .search-select :global(.search-select-search-icon) {
    position: absolute;
    top: 50%;
    left: 18px;
    width: 14px;
    height: 14px;
    color: var(--text-muted);
    pointer-events: none;
    transform: translateY(-50%);
  }

  .search-select-search-input {
    width: 100%;
    min-width: 0;
    padding: 7px 10px 7px 30px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text);
    font-size: 13px;
    font-family: inherit;
  }

  .search-select-search-input:focus {
    border-color: var(--accent);
    outline: none;
  }

  .search-select-list {
    max-height: 280px;
    margin: 0;
    padding: 6px;
    list-style: none;
    overflow-y: auto;
  }

  .search-select-option {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 7px 10px;
    border-radius: 6px;
    color: var(--text);
    font-size: 12px;
    cursor: pointer;
  }

  .search-select-option.mono {
    font-family: 'SF Mono', Menlo, Consolas, monospace;
  }

  /* Keyboard/hover highlight; the committed value is aria-selected and
     announced through aria-activedescendant on the search input. */
  .search-select-option-active {
    background: var(--bg-surface-hover);
  }

  .search-select-option-current {
    color: var(--accent);
    font-weight: 600;
  }

  .search-select-option-custom {
    color: var(--accent);
    font-style: italic;
  }

  .search-select-option-label {
    flex: 1 1 auto;
    min-width: 0;
  }

  .search-select-option-description {
    flex: 0 1 auto;
    color: var(--text-muted);
    font-family: inherit;
    font-size: 11px;
    white-space: nowrap;
  }

  .search-select :global(.search-select-check) {
    flex-shrink: 0;
    width: 14px;
    height: 14px;
  }

  .search-select-empty {
    padding: 12px 10px;
    color: var(--text-muted);
    font-size: 12px;
    text-align: center;
  }
</style>
