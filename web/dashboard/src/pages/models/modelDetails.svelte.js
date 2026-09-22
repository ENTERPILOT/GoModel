// Which model rows have their details accordion open. Keyed by the display
// row key, so an open panel survives inventory refetches, category batching
// and virtual-model mutations that rebuild the rows.

class ModelDetailsState {
  #expanded = $state({});

  isExpanded(row) {
    return Boolean(row && row.key && this.#expanded[row.key]);
  }

  toggle(row) {
    if (!row || !row.key) return;
    const next = { ...this.#expanded };
    if (next[row.key]) {
      delete next[row.key];
    } else {
      next[row.key] = true;
    }
    this.#expanded = next;
  }
}

export const modelDetailsState = new ModelDetailsState();
