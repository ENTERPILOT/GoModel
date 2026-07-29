<script>
  // Unified virtual-model editor modal (redirect/alias, load balancer, or
  // access policy).
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { virtualModels } from "./virtualModels.svelte.js";
  import { qualifiedModelName } from "./virtualModelsLogic.js";
  import VmTargetRow from "./VmTargetRow.svelte";

  const vm = virtualModels;

  // The strategy dropdown is server-driven (VIRTUAL_MODEL_STRATEGIES); make
  // sure the runtime config is loaded by the time the editor shows it.
  $effect(() => {
    if (vm.vmFormOpen) {
      runtimeConfig.ensureLoaded();
    }
  });
</script>

<Modal open={vm.vmFormOpen} onclose={() => vm.closeVirtualModelForm()}>
  <div class="model-editor" role="dialog" aria-modal="true" aria-label="Virtual model editor">
    <form
      class="form"
      onsubmit={(event) => {
        event.preventDefault();
        vm.submitVirtualModelForm();
      }}
    >
      <div class="editor-header">
        <InlineHelpSection
          copyId="virtual-model-help-copy"
          label="virtual model help"
          bind:open={vm.vmFormHelpOpen}
        >
          {#snippet title()}
            <h3>{vm.vmFormDisplayName || vm.vmForm.source || "Virtual model"}</h3>
          {/snippet}
          {#snippet help()}
            Add one <strong>target</strong> to make this a redirect/alias, or two or more to load
            balance across them, then pick a strategy: <code>round_robin</code> rotates across targets
            (weight biases the share) and <code>cost</code> always routes to the cheapest available
            target.{#if vm.vmStrategyOptions().some((option) => option.value === "adaptive")}
              <code>adaptive</code> lets the registered routing extension pick the target per
              request.{/if} Leave <strong>Targets</strong> empty to make it only an access policy on the
            <code>Source</code> selector. The selector uses <code>/</code> for all providers and
            models, <code>{"{provider_name}"}/</code> for one provider, or
            <code>{"{provider_name}"}/{"{model}"}</code> for one model. <code>user_paths</code> is
            matched against the effective request <code>user_path</code>: the managed API key
            <code>user_path</code> when present, otherwise the configured user path request header.
          {/snippet}
        </InlineHelpSection>
        <DialogCloseButton
          label="Close virtual model editor"
          onclick={() => vm.closeVirtualModelForm()}
        />
      </div>

      {#if vm.vmFormManaged}
        <p class="form-hint" role="status">
          This virtual model is defined in configuration (<code>config.yaml</code> /
          <code>VIRTUAL_MODELS</code>) and is read-only here. Edit your configuration to change it.
        </p>
      {/if}


      <div class="form-field">
        <label class="form-field-label" for="virtual-model-source">Source</label>
        <input
          id="virtual-model-source"
          type="text"
          class="mono"
          placeholder="smart"
          bind:value={vm.vmForm.source}
          disabled={vm.vmFormSourceLocked || vm.vmFormManaged}
          data-modal-autofocus
        />
      </div>

      <datalist id="virtual-model-target-options">
        {#each modelsStore.models as m (qualifiedModelName(m))}
          <option value={qualifiedModelName(m)}>{qualifiedModelName(m)}</option>
        {/each}
      </datalist>

      <!-- Every target renders the same; two or more turn the redirect into a load balancer. -->
      <div class="form-field">
        <span class="form-field-label">Targets</span>
        <VmTargetRow
          id="virtual-model-target"
          bind:model={vm.vmForm.target_model}
          bind:weight={vm.vmForm.target_weight}
          showRemove={vm.vmFormHasPrimaryTarget()}
          onremove={() => vm.removePrimaryTarget()}
        />
        {#each vm.vmForm.targets as target, index (index)}
          <VmTargetRow
            placeholder="groq/llama"
            bind:model={target.model}
            bind:weight={target.weight}
            onremove={() => vm.removeVmTarget(index)}
          />
        {/each}
        <div>
          <button
            type="button"
            class="btn btn-with-icon"
            disabled={vm.vmFormManaged}
            onclick={() => vm.addVmTarget()}
          >
            <Icon name="plus" class="form-action-icon" />
            <span>Add target (load balancing)</span>
          </button>
        </div>
      </div>

      {#if vm.vmFormShowStrategy()}
        <div class="form-field">
          <label class="form-field-label" for="virtual-model-strategy">Load-balancing strategy</label>
          <select
            id="virtual-model-strategy"
            class="form-select"
            bind:value={vm.vmForm.strategy}
            disabled={vm.vmFormManaged}
          >
            {#each vm.vmStrategyOptions() as option (option.value)}
              <option value={option.value}>{option.label}</option>
            {/each}
          </select>
        </div>
        <div class="form-field">
          <label class="vm-session-affinity-checkbox">
            <input
              type="checkbox"
              bind:checked={vm.vmForm.session_affinity}
              disabled={vm.vmFormManaged}
            />
            <span>Session keeping (route a detected client session to the target that served it)</span>
          </label>
        </div>
      {/if}

      <div class="form-field">
        <InlineHelpSection
          copyId="virtual-model-user-paths-help"
          label="user paths help"
          bind:open={vm.vmFormUserPathsHelpOpen}
        >
          {#snippet title()}
            <label class="form-field-label" for="virtual-model-user-paths">User Paths</label>
          {/snippet}
          {#snippet help()}
            Use <code>/</code> to allow every user path. Use a team path to restrict to that
            subtree, or an unused path to make the selector unavailable.
          {/snippet}
        </InlineHelpSection>
        <textarea
          id="virtual-model-user-paths"
          rows="4"
          class="mono"
          placeholder={"/\n/team/alpha\n/non-existing"}
          bind:value={vm.vmForm.user_paths}
          disabled={vm.vmFormManaged}
        ></textarea>
      </div>

      <div class="form-field">
        <label class="form-field-label" for="virtual-model-description">Description</label>
        <textarea
          id="virtual-model-description"
          rows="3"
          placeholder="Optional description"
          bind:value={vm.vmForm.description}
          disabled={vm.vmFormManaged}
        ></textarea>
      </div>

      <div class="vm-status-row">
        {#if vm.vmFormMode === "edit"}
          <span class="form-hint vm-status-summary">
            {"Default enabled: " +
              (vm.vmFormDefaultEnabled ? "yes" : "no") +
              " · Effective now: " +
              (vm.vmFormEffectiveEnabled ? "yes" : "no")}
          </span>
        {/if}
        <div class="vm-status-toggle">
          <button
            type="button"
            class="alias-toggle"
            class:enabled={vm.vmForm.enabled}
            class:restricted={vm.vmFormToggleRestricted()}
            aria-label={(vm.vmForm.enabled ? "Disable" : "Enable") + " virtual model"}
            disabled={vm.vmFormManaged}
            onclick={() => {
              if (!vm.vmFormManaged) {
                vm.vmForm.enabled = !vm.vmForm.enabled;
              }
            }}
          >
            <span class="alias-toggle-track"><span class="alias-toggle-thumb"></span></span>
            <span>{vm.vmFormToggleLabel()}</span>
          </button>
        </div>
      </div>

      {#if vm.vmFormError}
        <p class="form-error" role="alert" aria-live="assertive">{vm.vmFormError}</p>
      {/if}

      <div class="form-actions">
        <button type="button" class="btn" onclick={() => vm.closeVirtualModelForm()}>
          Cancel
        </button>
        {#if vm.vmFormHasExisting && !vm.vmFormManaged}
          <button
            type="button"
            class="btn btn-danger-outline"
            disabled={vm.vmDeleting || vm.vmSubmitting}
            onclick={() => vm.deleteVirtualModel()}
          >
            Remove
          </button>
        {/if}
        {#if !vm.vmFormManaged}
          <button
            type="submit"
            class="btn btn-primary btn-with-icon virtual-model-submit-btn"
            disabled={vm.vmSubmitting || vm.vmDeleting}
          >
            {#if vm.vmFormMode !== "edit"}
              <Icon name="plus" class="form-action-icon" />
            {:else}
              <Icon name="save" class="form-action-icon" />
            {/if}
            <span>{vm.vmSubmitting ? "Saving..." : vm.vmFormMode === "edit" ? "Save" : "Create"}</span>
          </button>
        {/if}
      </div>
    </form>
  </div>
</Modal>

<style>
  .vm-session-affinity-checkbox {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--text);
    cursor: pointer;
    user-select: none;
  }

  .vm-session-affinity-checkbox input {
    accent-color: var(--accent);
    cursor: pointer;
  }
</style>
