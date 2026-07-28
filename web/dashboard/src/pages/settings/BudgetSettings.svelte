<script>
  // Budget reset-anchor settings (GET/PUT /admin/budgets/settings). Only the
  // budget-settings section lives here; the budgets page itself is
  // src/pages/budgets/.
  import { auth } from "$lib/stores/auth.svelte.js";
  import { flash } from "$lib/stores/flash.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { getJSON, sendJSON } from "$lib/api/client.js";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import {
    defaultBudgetSettings,
    normalizeBudgetSettings,
    budgetWeekdays,
  } from "./budget-settings-logic.js";

  let settings = $state(defaultBudgetSettings());
  let loading = $state(false);
  let saving = $state(false);
  // Load failures only; save feedback goes through the flash store.
  let error = $state("");
  let monthlyDayHelpOpen = $state(false);

  const enabled = $derived(runtimeConfig.budgetsVisible());

  async function load() {
    await runtimeConfig.ensureLoaded();
    if (!runtimeConfig.budgetsVisible()) {
      error = "";
      return;
    }
    loading = true;
    error = "";
    try {
      const result = await getJSON("/admin/budgets/settings", {
        label: "budget settings",
      });
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        error = "Unable to load budget settings.";
        return;
      }
      settings = normalizeBudgetSettings(result.data, settings);
    } catch (e) {
      console.error("Failed to fetch budget settings:", e);
      error = "Unable to load budget settings.";
    } finally {
      loading = false;
    }
  }

  async function save() {
    if (saving) {
      return;
    }
    saving = true;
    try {
      const result = await sendJSON(
        "/admin/budgets/settings",
        "PUT",
        normalizeBudgetSettings(settings, settings),
        { label: "budget settings" },
      );
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        flash.error("Unable to save budget settings.");
        return;
      }
      settings = normalizeBudgetSettings(result.data, settings);
      // A successful save proves the endpoint works and delivered fresh
      // data, so a load error from a failed earlier fetch is obsolete.
      error = "";
      flash.success("Budget settings saved.");
    } catch (e) {
      console.error("Failed to save budget settings:", e);
      flash.error("Unable to save budget settings.");
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    void auth.refreshTick;
    load();
  });
</script>

