// Per-category column spec for the models table — the single source of
// truth for the table header (ModelTable) and the row cells (ModelRow), so
// the two can never drift. Each column: headerLines (multi-line headers
// render with <br>), optional cell class, and value(row, pricing).
// Relative import (not $lib) so node:test can resolve this module too.
import { formatPrice, formatPriceFine } from "../../lib/utils/format.js";
import * as m from "../../lib/paraglide/messages.js";

const modes = {
  headerLines: [m.models_column_modes()],
  value: (row) => (row.model?.metadata?.modes ?? []).join(", ") || "-",
};

function price(headerLines, value) {
  return { headerLines, class: "col-price", value };
}

const inputOutput = price(
  [m.models_column_input_output()],
  (row, p) => formatPrice(p?.input_per_mtok) + " / " + formatPrice(p?.output_per_mtok),
);

const CATEGORY_COLUMNS = {
  all: [inputOutput],
  text_generation: [
    modes,
    inputOutput,
    price([m.models_column_cached()], (row, p) => formatPrice(p?.cached_input_per_mtok)),
  ],
  embedding: [price([m.models_column_input(), "$/MTok"], (row, p) => formatPrice(p?.input_per_mtok))],
  image: [price([m.models_column_per_image()], (row, p) => formatPriceFine(p?.per_image))],
  audio: [
    price([m.models_column_per_second()], (row, p) => formatPriceFine(p?.per_second_input)),
    price([m.models_column_per_character()], (row, p) => formatPriceFine(p?.per_character_input)),
  ],
  video: [
    price([m.models_column_per_second_in()], (row, p) => formatPriceFine(p?.per_second_input)),
    price([m.models_column_per_second_out()], (row, p) => formatPriceFine(p?.per_second_output)),
  ],
  utility: [
    price([m.models_column_per_page()], (row, p) => formatPriceFine(p?.per_page)),
    price([m.models_column_per_request()], (row, p) => formatPriceFine(p?.per_request)),
  ],
};

export function categoryColumns(category) {
  return CATEGORY_COLUMNS[category] || CATEGORY_COLUMNS.all;
}

// Full table width: the Model column + category columns + the actions column.
export function categoryColspan(category) {
  return categoryColumns(category).length + 2;
}
