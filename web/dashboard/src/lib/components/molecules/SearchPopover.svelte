<script>
  // Shared shell of the searchable pickers (SearchSelect, the usage user path
  // tree): the trigger button, the fixed popover placed next to it, the
  // search box and the listbox, plus outside-click dismissal and the common
  // keys (arrows move the highlight, Escape and Tab close). The rows are the
  // caller's children; style them with the search-select-option classes.
  //
  // Props:
  //   open, query, activeIndex   bindable picker state
  //   rowCount                   rows in keyboard order (for the arrows)
  //   listID                     listbox id; row i is listID + "-" + i
  //   triggerText, triggerTitle, hasSelection, multiple, mono, disabled
  //   ariaLabel, searchPlaceholder, id, class
  //   onopen()                   runs as the list opens (set the highlight)
  //   onkeydown(event)           runs first for every search-box key; call
  //                              preventDefault() to skip the shell's handling
  // Methods (bind:this): close(), focusSearch(), place(), notifyChanged().
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { dismissOnOutside, followViewport, marqueeOnOverflow } from "$lib/utils/attachments.js";
  import { ChevronDown, Search } from "lucide";
  import { tick, untrack } from "svelte";
  import * as m from "$lib/paraglide/messages.js";
  import { moveActiveIndex, popoverPlacement } from "./searchSelectLogic.js";

  let {
    open = $bindable(false),
    query = $bindable(""),
    activeIndex = $bindable(-1),
    rowCount = 0,
    listID,
    triggerText = "",
    triggerTitle = undefined,
    hasSelection = false,
    multiple = false,
    mono = false,
    disabled = false,
    ariaLabel = "",
    searchPlaceholder = "",
    id = undefined,
    class: className = "",
    onopen,
    onkeydown,
    children,
  } = $props();

  let rootEl = $state(null);
  let triggerEl = $state(null);
  let searchEl = $state(null);
  // The popover is fixed-positioned from the trigger's viewport rect, so it
  // is never clipped by a scrolling ancestor such as an editor dialog.
  let popoverStyle = $state("");

  // The visible area the popover must fit in. On phones the visual viewport
  // is what is left above the on-screen keyboard; window.innerHeight still
  // measures the full layout viewport.
  function visibleViewport() {
    const vv = window.visualViewport;
    return vv
      ? { top: vv.offsetTop, height: vv.height, width: window.innerWidth }
      : { top: 0, height: window.innerHeight, width: window.innerWidth };
  }

  // Re-run on every scroll and viewport change while open (followViewport),
  // so the popover tracks its trigger instead of closing when a phone
  // keyboard opens for the search box; it closes only once the trigger has
  // left the visible area.
  export function place() {
    const rect = rootEl?.getBoundingClientRect();
    if (!rect) return;
    const style = popoverPlacement(rect, visibleViewport());
    if (style === null) close();
    else popoverStyle = style;
  }

  export function close() {
    open = false;
    query = "";
    activeIndex = -1;
  }

  export function focusSearch() {
    searchEl?.focus();
  }

  // Selection changes are programmatic, so no DOM change event fires on its
  // own. Dispatch a bubbling one so enclosing forms (the editor dialog's
  // unsaved-changes guard) treat picks like native field edits.
  export function notifyChanged() {
    rootEl?.dispatchEvent(new Event("change", { bubbles: true }));
  }

  async function openList() {
    if (disabled) return;
    place();
    open = true;
    query = "";
    activeIndex = -1;
    onopen?.();
    await tick();
    searchEl?.focus();
    scrollActiveIntoView();
  }

  function scrollActiveIntoView() {
    document.getElementById(listID + "-" + activeIndex)?.scrollIntoView?.({ block: "nearest" });
  }

  async function onSearchKeydown(event) {
    onkeydown?.(event);
    if (event.defaultPrevented) return;
    switch (event.key) {
      case "ArrowDown":
      case "ArrowUp":
        event.preventDefault();
        activeIndex = moveActiveIndex(activeIndex, event.key === "ArrowDown" ? 1 : -1, rowCount);
        await tick();
        scrollActiveIntoView();
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
        // Closing removes the focused search box; hand focus back to the
        // trigger so keyboard users stay in place.
        triggerEl?.focus();
        break;
    }
  }

  // Typing resets the highlight to the best match; rows appearing for other
  // reasons (a tree node expanding) keep it where it is.
  $effect(() => {
    void query;
    activeIndex = untrack(() => rowCount) > 0 ? 0 : -1;
  });
