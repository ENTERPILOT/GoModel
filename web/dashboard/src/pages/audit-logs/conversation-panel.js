export const DEFAULT_CONVERSATION_PANEL_WIDTH = 520;

export function conversationPanelBounds(viewportWidth, leadingWidth = 0) {
  const available = Math.max(0, finite(viewportWidth) - Math.max(0, finite(leadingWidth)));
  if (available >= 680) {
    return { min: 320, max: Math.max(320, available - 360) };
  }

  // Compact screens still share the shell instead of covering it. Keep a
  // usable strip of main content visible and allow a smaller panel only when
  // the desktop minimum cannot fit.
  const min = Math.min(320, Math.max(180, Math.floor(available * 0.58)));
  return { min, max: Math.max(min, available - 128) };
}

export function clampConversationPanelWidth(width, viewportWidth, leadingWidth = 0) {
  const bounds = conversationPanelBounds(viewportWidth, leadingWidth);
  const requested = finite(width) || DEFAULT_CONVERSATION_PANEL_WIDTH;
  return Math.min(bounds.max, Math.max(bounds.min, Math.round(requested)));
}

export function conversationPanelWidthFromPointer(clientX, viewportWidth, leadingWidth = 0) {
  return clampConversationPanelWidth(
    finite(viewportWidth) - finite(clientX),
    viewportWidth,
    leadingWidth,
  );
}

function finite(value) {
  const number = Number(value);
  return Number.isFinite(number) ? number : 0;
}
