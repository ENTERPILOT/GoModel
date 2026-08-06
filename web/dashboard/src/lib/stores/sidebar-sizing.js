export const MIN_SIDEBAR_WIDTH = 60;
export const MAX_SIDEBAR_WIDTH = 240;

export function clampSidebarWidth(value) {
  const width = Number(value);
  if (!Number.isFinite(width)) return MAX_SIDEBAR_WIDTH;
  return Math.min(MAX_SIDEBAR_WIDTH, Math.max(MIN_SIDEBAR_WIDTH, Math.round(width)));
}

export function sidebarWidthFromPointer(startWidth, startX, clientX) {
  return clampSidebarWidth(Number(startWidth) + Number(clientX) - Number(startX));
}
