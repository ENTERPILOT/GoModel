// Per-category column spec for the models table — the single source of
// truth for the table header (ModelTable) and the row cells (ModelRow), so
// the two can never drift. Each column: headerLines (multi-line headers
// render with <br>), optional cell class, and value(row, pricing).
// Relative import (not $lib) so node:test can resolve this module too.
import { formatPrice, formatPriceFine } from "../../lib/utils/format.js";

const modes = {
  headerLines: ["Modes"],
  value: (row) => (row.model?.metadata?.modes ?? []).join(", ") || "-",
};

function price(headerLines, value) {
  return { headerLines, class: "col-price", value };
}

const inputOutput = price(
  ["Input / Output ($/MTok)"],
  (row, p) => formatPrice(p?.input_per_mtok) + " / " + formatPrice(p?.output_per_mtok),
);

const CATEGORY_COLUMNS = {
  all: [modes, inputOutput],
  text_generation: [
    modes,
    inputOutput,
    price(["Cached $/MTok"], (row, p) => formatPrice(p?.cached_input_per_mtok)),
  ],
  embedding: [price(["Input", "$/MTok"], (row, p) => formatPrice(p?.input_per_mtok))],
  image: [price(["$/Image"], (row, p) => formatPriceFine(p?.per_image))],
  audio: [
    price(["$/Second"], (row, p) => formatPriceFine(p?.per_second_input)),
    price(["$/Character"], (row, p) => formatPriceFine(p?.per_character_input)),
  ],
  video: [
    price(["$/Second (In)"], (row, p) => formatPriceFine(p?.per_second_input)),
    price(["$/Second (Out)"], (row, p) => formatPriceFine(p?.per_second_output)),
  ],
  utility: [
    price(["$/Page"], (row, p) => formatPriceFine(p?.per_page)),
    price(["$/Request"], (row, p) => formatPriceFine(p?.per_request)),
  ],
};

export function categoryColumns(category) {
  return CATEGORY_COLUMNS[category] || CATEGORY_COLUMNS.all;
}

// Full table width: the Model column + category columns + the actions column.
export function categoryColspan(category) {
  return categoryColumns(category).length + 2;
}
