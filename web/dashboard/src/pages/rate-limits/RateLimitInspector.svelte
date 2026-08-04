<script>
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  // Effective rate limits for one model or provider: the subject's own rules
  // plus the provider and global rules that also throttle its traffic.
  // Opened from the Models page via rateLimits.openRateLimitInspectorForModel /
  // openRateLimitInspectorForProvider. Mount RateLimitEditor alongside this
  // component so "Add"/"Edit" from the inspector can open the editor.
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { router } from "$lib/stores/router.svelte.js";
  import { rateLimits } from "./rateLimits.svelte.js";
  import { Activity, Pencil, Plus, Timer } from "lucide";

  function onclose() {
    // Never close the inspector underneath the auth dialog.
    if (auth.dialogOpen) return;
    rateLimits.closeRateLimitInspector();
  }
</script>

<Modal open={rateLimits.rateLimitInspectorOpen} variant="editor" {onclose}>
  <div
    class="model-editor budget-editor"
    role="dialog"
    aria-modal="true"
    aria-label="Effective rate limits"
  >
    <div class="editor-header">
      <div>
        <h3>Rate Limits</h3>
        <p class="form-hint">
          <code class="mono">{rateLimits.rateLimitInspector.title}</code>
        </p>
      </div>
      <DialogCloseButton
        label="Close rate limits inspector"
        onclick={() => rateLimits.closeRateLimitInspector()}
      />
    </div>

    {#if rateLimits.rateLimitsLoading}
      <LoadingState label="Loading rate limits..." />
    {:else if !rateLimits.rateLimitsAvailable}
      <div class="alert alert-warning">Rate limit management is unavailable.</div>
    {:else}
      {#each rateLimits.rateLimitInspectorSections() as section (section.key)}
        <section class="form-section">
          <div class="inline-help-title-row">
            <h4 class="form-field-label">{section.title}</h4>
            <TableActionButton
              label={"Add " + section.title.toLowerCase()}
              class="budget-action-btn"
              onclick={() =>
                rateLimits.openRateLimitFormFromInspector(
                  section.scope,
                  section.subject,
                )}
            >
              <Icon icon={Plus} class="table-icon-svg" />
              <span class="budget-action-label">Add</span>
            </TableActionButton>
          </div>
          {#if section.hint}
            <p class="form-hint">{section.hint}</p>
          {/if}
          {#if section.items.length === 0}
            <p class="empty-state">No rules.</p>
          {:else}
            <div class="budget-list">
              {#each section.items as item (rateLimits.rateLimitKey(item))}
                <section
                  class="budget-row {rateLimits.rateLimitPressureClass(item)}"
                  style={rateLimits.rateLimitPressureStyle(item)}
                  title={rateLimits.rateLimitPressurePercent(item) +
                    "% of the most constrained cap used"}
                >
                  <div class="budget-row-main">
                    <div class="budget-row-head">
                      <code class="budget-scope-value budget-user-path">
                        {rateLimits.rateLimitSubject(item)}
                      </code>
                      <div class="budget-row-period">
                        <span class="budget-period-label">
                          <Icon
                            icon={rateLimits.rateLimitIsConcurrent(item)
                              ? Activity
                              : Timer}
                            class="budget-period-icon"
                          />
                          <span>{rateLimits.rateLimitPeriodLabel(item)}</span>
                        </span>
                      </div>
                      <div class="budget-row-controls">
                        <div class="budget-row-meta">
                          <span class="budget-source">
                            {rateLimits.rateLimitInspectorSummary(item)}
                          </span>
                          <span
                            class="budget-source"
                            title={rateLimits.rateLimitIsReadOnly(item)
                              ? "Declared in configuration; read-only in the dashboard"
                              : "Managed via dashboard or admin API"}
                          >
                            {rateLimits.rateLimitSourceLabel(item)}
                          </span>
                        </div>
                        <div class="budget-row-actions">
                          {#if !rateLimits.rateLimitIsReadOnly(item)}
                            <TableActionButton
                              label="Edit rate limit"
                              class="budget-action-btn"
                              onclick={() =>
                                rateLimits.openRateLimitFormFromInspector(
                                  null,
                                  null,
                                  item,
                                )}
                            >
                              <Icon icon={Pencil} class="budget-action-icon" />
                              <span class="budget-action-label">Edit</span>
                            </TableActionButton>
                          {/if}
                        </div>
                      </div>
                    </div>
                  </div>
                </section>
              {/each}
            </div>
          {/if}
        </section>
      {/each}
    {/if}

    <div class="form-actions">
      <button
        type="button"
        class="btn"
        onclick={() => rateLimits.closeRateLimitInspector()}
      >
        Close
      </button>
      {#if rateLimits.rateLimitsEnabled()}
        <button
          type="button"
          class="btn"
          onclick={() => {
            rateLimits.closeRateLimitInspector();
            router.navigate("rate-limits");
          }}
        >
          Open Rate Limits page
        </button>
      {/if}
    </div>
  </div>
</Modal>
