// Motion helpers for fast UI transitions, honoring prefers-reduced-motion.
import { slide } from "svelte/transition";

// The media query list is created once: motionDuration is called per row per
// render, and matchMedia() allocates a fresh MediaQueryList every call.
// `false` marks "no matchMedia here" (SSR / test environments).
let reducedMotionQuery = null;

function prefersReducedMotion() {
  if (reducedMotionQuery === null) {
    reducedMotionQuery =
      typeof window !== "undefined" && typeof window.matchMedia === "function"
        ? window.matchMedia("(prefers-reduced-motion: reduce)")
        : false;
  }
  return !!(reducedMotionQuery && reducedMotionQuery.matches);
}

export function motionDuration(ms) {
  return prefersReducedMotion() ? 0 : ms;
}

// liveSlide is `slide` for live-inserted rows and a no-op for everything else.
// A zero-duration `slide` still builds its keyframes, and building them costs a
// getComputedStyle() per node — a forced style recalc for every row of a freshly
// fetched page. Returning an empty config skips that work entirely.
export function liveSlide(node, params) {
  if (!params || !params.live) return { duration: 0 };
  return slide(node, { duration: motionDuration(150) });
}
