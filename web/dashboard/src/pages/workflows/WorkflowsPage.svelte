<script>
  // Workflows page.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { workflowsStore as wf } from "./workflows.svelte.js";
  import WorkflowEditor from "./WorkflowEditor.svelte";
  import WorkflowList from "./WorkflowList.svelte";

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
      <h2>Workflows</h2>
      <p class="workflow-page-note">Active workflows are matched path-first: deepest user path wins first, then provider name and model specificity, then broader matches, then global.</p>
    </div>
    <div class="page-header-controls">
      {#if wf.available}
        <button
          type="button"
          class="pagination-btn pagination-btn-primary pagination-btn-with-icon workflow-create-btn"
          onclick={() => wf.openCreate()}
        >
          <Icon name="plus" class="form-action-icon" aria-hidden="true" />
          <span>New&nbsp;Workflow</span>
        </button>
      {/if}
    </div>
  </div>

  {#if !wf.available && !auth.authError}
    <div class="alert alert-warning">Workflows feature is unavailable.</div>
  {/if}
  {#if wf.error && !auth.authError}
    <div class="alert alert-warning">{wf.error}</div>
  {/if}
  {#if wf.notice}
    <div class="alert alert-success">{wf.notice}</div>
  {/if}

  {#if wf.available}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <div class="filter-input-wrap">
          <Icon name="search" class="filter-input-icon" aria-hidden="true" />
          <input
            type="text"
            placeholder="Filter by scope, name, hash, or guardrail..."
            bind:value={wf.filter}
            class="filter-input"
            aria-label="Filter workflows by scope, name, hash, or guardrail"
          />
        </div>
      </div>
      <div class="table-toolbar-actions">
        <span class="model-count">{wf.filteredWorkflows.length + " active scopes"}</span>
      </div>
    </div>
  {/if}

  <WorkflowEditor />

  <div class="workflows-layout">
    <WorkflowList />
  </div>

  <datalist id="workflow-guardrail-options">
    {#each wf.guardrailRefs as guardrailRef (guardrailRef)}
      <option value={guardrailRef}></option>
    {/each}
  </datalist>
</div>
