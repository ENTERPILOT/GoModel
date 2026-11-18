<script>
  // Request-log toolbar: field selector + search, method/status/stream
  // selects, the request-type checklist, and the Clear button.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { debounced } from "$lib/utils/debounce.js";
  import { requestList } from "./requestList.svelte.js";
  import { REQUEST_TYPES } from "./request-operations.js";
  import { REQUEST_LOG_SEARCH_FIELDS, requestLogSearchField } from "./request-log-logic.js";
  import { X } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const onSearchInput = debounced(() => requestList.fetchRequestLog(true));
  $effect(() => onSearchInput.cancel);

  const searchField = $derived(requestLogSearchField(requestList.requestLogSearchField));

  // A new field only changes the results when there is a value to match.
  function onSearchFieldChange() {
    if (!requestList.requestLogSearch.trim()) return;
    onSearchInput.cancel();
    requestList.fetchRequestLog(true);
  }

  let typeMenu = $state();
  const hiddenCount = $derived(requestList.requestLogHiddenTypes.length);

  // Close the type checklist on an outside click or Escape.
  $effect(() => {
    const close = (event) => {
      if (!typeMenu || !typeMenu.open) return;
      if (event.type === "keydown" ? event.key === "Escape" : !typeMenu.contains(event.target)) {
        typeMenu.open = false;
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

<div class="request-log-toolbar">
  <div class="request-log-filter-row request-log-filter-row-search">
    <select
      id="request-log-filter-search-field"
      aria-label={m.requests_search_field_label()}
      class="usage-log-select request-log-filter-select request-log-search-field"
      bind:value={requestList.requestLogSearchField}
      onchange={onSearchFieldChange}
    >
      {#each REQUEST_LOG_SEARCH_FIELDS as field (field.key)}
        <option value={field.key}>{field.label()}</option>
      {/each}
    </select>
    <FilterInput
      id="request-log-filter-search"
      placeholder={searchField.placeholder()}
      label={searchField.key ? searchField.label() : m.requests_search_label()}
      bind:value={requestList.requestLogSearch}
      oninput={onSearchInput}
      loading={requestList.loading}
    />
  </div>
  <div class="request-log-filter-row request-log-filter-row-controls">
    <select
      id="request-log-filter-method"
      aria-label={m.requests_filter_method_label()}
      class="usage-log-select request-log-filter-select"
      bind:value={requestList.requestLogMethod}
      onchange={() => requestList.fetchRequestLog(true)}
    >
      <option value="">{m.requests_filter_all_methods()}</option>
      <option value="GET">GET</option>
      <option value="POST">POST</option>
      <option value="PUT">PUT</option>
      <option value="PATCH">PATCH</option>
      <option value="DELETE">DELETE</option>
    </select>
    <select
      id="request-log-filter-status"
      aria-label={m.requests_filter_status_label()}
      class="usage-log-select request-log-filter-select"
      bind:value={requestList.requestLogStatusCode}
      onchange={() => requestList.fetchRequestLog(true)}
    >
      <option value="">{m.requests_filter_all_statuses()}</option>
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
      id="request-log-filter-stream"
      aria-label={m.requests_filter_stream_label()}
      class="usage-log-select request-log-filter-select"
      bind:value={requestList.requestLogStream}
      onchange={() => requestList.fetchRequestLog(true)}
    >
      <option value="">{m.requests_filter_all_modes()}</option>
      <option value="true">{m.requests_filter_streaming()}</option>
      <option value="false">{m.requests_filter_non_streaming()}</option>
    </select>
    <details class="request-log-filter-select request-log-type-filter" bind:this={typeMenu}>
      <summary class="usage-log-select request-log-checklist-summary" aria-label={m.requests_filter_type_label()}>
        {hiddenCount > 0 ? m.requests_filter_types_hidden({ count: hiddenCount }) : m.requests_filter_all_types()}
      </summary>
      <fieldset class="request-log-checklist-menu">
        <legend class="request-log-checklist-legend">{m.requests_filter_type_label()}</legend>
        {#each REQUEST_TYPES as type (type.key)}
          {@const visible = !requestList.requestLogHiddenTypes.includes(type.key)}
          <label class="request-log-checklist-option">
            <input
              type="checkbox"
              checked={visible}
              onchange={() => requestList.toggleRequestType(type.key)}
            />
            <span>{type.label()}</span>
          </label>
        {/each}
      </fieldset>
    </details>
    <button
      type="button"
      class="btn request-log-clear-btn"
      onclick={() => requestList.clearRequestFilters()}
    >
      <Icon icon={X} class="table-icon-svg" />
      <span>{m.common_action_clear()}</span>
    </button>
  </div>
</div>

<style>
  .request-log-toolbar {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-bottom: 14px;
  }

  .request-log-filter-row {
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    gap: 10px;
  }

  .request-log-filter-select {
    grid-column: span 2;
    min-width: 0;
  }

  .request-log-filter-row-search .request-log-search-field {
    grid-column: span 2;
  }

  .request-log-filter-row-search :global(.filter-input-wrap) {
    grid-column: 3 / -1;
    min-width: 0;
    max-width: none;
  }

  .request-log-filter-row-controls .request-log-filter-select {
    grid-column: span 2;
  }

  .request-log-filter-row-controls :global(.btn) {
    grid-column: 11 / -1;
    justify-self: end;
    min-width: 108px;
  }

  .request-log-filter-row-controls .request-log-type-filter {
    position: relative;
    grid-column: span 3;
  }

  /* Colors come from the shared .btn styles: the stylesheet
     originally declared a white variant here, but the later .btn
     base rule always overrode it, so the shipped button is the plain one.
     Scoping would resurrect the dead declarations — keep only the live ones. */
  .request-log-clear-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    font-weight: 600;
  }

  .request-log-clear-btn :global(.table-icon-svg) {
    width: 12px;
    height: 12px;
  }

  @media (max-width: 768px) {
    /* Requests page mobile */
    .request-log-toolbar {
        gap: 8px;
      }

    .request-log-filter-row {
        grid-template-columns: 1fr;
      }

    .request-log-filter-row :global(.filter-input-wrap), .request-log-filter-row :global(.filter-input), .request-log-filter-select, .request-log-filter-row :global(.btn) {
        grid-column: auto;
      }

    /* The field selector stacks above the search input. */
    .request-log-filter-row-search .request-log-search-field,
    .request-log-filter-row-search :global(.filter-input-wrap) {
        grid-column: 1 / -1;
      }
  }
</style>
