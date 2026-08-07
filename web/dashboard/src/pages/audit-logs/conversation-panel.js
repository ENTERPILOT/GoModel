export const DEFAULT_CONVERSATION_PANEL_WIDTH = 520;

export function conversationPanelBounds(viewportWidth, leadingWidth = 0) {
  const available = Math.max(0, finite(viewportWidth) - Math.max(0, finite(leadingWidth)));
  // Grow the content reserve continuously from a compact 128px strip to the
  // desktop target of 360px. This keeps both panes usable without a breakpoint
  // that can make the panel jump when the viewport changes by a single pixel.
  const min = Math.min(320, Math.max(180, Math.floor(available * 0.58)));
  const contentReserve = Math.min(360, Math.max(128, Math.floor(available * 0.35)));
  return { min, max: Math.max(min, available - contentReserve) };
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

export function conversationMessageNavigationIndex(messageTops, viewportTop, direction) {
  if (!Array.isArray(messageTops) || messageTops.length === 0) return -1;
  const top = finite(viewportTop);
  let current = 0;
  messageTops.forEach((messageTop, index) => {
    if (finite(messageTop) <= top + 1) current = index;
  });
  const step = Number(direction) < 0 ? -1 : 1;
  return Math.min(messageTops.length - 1, Math.max(0, current + step));
}

function finite(value) {
  const number = Number(value);
  return Number.isFinite(number) ? number : 0;
}
