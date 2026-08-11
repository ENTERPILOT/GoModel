<script>
  // Unified virtual-model editor (redirect/alias, load balancer, or access
  // policy) on the shared EditorDialog shell.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import EnabledToggle from "$lib/components/atoms/EnabledToggle.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { virtualModels } from "./virtualModels.svelte.js";
  import { qualifiedModelName } from "./virtualModelsLogic.js";
  import VmTargetRow from "./VmTargetRow.svelte";
  import { Plus, Save } from "lucide";

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
  ariaLabel="Virtual model editor"
  error={vm.vmFormError}
  submitting={vm.vmSubmitting}
  submitDisabled={vm.vmDeleting || vm.vmFormManaged}
  submitLabel={vm.vmFormMode === "edit" ? "Save" : "Create"}
  submitIcon={vm.vmFormMode !== "edit" ? Plus : Save}
  onclose={() => vm.closeVirtualModelForm()}
  onsubmit={() => vm.submitVirtualModelForm()}
>
  {#snippet header()}
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
        target.{#if runtimeConfig.virtualModelStrategies().includes("adaptive")}
          <code>adaptive</code> lets the registered routing extension pick the target per
          request.{/if} Leave <strong>Targets</strong> empty to make it only an access policy on the
        <code>Source</code> selector. The selector uses <code>/</code> for all providers and
        models, <code>{"{provider_name}"}/</code> for one provider, or
        <code>{"{provider_name}"}/{"{model}"}</code> for one model. <code>user_paths</code> is
        matched against the effective request <code>user_path</code>: the managed API key
        <code>user_path</code> when present, otherwise the configured user path request header.
      {/snippet}
    </InlineHelpSection>
  {/snippet}

  {#if vm.vmFormManaged}
    <p class="form-hint" role="status">
      This virtual model is defined in configuration (<code>config.yaml</code> /
      <code>VIRTUAL_MODELS</code>) and is read-only here. Edit your configuration to change it.
    </p>
  {/if}

  <FormField id="virtual-model-source" label="Source">
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
        <Icon icon={Plus} class="form-action-icon" />
        <span>Add target (load balancing)</span>
      </button>
    </div>
  </div>

  {#if vm.vmFormShowStrategy()}
    <FormField id="virtual-model-strategy" label="Load-balancing strategy">
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

  <FormField id="virtual-model-description" label="Description">
    <textarea
      id="virtual-model-description"
      rows="3"
      placeholder="Optional description"
      bind:value={vm.vmForm.description}
      disabled={vm.vmFormManaged}
    ></textarea>
  </FormField>

  {#if vm.vmFormSupportsSlowdown()}
    <FormField id="virtual-model-slowdown" label="Slowdown (extra inference time)">
      <input
        id="virtual-model-slowdown"
        type="number"
        min="0"
        max="10"
        step="0.1"
        placeholder="Off"
        bind:value={vm.vmForm.slowdown}
        disabled={vm.vmFormManaged}
      />
      <span class="form-hint">
        Adds a fraction of measured inference time: 0.5 adds 50%. Streaming chunks are buffered
        and released on the slowed timeline. Use 0 to override inherited slowdown, or leave empty
        to inherit it; active values are 0.1–10.
      </span>
    </FormField>
  {/if}

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
      <EnabledToggle
        enabled={vm.vmForm.enabled}
        restricted={vm.vmFormToggleRestricted()}
        label="virtual model"
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