</script>

<div
  class={["search-select", className]}
  class:search-select-open={open}
  bind:this={rootEl}
  {@attach open ? dismissOnOutside(close) : undefined}
  {@attach open ? followViewport(place) : undefined}
>
  <button
    type="button"
    {id}
    bind:this={triggerEl}
    class="search-select-trigger"
    class:mono
    class:search-select-trigger-multiple={multiple}
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-controls={open ? listID : undefined}
    aria-label={ariaLabel || undefined}
    {disabled}
    onclick={() => (open ? close() : openList())}
    onkeydown={(event) => {
      if (!open && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
        event.preventDefault();
        openList();
      }
    }}
  >
    <span
      class="search-select-value"
      class:search-select-clip={!multiple}
      class:search-select-wrap={multiple}
      class:search-select-placeholder={!hasSelection}
      title={triggerTitle}
      {@attach multiple ? undefined : marqueeOnOverflow()}
    >
      <span class="search-select-text">{triggerText}</span>
    </span>
    <Icon icon={ChevronDown} class="search-select-chevron" />
  </button>

  {#if open}
    <div class="search-select-popover" style={popoverStyle}>
      <div class="search-select-search">
        <Icon icon={Search} class="search-select-search-icon" />
        <!-- The query is local UI state, never saved; stopPropagation keeps
             its events from marking an enclosing form dirty. -->
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
          oninput={(event) => event.stopPropagation()}
          onchange={(event) => event.stopPropagation()}
        />
      </div>
      <ul class="search-select-list" id={listID} role="listbox" aria-multiselectable={multiple || undefined}>
        {@render children?.()}
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

  /* Multi-select triggers grow with the selection so every value stays
     readable instead of clipping to one line. */
  .search-select-trigger-multiple {
    height: auto;
  }

  .search-select-wrap {
    display: block;
    white-space: normal;
    overflow-wrap: anywhere;
    line-height: 1.5;
  }

  /* Clipped labels show an ellipsis at rest; on hover marqueeOnOverflow
     marks the ones that really overflow and the text glides to the far end
     and back so the whole name can be read. Rows use the same classes, so
     these reach into the caller's children through :global. */
  .search-select :global(.search-select-clip) {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .search-select :global(.search-select-clip.marquee-active) {
    text-overflow: clip;
  }

  .search-select :global(.search-select-clip.marquee-active .search-select-text) {
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
    .search-select :global(.search-select-clip.marquee-active .search-select-text) {
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
    position: fixed;
    z-index: 100;
    display: flex;
    flex-direction: column;
    width: max-content;
    min-width: 280px;
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
    /* Shrinks with the popover when the viewport leaves it less room. */
    min-height: 0;
    margin: 0;
    padding: 6px;
    list-style: none;
    overflow-y: auto;
  }

  .search-select :global(.search-select-option) {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 7px 10px;
    border-radius: 6px;
    color: var(--text);
    font-size: 12px;
    cursor: pointer;
  }

  .search-select :global(.search-select-option.mono) {
    font-family: 'SF Mono', Menlo, Consolas, monospace;
  }

  /* Keyboard/hover highlight; the committed value is aria-selected and
     announced through aria-activedescendant on the search input. */
  .search-select :global(.search-select-option-active) {
    background: var(--bg-surface-hover);
  }

  .search-select :global(.search-select-option-current) {
    color: var(--accent);
    font-weight: 600;
  }

  .search-select :global(.search-select-option-custom) {
    color: var(--accent);
    font-style: italic;
  }

  .search-select :global(.search-select-option-label) {
    flex: 1 1 auto;
    min-width: 0;
  }

  .search-select :global(.search-select-option-description) {
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

  .search-select :global(.search-select-empty) {
    padding: 12px 10px;
    color: var(--text-muted);
    font-size: 12px;
    text-align: center;
  }
</style>
