<script>
  // Pricing override editor (EditorDialog shell).
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { formatPriceFine } from "$lib/utils/format.js";
  import { pricingOverrides } from "./pricingOverrides.svelte.js";
  import { Plus, X } from "lucide";

  const po = pricingOverrides;
</script>

<EditorDialog
  open={po.modelPricingOverrideFormOpen}
  ariaLabel="Model pricing editor"
  dialogClass="model-pricing-editor"
  error={po.modelPricingOverrideError}
  submitting={po.modelPricingOverrideSubmitting}
  submitLabel="Save Pricing"
  onclose={() => po.closeModelPricingOverrideForm()}
  onsubmit={() => po.submitModelPricingOverrideForm()}
>
  {#snippet header()}
    <p class="form-kicker">Pricing override</p>
    <h3>{po.modelPricingOverrideFormDisplayName || po.modelPricingOverrideForm.selector || "Pricing"}</h3>
  {/snippet}

  <div class="form-grid">
    <FormField id="model-pricing-override-selector" label="Selector">
      <input
        id="model-pricing-override-selector"
        type="text"
        class="mono"
        bind:value={po.modelPricingOverrideForm.selector}
        disabled
      />
    </FormField>

    {#if po.modelPricingOverrideFormScopeOptions.length > 1}
      <FormField id="model-pricing-override-scope" label="Scope">
        <select
          id="model-pricing-override-scope"
          class="form-select"
          bind:value={po.modelPricingOverrideFormScope}
          onchange={() => po.setModelPricingOverrideScope(po.modelPricingOverrideFormScope)}
        >
          {#each po.modelPricingOverrideFormScopeOptions as option (option.value)}
            <option value={option.value}>{option.label}</option>
          {/each}
        </select>
      </FormField>
    {/if}
  </div>

  <p class="form-hint">
    Currency is USD. Saved fields override model registry and config.yaml pricing for this
    selector; unset fields continue to inherit.
  </p>

  <div class="pricing-override-rows">
    {#each po.modelPricingOverrideRows as row (row.id)}
      <div class="pricing-override-row">
        <div class="form-field pricing-override-type-field">
          <label class="form-field-label" for={"pricing-type-" + row.id}>Price Type</label>
          <select
            id={"pricing-type-" + row.id}
            class="form-select"
            bind:value={row.field}
            data-modal-autofocus
          >
            {#each po.availablePricingFieldOptions(row) as option (option.value)}
              <option value={option.value}>{option.group + " - " + option.label}</option>
            {/each}
          </select>
        </div>
        <div class="form-field pricing-override-value-field">
          <label class="form-field-label" for={"pricing-value-" + row.id}>USD Value</label>
          <input
            id={"pricing-value-" + row.id}
            type="number"
            step="any"
            min="0"
            inputmode="decimal"
            bind:value={row.value}
          />
        </div>
        <TableActionButton
          label={"Remove " + po.pricingFieldLabel(row.field)}
          class="table-action-btn-danger table-icon-btn pricing-override-remove-row"
          onclick={() => po.removeModelPricingOverrideRow(row)}
        >
          <Icon icon={X} class="table-icon-svg" />
        </TableActionButton>
      </div>
    {/each}
  </div>

  <div class="pricing-override-row-actions">
    <button
      type="button"
      class="btn btn-with-icon"
      onclick={() => po.addModelPricingOverrideRow()}
    >
      <Icon icon={Plus} class="form-action-icon" />
      <span>Add Price</span>
    </button>
  </div>

  {#if po.modelPricingOverrideFormPreservedTiers.length > 0}
    <div class="pricing-override-tier-note">
      Tiered pricing exists for this override and will be preserved. Tier editing can be added
      without a database migration.
    </div>
  {/if}

  <div class="pricing-preview">
    <div class="pricing-preview-header">
      <span>Price Type</span>
      <span>USD</span>
      <span>Source</span>
    </div>
    {#if po.modelPricingEffectivePreviewRows().length === 0}
      <div class="pricing-preview-row pricing-preview-row-empty">No pricing fields set.</div>
    {/if}
    {#each po.modelPricingEffectivePreviewRows() as row (row.field)}
      <div class="pricing-preview-row">
        <span>{row.label}</span>
        <span class="mono">
          {row.value === null || row.value === undefined ? "-" : formatPriceFine(Number(row.value))}
        </span>
        <span>{row.source}</span>
      </div>
    {/each}
  </div>

  {#snippet extraActions()}
    {#if po.modelPricingOverrideFormHasExistingOverride}
      <button
        type="button"
        class="btn btn-danger-outline"
        disabled={po.modelPricingOverrideSubmitting}
        onclick={() => po.deleteModelPricingOverride()}
      >
        Remove Override
      </button>
    {/if}
  {/snippet}
</EditorDialog>

<style>
.pricing-override-rows {
    display: grid;
    gap: 12px;
  }

.pricing-override-row {
    display: grid;
    grid-template-columns: minmax(220px, 1fr) minmax(130px, 180px) 32px;
    gap: 12px;
    align-items: end;
  }

.pricing-override-row-actions {
    display: flex;
    justify-content: flex-start;
  }

.pricing-override-tier-note {
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg);
    color: var(--text-muted);
    font-size: 13px;
  }

.pricing-preview {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }

.pricing-preview-header, .pricing-preview-row {
    display: grid;
    grid-template-columns: minmax(150px, 1fr) minmax(90px, auto) minmax(130px, 0.8fr);
    gap: 12px;
    align-items: center;
    padding: 10px 12px;
  }

.pricing-preview-header {
    background: var(--bg);
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 600;
    text-transform: uppercase;
  }

.pricing-preview-row {
    border-top: 1px solid var(--border);
    font-size: 13px;
  }

.pricing-preview-row-empty {
    grid-template-columns: 1fr;
    color: var(--text-muted);
  }

@media (max-width: 768px) {
  .pricing-override-row, .pricing-preview-header, .pricing-preview-row {
          grid-template-columns: 1fr;
        }
}
</style>
