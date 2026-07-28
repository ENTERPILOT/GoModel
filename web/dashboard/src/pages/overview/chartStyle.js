// Overview-specific chart styling. The cross-page fragments (tick font,
// tooltip, categorical palette, CSS color resolution) live in
// ../../lib/utils/chartTheme.ts.

import { formatTokensShort } from "../../lib/utils/format.ts";
import { chartTickFont } from "../../lib/utils/chartTheme.ts";

// Y-axis ticks that abbreviate token counts (e.g. 1.2K, 3.4M).
export function tokenAxisTicks(colors) {
  return {
    color: colors.text,
    font: chartTickFont(),
    callback: (v) => formatTokensShort(v),
  };
}
