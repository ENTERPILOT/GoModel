<script>
  // "Columns" dropdown above the requests list: one checkbox per row column.
  // A view preference like "Group by session" (persisted in localStorage), so
  // the filters' Clear button leaves it alone.
  import { requestList } from "./requestList.svelte.js";
  import { REQUEST_COLUMNS } from "./request-columns.js";
  import * as m from "$lib/paraglide/messages.js";

  let menu = $state();

  // Close the checklist on an outside click or Escape.
  $effect(() => {
    const close = (event) => {
      if (!menu || !menu.open) return;
      if (event.type === "keydown" ? event.key === "Escape" : !menu.contains(event.target)) {
        menu.open = false;
      }
    };
    document.addEventListener("click", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("click", close);
      document.removeEventListener("keydown", close);
    };
  });
</script>

<details class="request-log-columns" bind:this={menu}>
  <summary
    class="usage-log-select request-log-checklist-summary request-log-columns-summary"
    aria-label={m.requests_columns_label()}
  >
    {m.requests_columns_menu()}
  </summary>
  <fieldset class="request-log-checklist-menu">
    <legend class="request-log-checklist-legend">{m.requests_columns_label()}</legend>
    {#each REQUEST_COLUMNS as column (column.key)}
      <label class="request-log-checklist-option">
        <input
          type="checkbox"
          checked={requestList.requestColumns[column.key]}
          onchange={() => requestList.toggleRequestColumn(column.key)}
        />
        <span>{column.label()}</span>
      </label>
    {/each}
  </fieldset>
</details>

<style>
  .request-log-columns {
    position: relative;
  }

  /* Same trigger as the filter selects, slimmer: tighter padding and a
     shorter chevron inset, no minimum width. */
  .request-log-columns-summary {
    min-width: 0;
    padding: 4px 28px 4px 10px;
    background-position:
      calc(100% - 15px) 50%,
      calc(100% - 10px) 50%;
  }

  /* On a phone the trigger sits mid-row; anchor the panel to its right edge so
     it does not run off the viewport. */
  @media (max-width: 768px) {
    .request-log-columns .request-log-checklist-menu {
      left: auto;
      right: 0;
    }
  }
</style>
