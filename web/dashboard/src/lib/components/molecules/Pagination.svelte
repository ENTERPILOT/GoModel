<script>
  // Offset/limit pagination bar.
  import { formatNumber } from "$lib/i18n/locale.js";
  import * as m from "$lib/paraglide/messages.js";

  let { total = 0, offset = 0, limit = 25, onprev, onnext } = $props();
</script>

{#if total > 0}
  <div class="pagination">
    <span class="pagination-info">
      {m.pagination_summary({
        start: formatNumber(offset + 1),
        end: formatNumber(Math.min(offset + limit, total)),
        total: formatNumber(total),
      })}
    </span>
    <div class="pagination-buttons">
      <button
        type="button"
        class="btn"
        disabled={offset === 0}
        onclick={() => onprev?.()}>{m.common_action_previous()}</button
      >
      <button
        type="button"
        class="btn"
        disabled={offset + limit >= total}
        onclick={() => onnext?.()}>{m.common_action_next()}</button
      >
    </div>
  </div>
{/if}

<style>
  .pagination-info {
    font-size: 13px;
    color: var(--text-muted);
  }

  .pagination-buttons {
    display: flex;
    gap: 8px;
  }
</style>
