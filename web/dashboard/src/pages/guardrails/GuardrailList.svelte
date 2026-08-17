<script>
  // Guardrail instances panel: create button, filter toolbar, and the
  // definitions table.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { guardrailsStore as store } from "./guardrails.svelte.js";
  import { Pencil, Plus, X } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<section class="settings-panel settings-guardrails-list">
  <div class="editor-header">
    <div>
      <h3>{m.guardrails_instances()}</h3>
      <p class="form-hint">
        {m.guardrails_instances_help()}
      </p>
    </div>
    <button
      type="button"
      class="btn btn-primary btn-with-icon guardrail-create-btn"
      disabled={store.typesLoading || store.formSubmitting || !store.available}
      onclick={() => store.openCreate()}
    >
      <Icon icon={Plus} class="form-action-icon" />
      <span>{m.guardrails_create()}</span>
    </button>
  </div>

  {#if store.available}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          id="guardrail-filter"
          placeholder={m.guardrails_filter_placeholder()}
          label={m.guardrails_filter_label()}
          bind:value={store.filter}
        />
      </div>
    </div>
  {/if}

  {#if store.loading && store.filtered.length === 0}
    <LoadingState label={m.guardrails_loading()} />
  {/if}

  {#if store.filtered.length > 0}
    <div class="table-wrapper">
      <table class="data-table settings-guardrails-table">
        <thead>
          <tr>
            <th>{m.guardrails_name()}</th>
            <th>{m.guardrails_type()}</th>
            <th>{m.guardrails_user_path()}</th>
            <th>{m.guardrails_summary()}</th>
            <th class="col-actions">{m.guardrails_actions()}</th>
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
                  {guardrail.summary || guardrail.description || m.guardrails_no_summary()}
                </div>
                {#if guardrail.description}
                  <div class="settings-guardrail-description">
                    {guardrail.description}
                  </div>
                {/if}
              </td>
              <td class="col-actions">
                <div class="alias-actions-cell model-list-actions">
                  <TableActionButton
                    label={m.guardrails_edit_action({ name: guardrail.name })}
                    class="table-icon-btn"
                    onclick={() => store.openEdit(guardrail)}
                  >
                    <Icon icon={Pencil} class="table-icon-svg" />
                  </TableActionButton>
                  <TableActionButton
                    label={store.deletingName === guardrail.name
                      ? m.guardrails_deleting_action({ name: guardrail.name })
                      : m.guardrails_delete_action({ name: guardrail.name })}
                    class="table-action-btn-danger table-icon-btn"
                    onclick={() => store.deleteGuardrail(guardrail)}
                    disabled={store.deletingName === guardrail.name}
                  >
                    <Icon icon={X} class="table-icon-svg" />
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
    <p class="empty-state">{m.guardrails_empty()}</p>
  {/if}
</section>

<style>
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
</style>
