// Curate suggestions only: the routing inventory and saved/custom selectors
// remain intact. Provider type identifies renamed MiniMax instances.
export function minimaxTextModelRank(entry) {
  const type = String(entry?.provider_type || entry?.provider_name || "").toLowerCase();
  if (type !== "minimax" && type !== "openrouter") return null;
  const model = String(entry?.model?.id || "").toLowerCase();
  const modes = entry?.model?.metadata?.modes;
  if (Array.isArray(modes) && modes.length &&
      !modes.some((mode) => ["chat", "responses", "messages"].includes(mode))) return null;
  if (type === "openrouter") {
    if (!model.startsWith("minimax/")) return null;
    if (model === "minimax/minimax-m3.1-flash:free") return 0;
    if (model === "minimax/minimax-m3") return 1;
    return -1;
  }
  if (!model.startsWith("minimax-")) return null;
  if (model === "minimax-m3.1-flash") return 0;
  if (model === "minimax-m3") return 1;
  return -1;
}

export function suggestedModelInventory(inventory) {
  const entries = (Array.isArray(inventory) ? inventory : [])
    .filter((entry) => minimaxTextModelRank(entry) !== -1);
  // Replace only MiniMax text slots, preserving the order of other providers
  // and nontext capabilities in the caller's already sorted inventory.
  const current = entries.filter((entry) => minimaxTextModelRank(entry) !== null)
    .sort((a, b) => minimaxTextModelRank(a) - minimaxTextModelRank(b));
  let index = 0;
  return entries.map((entry) => minimaxTextModelRank(entry) === null ? entry : current[index++]);
}
