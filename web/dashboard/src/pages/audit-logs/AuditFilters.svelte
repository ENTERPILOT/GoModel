<script>
  // Audit-log toolbar: consolidated search + method/status/stream selects and
  // the Clear button.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { debounced } from "$lib/utils/debounce.ts";
  import { auditList } from "./auditList.svelte.js";

  const onSearchInput = debounced(() => auditList.fetchAuditLog(true));
  $effect(() => onSearchInput.cancel);
</script>

<div class="audit-log-toolbar">
  <div class="audit-filter-row audit-filter-row-search">
    <FilterInput
      id="audit-filter-search"
      placeholder="Search by request ID, model, provider, path, user path, or error..."
      label="Search by request ID, model, provider, path, user path, or error"
      bind:value={auditList.auditSearch}
      oninput={onSearchInput}
    />
  </div>
  <div class="audit-filter-row audit-filter-row-controls">
    <select
      id="audit-filter-method"
      aria-label="HTTP method filter"
      class="usage-log-select audit-filter-select"
      bind:value={auditList.auditMethod}
      onchange={() => auditList.fetchAuditLog(true)}
    >
      <option value="">All Methods</option>
      <option value="GET">GET</option>
      <option value="POST">POST</option>
      <option value="PUT">PUT</option>
      <option value="PATCH">PATCH</option>
      <option value="DELETE">DELETE</option>
    </select>
    <select
      id="audit-filter-status"
      aria-label="Status code filter"
      class="usage-log-select audit-filter-select"
      bind:value={auditList.auditStatusCode}
      onchange={() => auditList.fetchAuditLog(true)}
    >
      <option value="">All Statuses</option>
      <option value="200">200</option>
      <option value="201">201</option>
      <option value="400">400</option>
      <option value="401">401</option>
      <option value="403">403</option>
      <option value="404">404</option>
      <option value="429">429</option>
      <option value="500">500</option>
      <option value="502">502</option>
      <option value="503">503</option>
      <option value="504">504</option>
    </select>
    <select
      id="audit-filter-stream"
      aria-label="Streaming mode filter"
      class="usage-log-select audit-filter-select"
      bind:value={auditList.auditStream}
      onchange={() => auditList.fetchAuditLog(true)}
    >
      <option value="">All Modes</option>
      <option value="true">Streaming</option>
      <option value="false">Non-streaming</option>
    </select>
    <label class="audit-group-checkbox">
      <input
        type="checkbox"
        checked={auditList.auditGroupSessions}
        onchange={() => auditList.toggleAuditGroupSessions()}
      />
      <span>Group by session</span>
    </label>
    <button
      type="button"
      class="btn audit-clear-btn"
      onclick={() => auditList.clearAuditFilters()}
    >
      <Icon name="x" class="table-icon-svg" />
      <span>Clear</span>
    </button>
  </div>
</div>

<style>
  .audit-log-toolbar {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-bottom: 14px;
  }

  .audit-filter-row {
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    gap: 10px;
  }

  .audit-filter-select {
    grid-column: span 2;
    min-width: 0;
  }

  .audit-filter-row-search :global(.filter-input-wrap) {
    grid-column: 1 / -1;
    max-width: none;
  }

  .audit-filter-row-controls .audit-filter-select {
    grid-column: span 2;
  }

  /* View preference, not a filter: Clear leaves it alone. */
  .audit-group-checkbox {
    grid-column: 7 / 11;
    justify-self: end;
    display: inline-flex;
    align-items: center;
    gap: 7px;
    color: var(--text-muted);
    font-size: 13px;
    cursor: pointer;
    user-select: none;
  }

  .audit-group-checkbox input {
    accent-color: var(--accent);
    cursor: pointer;
  }

  .audit-filter-row-controls :global(.btn) {
    grid-column: 11 / -1;
    justify-self: end;
    min-width: 108px;
  }

  /* Colors come from the shared .btn styles: the stylesheet
     originally declared a white variant here, but the later .btn
     base rule always overrode it, so the shipped button is the plain one.
     Scoping would resurrect the dead declarations — keep only the live ones. */
  .audit-clear-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    font-weight: 600;
  }

  .audit-clear-btn :global(.table-icon-svg) {
    width: 12px;
    height: 12px;
  }

  @media (max-width: 768px) {
    /* Audit page mobile */
    .audit-log-toolbar {
        gap: 8px;
      }

    .audit-filter-row {
        grid-template-columns: 1fr;
      }

    .audit-filter-row :global(.filter-input-wrap), .audit-filter-row :global(.filter-input), .audit-filter-select, .audit-group-checkbox, .audit-filter-row :global(.btn) {
        grid-column: auto;
      }

    .audit-group-checkbox {
        justify-self: start;
      }
  }
</style>
