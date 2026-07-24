<script>
  // Enable/restrict/disable switch shared by model rows, alias rows, provider
  // groups, and the global scope.
  import { virtualModels } from "./virtualModels.svelte.js";

  let { row } = $props();
</script>

<button
  type="button"
  class="alias-toggle"
  class:enabled={virtualModels.rowToggleEnabled(row)}
  class:restricted={virtualModels.rowToggleRestricted(row)}
  disabled={virtualModels.rowTogglingKey === row.key || !virtualModels.virtualModelsAvailable}
  aria-label={virtualModels.rowToggleAriaLabel(row)}
  onclick={() => virtualModels.toggleRowEnabled(row)}
>
  <span class="alias-toggle-track"><span class="alias-toggle-thumb"></span></span>
  <span>{virtualModels.rowToggleLabel(row)}</span>
</button>