{#if enabled}
  <div class="settings-refresh-section budget-settings-section">
    <InlineHelpSection
      copyId="budget-settings-help-copy"
      label="budget help"
      text="Budget reset anchors are stored in the database and evaluated in UTC. Hourly budgets reset at the top of each hour."
    >
      {#snippet title()}<h3>Budget Resets</h3>{/snippet}
    </InlineHelpSection>
    <div class="budget-settings-grid">
      <div class="budget-settings-row">
        <div class="budget-settings-period">Monthly</div>
        <div class="form-field">
          <!-- external: the copy renders in the budget-settings-help-cell
               grid cell below, keyed off the same bound open state. -->
          <InlineHelpSection
            copyId="budget-monthly-day-help-copy"
            label="day of month help"
            external
            bind:open={monthlyDayHelpOpen}
          >
            {#snippet title()}
              <label class="form-field-label" for="budget-monthly-day"
                >Day of Month</label
              >
            {/snippet}
          </InlineHelpSection>
          <input
            id="budget-monthly-day"
            class="form-input"
            type="number"
            min="1"
            max="31"
            step="1"
            bind:value={settings.monthly_reset_day}
            aria-describedby="budget-monthly-day-help-copy"
          />
        </div>
        <div class="form-field">
          <label class="form-field-label" for="budget-monthly-hour">Hour</label>
          <input
            id="budget-monthly-hour"
            class="form-input"
            type="number"
            min="0"
            max="23"
            step="1"
            bind:value={settings.monthly_reset_hour}
          />
        </div>
        <div class="form-field">
          <label class="form-field-label" for="budget-monthly-minute"
            >Minute</label
          >
          <input
            id="budget-monthly-minute"
            class="form-input"
            type="number"
            min="0"
            max="59"
            step="1"
            bind:value={settings.monthly_reset_minute}
          />
        </div>
        <div class="budget-settings-help-cell">
          {#if monthlyDayHelpOpen}
            <p id="budget-monthly-day-help-copy" class="inline-help-copy">
              If the selected day does not exist in a month, the reset runs on
              the last day of that month.
            </p>
          {/if}
        </div>
      </div>
      <div class="budget-settings-row">
        <div class="budget-settings-period">Weekly</div>
        <div class="form-field">
          <label class="form-field-label" for="budget-weekly-day"
            >Day of Week</label
          >
          <select
            id="budget-weekly-day"
            class="form-select settings-select"
            bind:value={settings.weekly_reset_weekday}
          >
            {#each budgetWeekdays() as day (day.value)}
              <option value={day.value}>{day.label}</option>
            {/each}
          </select>
        </div>
        <div class="form-field">
          <label class="form-field-label" for="budget-weekly-hour">Hour</label>
          <input
            id="budget-weekly-hour"
            class="form-input"
            type="number"
            min="0"
            max="23"
            step="1"
            bind:value={settings.weekly_reset_hour}
          />
        </div>
        <div class="form-field">
          <label class="form-field-label" for="budget-weekly-minute"
            >Minute</label
          >
          <input
            id="budget-weekly-minute"
            class="form-input"
            type="number"
            min="0"
            max="59"
            step="1"
            bind:value={settings.weekly_reset_minute}
          />
        </div>
        <div class="budget-settings-help-cell" aria-hidden="true"></div>
      </div>
      <div class="budget-settings-row">
        <div class="budget-settings-period">Daily</div>
        <div class="budget-settings-spacer" aria-hidden="true"></div>
        <div class="form-field">
          <label class="form-field-label" for="budget-daily-hour">Hour</label>
          <input
            id="budget-daily-hour"
            class="form-input"
            type="number"
            min="0"
            max="23"
            step="1"
            bind:value={settings.daily_reset_hour}
          />
        </div>
        <div class="form-field">
          <label class="form-field-label" for="budget-daily-minute"
            >Minute</label
          >
          <input
            id="budget-daily-minute"
            class="form-input"
            type="number"
            min="0"
            max="59"
            step="1"
            bind:value={settings.daily_reset_minute}
          />
        </div>
        <div class="budget-settings-help-cell" aria-hidden="true"></div>
      </div>
    </div>
    <div class="settings-refresh-actions budget-settings-actions">
      <button
        type="button"
        class="btn btn-primary btn-with-icon"
        disabled={saving || loading}
        aria-busy={saving ? "true" : "false"}
        aria-describedby="budget-settings-help-copy"
        onclick={save}
      >
        <Icon name="save" class="form-action-icon" />
        <span>Save Budget Settings</span>
      </button>
      {#if loading}
        <Spinner size={16} label="Loading budget settings" />
      {/if}
    </div>
  </div>

  <div>
    {#if error}
      <div class="alert alert-warning settings-refresh-alert" role="alert" aria-live="assertive">
        {error}
      </div>
    {/if}
  </div>
{/if}

<style>
  .budget-settings-section {
    width: 100%;
  }

  .budget-settings-grid {
    display: grid;
    gap: 12px;
  }

  .budget-settings-row {
    display: grid;
    grid-template-columns: 96px minmax(170px, 1fr) minmax(110px, 140px) minmax(110px, 140px) minmax(220px, 280px);
    align-items: start;
    gap: 12px;
  }

  .budget-settings-period {
    align-self: end;
    color: var(--text);
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0;
    min-height: 35px;
    padding-bottom: 9px;
    text-transform: uppercase;
  }

  .budget-settings-spacer {
    min-height: 1px;
  }

  .budget-settings-help-cell {
    align-self: start;
    min-width: 0;
    min-height: 35px;
  }

  .budget-settings-help-cell :global(.inline-help-copy) {
    margin: 22px 0 0;
    max-width: 280px;
    font-size: 12px;
    line-height: 1.35;
  }

  @media (max-width: 768px) {
    .budget-settings-grid {
        grid-template-columns: 1fr;
      }

    .budget-settings-row {
        grid-template-columns: 1fr;
      }

    .budget-settings-spacer {
        display: none;
      }
  }
</style>
