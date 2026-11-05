<script>
  // User path filter: known paths as a tree grouped by common prefix, with
  // expand/collapse, search, multi-select (a node selects its subtree) and a
  // "Use <typed path>" row for paths not in the list. Shares SearchSelect's
  // shell, so both pickers look and behave alike.
  //
  // Props: paths (known user paths), values (bindable selection), onchange,
  // placeholder, ariaLabel, scope (a scoped key's root: only its subtree is
  // offered).
  import Icon from "$lib/components/atoms/Icon.svelte";
  import SearchPopover from "$lib/components/molecules/SearchPopover.svelte";
  import { toggleSearchValue } from "$lib/components/molecules/searchSelectLogic.js";
  import { Check, ChevronDown, ChevronRight } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
  import { buildUserPathTree, customUserPath, userPathCovered, visibleUserPathRows } from "./user-path-tree.js";

  let { paths = [], values = $bindable([]), onchange, placeholder = "", ariaLabel = "", scope = "/" } = $props();

  const uid = $props.id();
  const listID = uid + "-user-path-tree";
  let open = $state(false);
  let query = $state("");
  let activeIndex = $state(-1);
  let expanded = $state(new Set());
  let shell = $state(null);

  const tree = $derived(buildUserPathTree([...paths, ...values], scope));
  const rows = $derived(visibleUserPathRows(tree, expanded, query));
  const custom = $derived(customUserPath(query, tree, values, scope));
  const rowCount = $derived(rows.length + (custom ? 1 : 0));

  // Opening reveals the selection: every ancestor of a selected path expands.
  function onopen() {
    const next = new Set(expanded);
    for (const value of values) {
      const segments = value.split("/").filter(Boolean);
      for (let i = 1; i < segments.length; i++) next.add("/" + segments.slice(0, i).join("/"));
    }
    expanded = next;
  }

  function toggleExpanded(path, force) {
    const next = new Set(expanded);
    if (force ?? !next.has(path)) next.add(path);
    else next.delete(path);
    expanded = next;
  }

  function choose(path) {
    if (!path) return;
    values = toggleSearchValue(values, path);
    onchange?.(values);
    query = "";
    shell?.focusSearch();
  }

  function rowAt(index) {
    if (custom) return index === 0 ? { path: custom } : rows[index - 1];
    return rows[index];
  }

  function onkeydown(event) {
    const row = rowAt(activeIndex);
    switch (event.key) {
      case "ArrowRight":
      case "ArrowLeft":
        if (row?.hasChildren && !query) {
          event.preventDefault();
          toggleExpanded(row.path, event.key === "ArrowRight");
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
  triggerText={values.length ? values.join(", ") : placeholder}
  triggerTitle={values.join(", ") || undefined}
  hasSelection={values.length > 0}
  multiple
  mono
  {ariaLabel}
  searchPlaceholder={m.usage_search_user_paths()}
  class="user-path-tree-select"
  {onopen}
  {onkeydown}
>
  {#if custom}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <li
      id={listID + "-0"}
      class="search-select-option search-select-option-custom mono"
      class:search-select-option-active={activeIndex === 0}
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
      class="search-select-option tree-row mono"
      class:search-select-option-active={activeIndex === position}
      class:search-select-option-current={selected}
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
      <span class="search-select-option-label tree-row-name">{row.name}</span>
      {#if row.hasChildren}<span class="search-select-option-description">{m.usage_user_path_subtree()}</span>{/if}
      {#if selected || covered}
        <Icon icon={Check} class="search-select-check" />
      {/if}
    </li>
  {/each}
  {#if rowCount === 0}
    <li class="search-select-empty" role="presentation">{m.search_select_no_matches()}</li>
  {/if}
</SearchPopover>

<style>
  /* Tree rows add an expand toggle and indentation to the shared rows. */
  :global(.user-path-tree-select) .tree-row.search-select-option {
    gap: 6px;
  }

  :global(.user-path-tree-select) .tree-row-covered.search-select-option {
    color: var(--text-muted);
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
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
