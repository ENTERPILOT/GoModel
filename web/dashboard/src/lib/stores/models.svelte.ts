// Shared model inventory (/admin/models) and categories. Several pages read
// this: Models renders it, editors use it for datalists, and the overview
// links into filtered views.

import { getJSON, isAbortError } from "$lib/api/client.ts";

// One /admin/models row. The catalog carries more fields than the store
// needs; the index signatures keep pass-through access open to pages.
export interface ModelEntry {
  selector?: string;
  provider_name?: string;
  provider_type?: string;
  model?: {
    id?: string;
    owned_by?: string;
    metadata?: {
      modes?: string[];
      categories?: string[];
      [key: string]: unknown;
    };
    [key: string]: unknown;
  };
  [key: string]: unknown;
}

export interface CategoryEntry {
  category: string;
  count: number;
}

class ModelsStore {
  models = $state<ModelEntry[]>([]);
  categories = $state<CategoryEntry[]>([]);
  activeCategory = $state("all");
  filter = $state("");
  // Start busy so a direct visit to the Models route can paint its loader
  // before the first inventory request starts.
  loading = $state(true);
  #controller: AbortController | null = null;

  async fetchModels(): Promise<void> {
    if (this.#controller) this.#controller.abort();
    const controller = new AbortController();
    this.#controller = controller;
    this.loading = true;
    try {
      let url = "/admin/models";
      if (this.activeCategory && this.activeCategory !== "all") {
        url += "?category=" + encodeURIComponent(this.activeCategory);
      }
      const result = await getJSON(url, {
        label: "models",
        signal: controller.signal,
      });
      if (result.stale || controller.signal.aborted) return;
      this.models =
        result.ok && Array.isArray(result.data)
          ? (result.data as ModelEntry[])
          : [];
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch models:", e);
      this.models = [];
    } finally {
      if (this.#controller === controller) {
        this.#controller = null;
        this.loading = false;
      }
    }
  }

  async fetchCategories(): Promise<void> {
    try {
      const result = await getJSON("/admin/models/categories", {
        label: "categories",
      });
      if (result.stale) return;
      this.categories =
        result.ok && Array.isArray(result.data)
          ? (result.data as CategoryEntry[])
          : [];
    } catch (e) {
      console.error("Failed to fetch categories:", e);
      this.categories = [];
    }
  }

  selectCategory(cat: string): void {
    this.activeCategory = cat;
    this.filter = "";
    this.fetchModels();
  }

  categoryCount(cat: string): number {
    const entry = this.categories.find((c) => c.category === cat);
    return entry ? entry.count : 0;
  }

  get filteredModels(): ModelEntry[] {
    if (!this.filter) return this.models;
    const f = this.filter.toLowerCase();
    return this.models.filter(
      (m) =>
        (m.model?.id ?? "").toLowerCase().includes(f) ||
        (m.provider_name ?? "").toLowerCase().includes(f) ||
        (m.provider_type ?? "").toLowerCase().includes(f) ||
        (m.selector ?? "").toLowerCase().includes(f) ||
        (m.model?.owned_by ?? "").toLowerCase().includes(f) ||
        (m.model?.metadata?.modes ?? []).join(",").toLowerCase().includes(f) ||
        (m.model?.metadata?.categories ?? [])
          .join(",")
          .toLowerCase()
          .includes(f),
    );
  }
}

export const modelsStore = new ModelsStore();
