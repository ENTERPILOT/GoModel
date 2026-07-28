// Duration for fast UI transitions, honoring prefers-reduced-motion.
export function motionDuration(ms: number): number {
  if (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  ) {
    return 0;
  }
  return ms;
}
