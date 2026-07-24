<script>
  // Pricing override editor modal.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { formatPriceFine } from "$lib/utils/format.js";
  import { pricingOverrides } from "./pricingOverrides.svelte.js";
  import TableIcon from "./TableIcon.svelte";

  const po = pricingOverrides;
</script>

<Modal open={po.modelPricingOverrideFormOpen} onclose={() => po.closeModelPricingOverrideForm()}>
  <div
    class="model-editor model-pricing-editor"
    role="dialog"
    aria-modal="true"
    aria-label="Model pricing editor"
  >
    <form
      class="form"
      onsubmit={(event) => {
        event.preventDefault();
        po.submitModelPricingOverrideForm();
      }}
    >
      <div class="editor-header">
        <div>
          <p class="form-kicker">Pricing override</p>
          <h3>{po.modelPricingOverrideFormDisplayName || po.modelPricingOverrideForm.selector || "Pricing"}</h3>
        </div>
        <DialogCloseButton
          label="Close model pricing editor"
          onclick={() => po.closeModelPricingOverrideForm()}
        />
      </div>

      <div class="form-grid">
        <div class="form-field">
          <label class="form-field-label" for="model-pricing-override-selector">Selector</label>
          <input
            id="model-pricing-override-selector"
            type="text"
            class="mono"
            bind:value={po.modelPricingOverrideForm.selector}
            disabled
          />
        </div>

        {#if po.modelPricingOverrideFormScopeOptions.length > 1}
          <div class="form-field">
            <label class="form-field-label" for="model-pricing-override-scope">Scope</label>
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
          </div>
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
              <TableIcon name="x" />
            </TableActionButton>
          </div>
        {/each}
      </div>

      <div class="pricing-override-row-actions">
        <button
          type="button"
          class="pagination-btn pagination-btn-with-icon"
          onclick={() => po.addModelPricingOverrideRow()}
        >
          <Icon name="plus" class="form-action-icon" />
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

      {#if po.modelPricingOverrideError}
        <p class="form-error" role="alert" aria-live="assertive">{po.modelPricingOverrideError}</p>
      {/if}

      <div class="form-actions">
        <button type="button" class="pagination-btn" onclick={() => po.closeModelPricingOverrideForm()}>
          Cancel
        </button>
        {#if po.modelPricingOverrideFormHasExistingOverride}
          <button
            type="button"
            class="pagination-btn pagination-btn-danger-outline"
            disabled={po.modelPricingOverrideSubmitting}
            onclick={() => po.deleteModelPricingOverride()}
          >
            Remove Override
          </button>
        {/if}
        <button
          type="submit"
          class="pagination-btn pagination-btn-primary pagination-btn-with-icon model-pricing-submit-btn"
          disabled={po.modelPricingOverrideSubmitting}
        >
          <Icon name="save" class="form-action-icon" />
          <span>{po.modelPricingOverrideSubmitting ? "Saving..." : "Save Pricing"}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>
