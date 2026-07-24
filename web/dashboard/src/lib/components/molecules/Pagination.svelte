<script>
  // Offset/limit pagination bar.
  let { total = 0, offset = 0, limit = 25, onprev, onnext } = $props();
</script>

{#if total > 0}
  <div class="pagination">
    <span class="pagination-info">
      Showing {offset + 1}-{Math.min(offset + limit, total)} of {total}
    </span>
    <div class="pagination-buttons">
      <button
        type="button"
        class="pagination-btn"
        disabled={offset === 0}
        onclick={() => onprev?.()}>Prev</button
      >
      <button
        type="button"
        class="pagination-btn"
        disabled={offset + limit >= total}
        onclick={() => onnext?.()}>Next</button
      >
    </div>
  </div>
{/if}
