<script>
  // Guardrail instances panel: create button, filter toolbar, and the
  // definitions table.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { guardrailsStore as store } from "./guardrails.svelte.js";
</script>

<div class="settings-guardrails-layout">
  <section class="settings-panel settings-guardrails-list">
    <div class="editor-header">
      <div>
        <h3>Instances</h3>
        <p class="form-hint">
          Each instance has a reusable name, a type, an optional user path for
          future UI visibility scoping, and a JSON-backed config payload for
          that type.
        </p>
      </div>
      <button
        type="button"
        class="pagination-btn pagination-btn-primary pagination-btn-with-icon guardrail-create-btn"
        disabled={store.typesLoading || store.formSubmitting || !store.available}
        onclick={() => store.openCreate()}
      >
        <Icon name="plus" class="form-action-icon" />
        <span>Create&nbsp;Guardrail</span>
      </button>
    </div>

    {#if store.available}
      <div class="table-toolbar">
        <div class="table-toolbar-main">
          <div class="filter-input-wrap">
            <Icon name="search" class="filter-input-icon" />
            <input
              type="text"
              id="guardrail-filter"
              class="filter-input"
              placeholder="Filter by name, type, user path, summary..."
              aria-label="Guardrail filter"
              bind:value={store.filter}
            />
          </div>
        </div>
      </div>
    {/if}

    {#if store.loading && store.filtered.length === 0}
      <p class="empty-state">
        <Spinner size={16} label="Loading guardrails" /> Loading guardrails...
      </p>
    {/if}

    {#if store.filtered.length > 0}
      <div class="table-wrapper">
        <table class="data-table settings-guardrails-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Type</th>
              <th>User Path</th>
              <th>Summary</th>
              <th class="col-actions">Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each store.filtered as guardrail (guardrail.name)}
              <tr>
                <td class="mono font-size-md">{guardrail.name}</td>
                <td>
                  <span class="settings-guardrail-type-pill"
                    >{store.typeLabel(guardrail.type)}</span
                  >
                </td>
                <td class="mono font-size-md">{guardrail.user_path || "—"}</td>
                <td>
                  <div class="settings-guardrail-summary">
                    {guardrail.summary || guardrail.description || "No summary yet."}
                  </div>
                  {#if guardrail.description}
                    <div class="settings-guardrail-description">
                      {guardrail.description}
                    </div>
                  {/if}
                </td>
                <td class="col-actions">
                  <div class="alias-actions-cell">
                    <TableActionButton
                      label={"Edit guardrail " + guardrail.name}
                      class="table-icon-btn"
                      onclick={() => store.openEdit(guardrail)}
                    >
                      <svg
                        class="table-icon-svg"
                        viewBox="0 0 24 24"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="2"
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        aria-hidden="true"
                      >
                        <path d="M12 20h9"></path>
                        <path d="M16.5 3.5a2.12 2.12 0 1 1 3 3L7 19l-4 1 1-4 12.5-12.5z"></path>
                      </svg>
                    </TableActionButton>
                    <TableActionButton
                      label={store.deletingName === guardrail.name ? "Deleting guardrail " + guardrail.name : "Delete guardrail " + guardrail.name}
                      class="table-action-btn-danger table-icon-btn"
                      onclick={() => store.deleteGuardrail(guardrail)}
                      disabled={store.deletingName === guardrail.name}
                    >
                      <svg
                        class="table-icon-svg"
                        viewBox="0 0 24 24"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="2"
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        aria-hidden="true"
                      >
                        <path d="M18 6L6 18"></path>
                        <path d="M6 6l12 12"></path>
                      </svg>
                    </TableActionButton>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    {#if store.filtered.length === 0 && !store.loading && store.available && !store.error && !auth.authError}
      <p class="empty-state">No guardrails defined yet.</p>
    {/if}
  </section>
</div>

<style>
/* Styles owned by this component (moved from dashboard.css). */
.settings-guardrails-layout {
    display: grid;
    grid-template-columns: 1fr;
    gap: 20px;
    align-items: start;
  }

.settings-guardrails-list {
    min-width: 0;
  }

.settings-guardrail-type-pill {
    display: inline-flex;
    align-items: center;
    padding: 6px 10px;
    border: 1px solid color-mix(in srgb, var(--accent) 18%, var(--border));
    border-radius: 999px;
    background: color-mix(in srgb, var(--accent) 12%, transparent);
    color: var(--text);
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
  }

.settings-guardrail-summary {
    color: var(--text);
    font-size: 14px;
    line-height: 1.45;
  }

.settings-guardrail-description {
    margin-top: 6px;
    color: var(--text-muted);
    font-size: 12px;
  }

@media (max-width: 768px) {
  .settings-guardrails-layout {
          grid-template-columns: 1fr;
        }
}
</style>
