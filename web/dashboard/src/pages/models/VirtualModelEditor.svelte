<script>
  // Unified virtual-model editor (redirect/alias, load balancer, or access
  // policy) on the shared EditorDialog shell.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import EnabledToggle from "$lib/components/atoms/EnabledToggle.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { virtualModels } from "./virtualModels.svelte.js";
  import VmTargetRow from "./VmTargetRow.svelte";
  import { Plus, Save } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const vm = virtualModels;

  // The strategy dropdown is server-driven (VIRTUAL_MODEL_STRATEGIES); make
  // sure the runtime config is loaded by the time the editor shows it.
  $effect(() => {
    if (vm.vmFormOpen) {
      runtimeConfig.ensureLoaded();
    }
  });
</script>

<EditorDialog
  open={vm.vmFormOpen}
  ariaLabel={m.models_editor_label()}
  error={vm.vmFormError}
  submitting={vm.vmSubmitting}
  submitDisabled={vm.vmDeleting || vm.vmFormManaged}
  submitLabel={vm.vmFormMode === "edit" ? m.models_save() : m.models_create()}
  submitIcon={vm.vmFormMode !== "edit" ? Plus : Save}
  onclose={() => vm.closeVirtualModelForm()}
  onsubmit={() => vm.submitVirtualModelForm()}
>
  {#snippet header()}
    <InlineHelpSection
      copyId="virtual-model-help-copy"
      label={m.models_help_label()}
      bind:open={vm.vmFormHelpOpen}
    >
      {#snippet title()}
        <h3>{vm.vmFormDisplayName || vm.vmForm.source || m.models_virtual_model()}</h3>
      {/snippet}
      {#snippet help()}
        {m.models_help()}
      {/snippet}
    </InlineHelpSection>
  {/snippet}

  {#if vm.vmFormManaged}
    <p class="form-hint" role="status">
      {m.models_managed_help()}
    </p>
  {/if}

  <FormField id="virtual-model-source" label={m.models_source()}>
    <input
      id="virtual-model-source"
      type="text"
      class="mono"
      placeholder="smart"
      bind:value={vm.vmForm.source}
      disabled={vm.vmFormSourceLocked || vm.vmFormManaged}
      data-modal-autofocus
    />
  </FormField>

  <!-- Every target renders the same; two or more turn the redirect into a load balancer. -->
  <div class="form-field">
    <span class="form-field-label">{m.models_targets()}</span>
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
        <Icon icon={Plus} class="form-action-icon" />
        <span>{m.models_add_target()}</span>
      </button>
    </div>
  </div>

  {#if vm.vmFormShowStrategy()}
    <FormField id="virtual-model-strategy" label={m.models_strategy()}>
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
    </FormField>
    {#if vm.vmFormShowSessionAffinity()}
      <div class="form-field">
        <label class="vm-option-checkbox">
          <input
            type="checkbox"
            bind:checked={vm.vmForm.session_affinity}
            disabled={vm.vmFormManaged}
          />
          <span>{m.models_session_keeping()}</span>
        </label>
      </div>
    {/if}
    {#if vm.vmFormShowFailover()}
      <div class="form-field">
        <label class="vm-option-checkbox">
          <input
            type="checkbox"
            bind:checked={vm.vmForm.failover}
            disabled={vm.vmFormManaged}
          />
          <span>{m.models_failover_option()}</span>
        </label>
      </div>
    {/if}
  {/if}

  <div class="form-field">
    <InlineHelpSection
      copyId="virtual-model-user-paths-help"
      label={m.models_user_paths_help_label()}
      bind:open={vm.vmFormUserPathsHelpOpen}
    >
      {#snippet title()}
        <label class="form-field-label" for="virtual-model-user-paths">{m.models_user_paths()}</label>
      {/snippet}
      {#snippet help()}
        {m.models_user_paths_help()}
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

  <FormField id="virtual-model-description" label={m.models_description()}>
    <textarea
      id="virtual-model-description"
      rows="3"
      placeholder={m.models_description_placeholder()}
      bind:value={vm.vmForm.description}
      disabled={vm.vmFormManaged}
    ></textarea>
  </FormField>

  {#if vm.vmFormSupportsSlowdown()}
    <FormField id="virtual-model-slowdown" label={m.models_slowdown_field()}>
      <input
        id="virtual-model-slowdown"
        type="number"
        min="0"
        max="10"
        step="0.1"
        placeholder={m.models_off()}
        bind:value={vm.vmForm.slowdown}
        disabled={vm.vmFormManaged}
      />
      <span class="form-hint">
        {m.models_slowdown_help()}
      </span>
    </FormField>
  {/if}

  <div class="vm-status-row">
    {#if vm.vmFormMode === "edit"}
      <span class="form-hint vm-status-summary">
        {m.models_status_summary({
          default: vm.vmFormDefaultEnabled ? m.models_yes() : m.models_no(),
          effective: vm.vmFormEffectiveEnabled ? m.models_yes() : m.models_no(),
        })}
      </span>
    {/if}
    <div class="vm-status-toggle">
      <EnabledToggle
        enabled={vm.vmForm.enabled}
        restricted={vm.vmFormToggleRestricted()}
        label={m.models_virtual_model_toggle()}
        disabled={vm.vmFormManaged}
        text={vm.vmFormToggleLabel()}
        onclick={() => {
          if (!vm.vmFormManaged) {
            vm.vmForm.enabled = !vm.vmForm.enabled;
          }
        }}
      />
    </div>
  </div>

  {#snippet extraActions()}
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
  {/snippet}
</EditorDialog>

<style>
  .vm-option-checkbox {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--text);
    cursor: pointer;
    user-select: none;
  }

  .vm-option-checkbox input {
    accent-color: var(--accent);
    cursor: pointer;
  }
</style>
