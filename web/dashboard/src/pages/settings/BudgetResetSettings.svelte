<script>
  // Settings → Reset All Budgets: starts new budget periods for every
  // configured budget via the shared budgets store's typed confirmation.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { budgetsStore } from "$pages/budgets/budgets.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { RotateCcw } from "lucide";
</script>

{#if runtimeConfig.budgetsVisible()}
  <div class="settings-refresh-section budget-reset-all-section">
    <div>
      <h3>Reset All Budgets</h3>
      <p>
        Start new budget periods for every configured budget without changing
        the limits.
      </p>
    </div>
    <div class="settings-refresh-actions budget-settings-actions">
      <button
        type="button"
        class="btn btn-danger btn-with-icon"
        disabled={budgetsStore.resetAllLoading}
        onclick={() => budgetsStore.openResetDialog()}
      >
        <Icon icon={RotateCcw} class="form-action-icon" />
        <span>Reset Budgets</span>
      </button>
    </div>
  </div>
{/if}
