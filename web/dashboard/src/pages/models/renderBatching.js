// Incremental render batching for large model lists.
// Pure logic (no Svelte) so the node:test suite can exercise it directly;
// relative imports keep it loadable outside Vite.

// computeRenderStep advances the bounded row-render window by one batch.
export function computeRenderStep(currentLimit, batchSize, total) {
  const size = Math.max(1, Number(batchSize || 75));
  const limit = Math.min(total, currentLimit + size);
  return { limit, rendering: limit < total };
}

// initialRenderStep starts a fresh render pass with the first batch.
export function initialRenderStep(batchSize, total) {
  return computeRenderStep(0, batchSize, total);
}
