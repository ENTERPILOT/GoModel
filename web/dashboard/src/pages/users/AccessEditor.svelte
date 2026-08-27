<script>
  // Grant-model-access modal for one user or group. Each row is one concrete
  // model: the checkbox is the subject's explicit grant on that model's
  // access policy; the status column shows the effective availability
  // (open to everyone, inherited via ancestor path or group, blocked,
  // disabled). Saving diffs the checkboxes into policy PUT/DELETEs.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import { usersStore as store } from "./users.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  const editor = $derived(store.accessEditor);
  const visibleRows = $derived(
    editor.rows.filter(
      (row) =>
        !editor.filter ||
        row.selector.toLowerCase().includes(editor.filter.trim().toLowerCase()),
    ),
  );
  const plan = $derived(store.accessEditor.open ? store.accessPlan() : []);
  const restrictsCount = $derived(plan.filter((step) => step.restricts).length);
  const reopensCount = $derived(plan.filter((step) => step.reopens).length);

  function statusText(row) {
    switch (row.status) {
      case "open":
        return m.users_access_status_open();
      case "granted":
        return m.users_access_status_granted();
      case "inherited":
        return m.users_access_status_inherited();
      case "disabled":
        return m.users_access_status_disabled();
      default:
        return m.users_access_status_blocked();
    }
  }
</script>

<EditorDialog
  open={editor.open}
  title={editor.subject
    ? m.users_access_title({ subject: editor.subject.label })
    : ""}
  error={editor.error}
  submitting={editor.submitting}
  submitDisabled={editor.loading || plan.length === 0}
  submitLabel={m.users_access_apply({ count: plan.length })}
  submittingLabel={m.users_saving()}
  dialogClass="users-access-editor"
  onclose={() => store.closeAccessEditor()}
  onsubmit={() => store.submitAccessEditor()}
>
  {#if editor.loading}
    <LoadingState label={m.users_loading()} />
  {:else}
    <p class="form-hint users-access-hint">{m.users_access_help()}</p>
    <FilterInput
      placeholder={m.users_access_filter_placeholder()}
      label={m.users_access_filter_placeholder()}
      bind:value={editor.filter}
    />
    <div class="users-access-list" role="group" aria-label={m.users_access_models()}>
      {#each visibleRows as row (row.selector)}
        <label class="users-access-row" class:users-access-row-managed={row.managed}>
          <input
            type="checkbox"
            checked={row.checked}
            onchange={(e) => (row.checked = e.currentTarget.checked)}
            disabled={row.managed || editor.submitting}
          />
          <code class="users-access-selector">{row.selector}</code>
          <span
            class="users-access-status"
            class:users-access-status-open={row.status === "open"}
            class:users-access-status-granted={row.status === "granted" || row.status === "inherited"}
            class:users-access-status-blocked={row.status === "blocked" || row.status === "disabled"}
          >
            {statusText(row)}{row.managed ? " · " + m.users_access_managed() : ""}
          </span>
        </label>
      {:else}
        <p class="empty-state">{m.users_access_no_models()}</p>
      {/each}
    </div>
    {#if restrictsCount > 0}
      <p class="users-access-warning">{m.users_access_restrict_warning({ count: restrictsCount })}</p>
    {/if}
    {#if reopensCount > 0}
      <p class="users-access-warning">{m.users_access_reopen_warning({ count: reopensCount })}</p>
    {/if}
  {/if}
</EditorDialog>

<style>
  .users-access-hint {
    margin-bottom: 12px;
  }

  .users-access-list {
    margin-top: 12px;
    max-height: 320px;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 4px 0;
  }

  .users-access-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 6px 12px;
    cursor: pointer;
    font-size: 13px;
  }

  .users-access-row:hover {
    background: color-mix(in srgb, var(--accent) 6%, transparent);
  }

  .users-access-row-managed {
    opacity: 0.6;
    cursor: default;
  }

  .users-access-selector {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .users-access-status {
    font-size: 12px;
    white-space: nowrap;
  }

  .users-access-status-open {
    color: var(--text-secondary);
  }

  .users-access-status-granted {
    color: var(--success);
  }

  .users-access-status-blocked {
    color: var(--text-secondary);
    font-style: italic;
  }

  .users-access-warning {
    margin-top: 12px;
    font-size: 12.5px;
    color: var(--warning);
  }
</style>
