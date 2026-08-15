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
  import { Calculator } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  let userPath = $state("");
  let selector = $state("");
  let loading = $state(false);

  const enabled = $derived(
    runtimeConfig.booleanFlag("USAGE_PRICING_RECALCULATION_ENABLED", false),
  );

  function openDialog() {
    if (!enabled) {
      flash.error(m.settings_pricing_unavailable());
      return;
    }
    if (loading) {
      return;
    }
    confirmDialog.open({
      title: m.settings_pricing_action(),
      titleId: "pricingRecalculateDialogTitle",
      inputId: "pricing-recalculate-confirmation",
      requiredText: m.settings_pricing_confirmation(),
      confirmLabel: m.settings_pricing_action(),
      icon: Calculator,
      dialogClass: "pricing-recalculate-dialog",
      message: m.settings_pricing_confirmation_message(),
      onConfirm: () => recalculate(),
    });
  }

  async function recalculate() {
    if (!enabled) {
      flash.error(m.settings_pricing_unavailable());
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
        m.settings_pricing_confirmation(),
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
        confirmDialog.error = m.settings_pricing_failed();
        return;
      }
      confirmDialog.close();
      flash.success(pricingRecalculateSummary(result.data));
      void usageData.fetchUsage();
    } catch (e) {
      console.error("Failed to recalculate pricing:", e);
      confirmDialog.error = m.settings_pricing_failed();
    } finally {
      loading = false;
    }
  }
</script>

{#if enabled}
  <div class="settings-refresh-section pricing-recalculate-section">
    <InlineHelpSection
      copyId="pricing-recalculate-help-copy"
      label={m.settings_pricing_help_label()}
      text={m.settings_pricing_help()}
    >
      {#snippet title()}<h3>{m.settings_pricing_title()}</h3>{/snippet}
    </InlineHelpSection>
    <div class="pricing-recalculate-grid">
      <div class="form-field pricing-recalculate-date-field">
        <!-- svelte-ignore a11y_label_has_associated_control -- the picker is labelled via aria-labelledby -->
        <label class="form-field-label" id="pricing-recalculate-date-label"
          >{m.settings_pricing_date_range()}</label
        >
        <div aria-labelledby="pricing-recalculate-date-label">
          <DatePicker />
        </div>
      </div>
      <div class="form-field pricing-recalculate-filter-field">
        <label class="form-field-label" for="pricing-recalculate-user-path"
          >{m.settings_pricing_user_path()}</label
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
          >{m.settings_pricing_selector()}</label
        >
        <input
          id="pricing-recalculate-selector"
          class="form-input"
          type="text"
          placeholder={m.settings_pricing_selector_placeholder()}
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
        <Icon icon={Calculator} class="form-action-icon" />
        <span>{m.settings_pricing_action()}</span>
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
