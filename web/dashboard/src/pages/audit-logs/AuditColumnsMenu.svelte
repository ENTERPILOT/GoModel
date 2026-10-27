<script>
  // "Columns" dropdown above the audit list: one checkbox per row column.
  // A view preference like "Group by session" (persisted in localStorage), so
  // the filters' Clear button leaves it alone.
  import { auditList } from "./auditList.svelte.js";
  import { AUDIT_COLUMNS } from "./audit-columns.js";
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

<details class="audit-columns" bind:this={menu}>
  <summary
    class="usage-log-select audit-checklist-summary audit-columns-summary"
    aria-label={m.audit_columns_label()}
  >
    {m.audit_columns_menu()}
  </summary>
  <fieldset class="audit-checklist-menu">
    <legend class="audit-checklist-legend">{m.audit_columns_label()}</legend>
    {#each AUDIT_COLUMNS as column (column.key)}
      <label class="audit-checklist-option">
        <input
          type="checkbox"
          checked={auditList.auditColumns[column.key]}
          onchange={() => auditList.toggleAuditColumn(column.key)}
        />
        <span>{column.label()}</span>
      </label>
    {/each}
  </fieldset>
</details>

<style>
  .audit-columns {
    position: relative;
  }

  /* Same trigger as the filter selects, slimmer: tighter padding and a
     shorter chevron inset, no minimum width. */
  .audit-columns-summary {
    min-width: 0;
    padding: 4px 28px 4px 10px;
    background-position:
      calc(100% - 15px) 50%,
      calc(100% - 10px) 50%;
  }

  /* On a phone the trigger sits mid-row; anchor the panel to its right edge so
     it does not run off the viewport. */
  @media (max-width: 768px) {
    .audit-columns .audit-checklist-menu {
      left: auto;
      right: 0;
    }
  }
</style>
