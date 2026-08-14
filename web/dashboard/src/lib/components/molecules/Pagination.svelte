<script>
  // Offset/limit pagination bar.
  import { i18n } from "$lib/i18n/i18n.svelte.js";

  let { total = 0, offset = 0, limit = 25, onprev, onnext } = $props();
</script>

{#if total > 0}
  <div class="pagination">
    <span class="pagination-info">
      {i18n.t("pagination.summary", {
        start: i18n.formatNumber(offset + 1),
        end: i18n.formatNumber(Math.min(offset + limit, total)),
        total: i18n.formatNumber(total),
      })}
    </span>
    <div class="pagination-buttons">
      <button
        type="button"
        class="btn"
        disabled={offset === 0}
        onclick={() => onprev?.()}>{i18n.t("common.actions.previous")}</button
      >
      <button
        type="button"
        class="btn"
        disabled={offset + limit >= total}
        onclick={() => onnext?.()}>{i18n.t("common.actions.next")}</button
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
