<script>
  // User path filter: known paths as a tree grouped by common prefix, with
  // expand/collapse, search, multi-select (a node selects its subtree) and a
  // "Use <typed path>" row for paths not in the list. Styled to match
  // SearchSelect beside it in the filter bar.
  //
  // Props: paths (known user paths), values (bindable selection), onchange,
  // placeholder, ariaLabel.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { dismissOnOutside, followViewport } from "$lib/utils/attachments.js";
  import { popoverPlacement, moveActiveIndex } from "$lib/components/molecules/searchSelectLogic.js";
  import { Check, ChevronDown, ChevronRight, Search } from "lucide";
  import { tick } from "svelte";
  import * as m from "$lib/paraglide/messages.js";
  import {
    buildUserPathTree,
    customUserPath,
    toggleUserPath,
    userPathCovered,
    visibleUserPathRows,
  } from "./user-path-tree.js";

  let { paths = [], values = $bindable([]), onchange, placeholder = "", ariaLabel = "" } = $props();

  const uid = $props.id();
  const listID = uid + "-user-path-tree";
  let open = $state(false);
  let query = $state("");
  let activeIndex = $state(-1);
  let expanded = $state(new Set());
  let rootEl = $state(null);
  let searchEl = $state(null);
  let listEl = $state(null);
  let popoverStyle = $state("");

  const tree = $derived(buildUserPathTree([...paths, ...values]));
  const rows = $derived(visibleUserPathRows(tree, expanded, query));
  const custom = $derived(customUserPath(query, tree, values));
  const rowCount = $derived(rows.length + (custom ? 1 : 0));
  const triggerText = $derived(values.length ? values.join(", ") : placeholder);

  function placePopover() {
    const rect = rootEl?.getBoundingClientRect();
    if (!rect) return;
    const vv = window.visualViewport;
    const viewport = vv
      ? { top: vv.offsetTop, height: vv.height, width: window.innerWidth }
      : { top: 0, height: window.innerHeight, width: window.innerWidth };
    const style = popoverPlacement(rect, viewport);
    if (style === null) close();
    else popoverStyle = style;
  }

  async function openList() {
    placePopover();
    open = true;
    query = "";
    // Reveal the selection: expand every ancestor of a selected path.
    const next = new Set(expanded);
    for (const value of values) {
      const segments = value.split("/").filter(Boolean);
      for (let i = 1; i < segments.length; i++) next.add("/" + segments.slice(0, i).join("/"));
    }
    expanded = next;
    await tick();
    searchEl?.focus();
  }

  function close() {
    open = false;
    query = "";
    activeIndex = -1;
  }

  function toggleExpanded(path, force) {
    const next = new Set(expanded);
    const want = force ?? !next.has(path);
    if (want) next.add(path);
    else next.delete(path);
    expanded = next;
  }

  function choose(path) {
    if (!path) return;
    values = toggleUserPath(values, path);
    onchange?.(values);
    query = "";
    searchEl?.focus();
  }

  function rowAt(index) {
    if (custom) return index === 0 ? { path: custom, custom: true } : rows[index - 1];
    return rows[index];
  }

  async function onKeydown(event) {
    const row = rowAt(activeIndex);
    switch (event.key) {
      case "ArrowDown":
      case "ArrowUp":
        event.preventDefault();
        activeIndex = moveActiveIndex(activeIndex, event.key === "ArrowDown" ? 1 : -1, rowCount);
        await tick();
        listEl?.querySelector(".tree-row-active")?.scrollIntoView?.({ block: "nearest" });
        break;
      case "ArrowRight":
        if (row?.hasChildren && !query) {
          event.preventDefault();
          toggleExpanded(row.path, true);
        }
        break;
      case "ArrowLeft":
        if (row?.hasChildren && !query) {
          event.preventDefault();
          toggleExpanded(row.path, false);
        }
        break;
      case "Enter":
        event.preventDefault();
        choose((row || rowAt(0))?.path);
        break;
      case "Backspace":
        if (!query && values.length) {
          event.preventDefault();
          values = values.slice(0, -1);
          onchange?.(values);
        }
        break;
      case "Escape":
        event.preventDefault();
        event.stopPropagation();
        close();
        break;
      case "Tab":
        close();
        break;
    }
  }

  $effect(() => {
    void query;
    activeIndex = rowCount > 0 ? 0 : -1;
  });
</script>

<div
  class="tree-select"
  class:tree-select-open={open}
  bind:this={rootEl}
  {@attach open ? dismissOnOutside(close) : undefined}
  {@attach open ? followViewport(placePopover) : undefined}
