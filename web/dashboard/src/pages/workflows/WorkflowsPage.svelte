<script>
  import * as m from "$lib/paraglide/messages.js";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { workflowsStore as wf } from "./workflows.svelte.js";
  import WorkflowEditor from "./WorkflowEditor.svelte";
  import WorkflowList from "./WorkflowList.svelte";
  import { Plus } from "lucide";

  // Fetch on mount (the page renders only while its route is active) and
  // whenever the API key / timezone refresh tick changes.
  $effect(() => {
    void auth.refreshTick;
    wf.fetchPage();
  });
</script>

<div>
  <div class="page-header">
    <div>
      <h2>{m.workflows_title()}</h2>
      <p class="workflow-page-note">{m.workflows_note()}</p>
    </div>
    <div class="page-header-controls">
      {#if wf.available}
        <button
          type="button"
          class="btn btn-primary btn-with-icon workflow-create-btn"
          onclick={() => wf.openCreate()}
        >
          <Icon icon={Plus} class="form-action-icon" aria-hidden="true" />
          <span>{m.workflows_new()}</span>
        </button>
      {/if}
    </div>
  </div>

  {#if !wf.available && !auth.authError}
    <div class="alert alert-warning">{m.workflows_unavailable()}</div>
  {/if}
  {#if wf.error && !auth.authError}
    <div class="alert alert-warning">{wf.error}</div>
  {/if}

  {#if wf.available}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          placeholder={m.workflows_filter_placeholder()}
          label={m.workflows_filter_label()}
          bind:value={wf.filter}
        />
      </div>
      <div class="table-toolbar-actions">
        <span class="model-count">{wf.filteredWorkflows.length + " active scopes"}</span>
      </div>
    </div>
  {/if}

  <WorkflowEditor />

  <WorkflowList />

  <datalist id="workflow-guardrail-options">
    {#each wf.guardrailRefs as guardrailRef (guardrailRef)}
      <option value={guardrailRef}></option>
    {/each}
  </datalist>
</div>

<style>
/* Workflows */
.workflow-page-note {
    margin-top: 6px;
    color: var(--text-muted);
    font-size: 14px;
  }
</style>
