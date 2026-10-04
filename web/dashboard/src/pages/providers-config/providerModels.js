// Pure helpers for the provider editor's custom models control, which edits a
// credential's `models` array either one model per row or as free text for
// pasting a long list. An empty list means "auto-discover", so blank rows and
// blank lines simply drop out rather than being errors.

// parseModelList splits text on commas and newlines, trims each entry, and
// drops blanks and repeats, keeping the first occurrence's position.
export function parseModelList(text) {
  const seen = new Set();
  const models = [];
  for (const item of String(text || "").split(/[,\n]/)) {
    const model = item.trim();
    if (model && !seen.has(model)) {
      seen.add(model);
      models.push(model);
    }
  }
  return models;
}

// modelsToRows converts a stored models array into editable {value} rows.
export function modelsToRows(models) {
  return (Array.isArray(models) ? models : []).map((value) => ({
    value: String(value || ""),
  }));
}

// modelRowsToList flattens editor rows into the wire array. A row holding a
// pasted "a, b" splits like the text view would, so both views agree.
export function modelRowsToList(rows) {
  return parseModelList(
    (Array.isArray(rows) ? rows : []).map((row) => String((row && row.value) || "")).join("\n"),
  );
}

// modelRowsToText renders rows for the text view, one model per line.
export function modelRowsToText(rows) {
  return modelRowsToList(rows).join("\n");
}

// modelsTextToRows parses the text view back into rows.
export function modelsTextToRows(text) {
  return modelsToRows(parseModelList(text));
}