>
  <button
    type="button"
    class="tree-select-trigger"
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-controls={open ? listID : undefined}
    aria-label={ariaLabel || undefined}
    onclick={() => (open ? close() : openList())}
  >
    <span class="tree-select-value" class:tree-select-placeholder={!values.length} title={values.join(", ") || undefined}>
      {triggerText}
    </span>
    <Icon icon={ChevronDown} class="tree-select-chevron" />
  </button>

  {#if open}
    <div class="tree-select-popover" style={popoverStyle}>
      <div class="tree-select-search">
        <Icon icon={Search} class="tree-select-search-icon" />
        <input
          type="text"
          class="tree-select-search-input"
          role="combobox"
          aria-autocomplete="list"
          aria-expanded="true"
          aria-controls={listID}
          aria-activedescendant={activeIndex >= 0 ? listID + "-" + activeIndex : undefined}
          aria-label={m.usage_search_user_paths()}
          placeholder={m.usage_search_user_paths()}
          autocomplete="off"
          spellcheck="false"
          bind:this={searchEl}
          bind:value={query}
          onkeydown={onKeydown}
        />
      </div>
      <ul class="tree-select-list" id={listID} role="listbox" aria-multiselectable="true" bind:this={listEl}>
        {#if custom}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <li
            id={listID + "-0"}
            class="tree-row tree-row-custom"
            class:tree-row-active={activeIndex === 0}
            role="option"
            aria-selected="false"
            onmouseenter={() => (activeIndex = 0)}
            onclick={() => choose(custom)}
          >
            {m.search_select_use_custom({ value: custom })}
          </li>
        {/if}
        {#each rows as row, index (row.path)}
          {@const position = index + (custom ? 1 : 0)}
          {@const selected = values.includes(row.path)}
          {@const covered = !selected && userPathCovered(row.path, values)}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <li
            id={listID + "-" + position}
            class="tree-row"
            class:tree-row-active={activeIndex === position}
            class:tree-row-selected={selected}
            class:tree-row-covered={covered}
            role="option"
            aria-selected={selected}
            style:padding-left="{8 + row.depth * 16}px"
            title={row.path}
            onmouseenter={() => (activeIndex = position)}
            onclick={() => choose(row.path)}
          >
            {#if row.hasChildren}
              <button
                type="button"
                class="tree-toggle"
                tabindex="-1"
                aria-label={row.expanded ? m.usage_collapse_path({ path: row.path }) : m.usage_expand_path({ path: row.path })}
                onclick={(event) => {
                  event.stopPropagation();
                  toggleExpanded(row.path);
                }}
              >
                <Icon icon={row.expanded ? ChevronDown : ChevronRight} />
              </button>
            {:else}
              <span class="tree-toggle-spacer"></span>
            {/if}
            <span class="tree-row-name">{row.name}</span>
            {#if row.hasChildren}<span class="tree-row-hint">{m.usage_user_path_subtree()}</span>{/if}
            {#if selected || covered}
              <Icon icon={Check} class="tree-row-check" />
            {/if}
          </li>
        {/each}
        {#if rowCount === 0}
          <li class="tree-select-empty" role="presentation">{m.search_select_no_matches()}</li>
        {/if}
      </ul>
    </div>
  {/if}
</div>

<style>
  .tree-select {
    position: relative;
    display: inline-flex;
    min-width: 0;
  }

  .tree-select-trigger {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    min-height: 32px;
    padding: 5px 8px 5px 10px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    font-size: 12px;
    font-family: 'SF Mono', Menlo, Consolas, monospace;
    text-align: left;
    cursor: pointer;
  }

  .tree-select-trigger:hover,
  .tree-select-open .tree-select-trigger {
    border-color: var(--accent);
  }

  .tree-select-value {
    flex: 1 1 auto;
    min-width: 0;
    overflow-wrap: anywhere;
    line-height: 1.5;
  }

  .tree-select-placeholder {
    color: var(--text-muted);
    font-family: inherit;
  }

  .tree-select :global(.tree-select-chevron) {
    flex-shrink: 0;
    width: 14px;
    height: 14px;
    color: var(--text-muted);
  }

  .tree-select-popover {
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

  .tree-select-search {
    position: relative;
    display: flex;
    padding: 8px;
    border-bottom: 1px solid var(--border);
  }

  .tree-select :global(.tree-select-search-icon) {
    position: absolute;
    top: 50%;
    left: 18px;
    width: 14px;
    height: 14px;
    color: var(--text-muted);
    transform: translateY(-50%);
    pointer-events: none;
  }

  .tree-select-search-input {
    width: 100%;
    padding: 7px 10px 7px 30px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text);
    font-size: 13px;
  }

  .tree-select-search-input:focus {
    border-color: var(--accent);
    outline: none;
  }

  .tree-select-list {
    max-height: 300px;
    min-height: 0;
    margin: 0;
    padding: 6px;
    list-style: none;
    overflow-y: auto;
  }

  .tree-row {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 10px 6px 8px;
    border-radius: 6px;
    color: var(--text);
    font-size: 12px;
    font-family: 'SF Mono', Menlo, Consolas, monospace;
    cursor: pointer;
  }

  .tree-row-active {
    background: var(--bg-surface-hover);
  }

  .tree-row-selected {
    color: var(--accent);
    font-weight: 600;
  }

  .tree-row-covered {
    color: var(--text-muted);
  }

  .tree-row-custom {
    color: var(--accent);
    font-style: italic;
  }

  .tree-toggle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    padding: 0;
    background: none;
    border: none;
    border-radius: 4px;
    color: var(--text-muted);
    cursor: pointer;
  }

  .tree-toggle:hover {
    color: var(--text);
  }

  .tree-toggle :global(svg) {
    width: 14px;
    height: 14px;
  }

  .tree-toggle-spacer {
    flex-shrink: 0;
    width: 18px;
  }

  .tree-row-name {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .tree-row-hint {
    color: var(--text-muted);
    font-family: inherit;
    font-size: 11px;
    font-weight: 400;
  }

  .tree-select :global(.tree-row-check) {
    flex-shrink: 0;
    width: 14px;
    height: 14px;
  }

  .tree-select-empty {
    padding: 12px 10px;
    color: var(--text-muted);
    font-size: 12px;
    text-align: center;
  }
</style>
