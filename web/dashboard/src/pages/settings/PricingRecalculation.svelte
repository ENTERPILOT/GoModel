<script>
  // Usage pricing recalculation (POST /admin/usage/recalculate-pricing),
  // gated on the USAGE_PRICING_RECALCULATION_ENABLED runtime flag and
  // guarded by the shared typed-confirmation dialog ("recalculate").
  import { flash } from "$lib/stores/flash.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { dateRange } from "$lib/stores/dateRange.svelte.js";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { confirmDialog } from "$lib/stores/confirm.svelte.js";
  import { usageData } from "$lib/stores/usageData.svelte.js";
  import { sendJSON } from "$lib/api/client.js";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import DatePicker from "$lib/components/molecules/DatePicker.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import {
    pricingRecalculatePayload,
    pricingRecalculateSummary,
  } from "./pricing-logic.js";

  let userPath = $state("");
  let selector = $state("");
  let loading = $state(false);

  const enabled = $derived(
    runtimeConfig.booleanFlag("USAGE_PRICING_RECALCULATION_ENABLED", false),
  );

  function openDialog() {
    if (!enabled) {
      flash.error("Usage pricing recalculation is unavailable.");
      return;
    }
    if (loading) {
      return;
    }
    confirmDialog.open({
      title: "Recalculate Pricing",
      titleId: "pricingRecalculateDialogTitle",
      inputId: "pricing-recalculate-confirmation",
      requiredText: "recalculate",
      confirmLabel: "Recalculate Pricing",
      icon: "calculator",
      dialogClass: "pricing-recalculate-dialog",
      message:
        "Stored usage cost fields matching the selected filters will be overwritten.",
      onConfirm: () => recalculate(),
    });
  }

  async function recalculate() {
    if (!enabled) {
      flash.error("Usage pricing recalculation is unavailable.");
      return;
    }
    if (loading) {
      return;
    }
    loading = true;
    try {
      const payload = pricingRecalculatePayload(
        {
          selectedPreset: dateRange.selectedPreset,
          customStartDate: dateRange.customStartDate,
          customEndDate: dateRange.customEndDate,
          today: timezone.todayDate(),
        },
        userPath,
        selector,
        "recalculate",
      );
      const result = await sendJSON(
        "/admin/usage/recalculate-pricing",
        "POST",
        payload,
        { label: "pricing recalculation" },
      );
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        confirmDialog.error = "Unable to recalculate pricing.";
        return;
      }
      confirmDialog.close();
      flash.success(pricingRecalculateSummary(result.data));
      void usageData.fetchUsage();
    } catch (e) {
      console.error("Failed to recalculate pricing:", e);
      confirmDialog.error = "Unable to recalculate pricing.";
    } finally {
      loading = false;
    }
  }
</script>

{#if enabled}
  <div class="settings-refresh-section pricing-recalculate-section">
    <InlineHelpSection
      copyId="pricing-recalculate-help-copy"
      label="pricing recalculation help"
      text="Recalculate stored input, output, total, and Pro Saved costs from the current model pricing metadata. This overwrites matching historical cost fields. Filters are applied to the selected date range, user path subtree, and provider/model selector or alias."
    >
      {#snippet title()}<h3>Usage Pricing Recalculation</h3>{/snippet}
    </InlineHelpSection>
    <div class="pricing-recalculate-grid">
      <div class="form-field pricing-recalculate-date-field">
        <!-- svelte-ignore a11y_label_has_associated_control -- the picker is labelled via aria-labelledby -->
        <label class="form-field-label" id="pricing-recalculate-date-label"
          >Date Range</label
        >
        <div aria-labelledby="pricing-recalculate-date-label">
          <DatePicker />
        </div>
      </div>
      <div class="form-field pricing-recalculate-filter-field">
        <label class="form-field-label" for="pricing-recalculate-user-path"
          >User Path (optional)</label
        >
        <input
          id="pricing-recalculate-user-path"
          class="form-input"
          type="text"
          placeholder="/team/alpha"
          bind:value={userPath}
        />
      </div>
      <div
        class="form-field pricing-recalculate-filter-field pricing-recalculate-selector-field"
      >
        <label class="form-field-label" for="pricing-recalculate-selector"
          >Provider/Model or Alias (optional)</label
        >
        <input
          id="pricing-recalculate-selector"
          class="form-input"
          type="text"
          placeholder="openai/gpt-4o or smart"
          bind:value={selector}
        />
      </div>
    </div>
    <div class="settings-refresh-actions pricing-recalculate-actions">
      <button
        type="button"
        class="btn btn-danger btn-with-icon"
        disabled={loading || !enabled}
        aria-busy={loading ? "true" : "false"}
        aria-describedby="pricing-recalculate-help-copy"
        onclick={openDialog}
      >
        <Icon name="calculator" class="form-action-icon" />
        <span>Recalculate Pricing</span>
      </button>
    </div>
  </div>
{/if}

<style>
.pricing-recalculate-section {
    width: 100%;
  }

.pricing-recalculate-grid {
    display: grid;
    grid-template-columns: minmax(220px, 320px) minmax(260px, 360px);
    align-items: end;
    justify-content: start;
    gap: 12px;
    width: 100%;
  }

.pricing-recalculate-date-field {
    grid-column: 1 / -1;
    max-width: 320px;
    width: 100%;
  }

.pricing-recalculate-filter-field {
    max-width: 360px;
    width: 100%;
  }

/* The picker and its trigger render inside the DatePicker child, so :global
   reaches them; the field class keeps these rules winning over DatePicker's
   scoped bases. */
.pricing-recalculate-date-field :global(.date-picker) {
    width: 100%;
  }

.pricing-recalculate-date-field :global(.date-picker-trigger) {
    width: 100%;
    justify-content: space-between;
  }

.pricing-recalculate-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
  }

@media (max-width: 768px) {
  .pricing-recalculate-grid {
          grid-template-columns: 1fr;
        }

  .pricing-recalculate-actions {
          width: 100%;
        }

  .pricing-recalculate-actions :global(.btn) {
          width: 100%;
        }
}

  /* Dropdown positioning override; the dropdown renders inside the
     DatePicker child, so :global reaches it while the field class
     keeps this rule winning over DatePicker's scoped base. */
  .pricing-recalculate-date-field :global(.date-picker-dropdown) {
        top: auto;
        bottom: calc(100% + 6px);
        right: auto;
        left: 0;
      }
</style>
