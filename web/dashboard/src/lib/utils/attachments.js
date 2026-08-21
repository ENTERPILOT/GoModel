// Reusable Svelte attachments (`{@attach ...}`), for the small DOM behaviours
// the dashboard repeats. An attachment is a function that receives the element
// it is placed on and may return a teardown function; it runs in an effect, so
// reactive reads inside it are tracked and teardown runs when the element goes
// away. Prefer these over `bind:this` + `$effect` — there is no element ref to
// hold and no null guard to write.
//
// Attach a falsy value to disable one: `{@attach open ? dismissOnOutside(close) : undefined}`.

/**
 * Dismiss a popup when a click lands outside the element or Escape is pressed.
 * Place it on the popup's outermost element (the one that also contains its
 * trigger, so clicking the trigger stays an inside click and the trigger's own
 * toggle keeps working).
 *
 * @param {() => void} ondismiss
 */
export function dismissOnOutside(ondismiss) {
  return (/** @type {Element} */ node) => {
    const onDocClick = (/** @type {MouseEvent} */ event) => {
      if (!node.contains(/** @type {Node} */ (event.target))) ondismiss();
    };
    const onKeydown = (/** @type {KeyboardEvent} */ event) => {
      if (event.key === "Escape") ondismiss();
    };
    // Capture phase: see the click even when something inside the page stops
    // it from bubbling.
    document.addEventListener("click", onDocClick, true);
    window.addEventListener("keydown", onKeydown);
    return () => {
      document.removeEventListener("click", onDocClick, true);
      window.removeEventListener("keydown", onKeydown);
    };
  };
}

/**
 * Focus the first descendant matching `selector` once the element is mounted.
 *
 * @param {string} [selector]
 */
export function autofocusWithin(selector = "[data-modal-autofocus]") {
  return (/** @type {Element} */ node) => {
    const target = /** @type {HTMLElement | null} */ (
      node.querySelector(selector)
    );
    if (target && typeof target.focus === "function") target.focus();
  };
}

/**
 * Seconds a marquee pass should take for `overflowPx` of hidden text at
 * `pxPerSecond`, never quicker than a readable minimum.
 */
export function marqueeDuration(overflowPx, pxPerSecond = 60) {
  const overflow = Math.max(0, Number(overflowPx) || 0);
  const speed = Math.max(1, Number(pxPerSecond) || 60);
  return Math.max(1.5, overflow / speed);
}

/**
 * Scroll clipped text into view while the pointer hovers it. Place it on the
 * clipping element (overflow hidden, nowrap) whose first element child holds
 * the text; the attachment measures the overflow on hover and, only when
 * something is hidden, adds `marquee-active` and sets `--marquee-shift`
 * (negative px) and `--marquee-duration` for the owner's CSS animation to
 * translate the child left to the far end and back.
 *
 * @param {number} [pxPerSecond]
 */
export function marqueeOnOverflow(pxPerSecond = 60) {
  return (/** @type {HTMLElement} */ node) => {
    const start = () => {
      if (!node.firstElementChild) return;
      // The clipping element's scrollWidth spans its full text even when an
      // ellipsis is painted; an inline child reports scrollWidth 0, so never
      // measure the child.
      const overflow = node.scrollWidth - node.clientWidth;
      if (overflow <= 1) return;
      node.style.setProperty("--marquee-shift", `${-overflow}px`);
      node.style.setProperty("--marquee-duration", `${marqueeDuration(overflow, pxPerSecond)}s`);
      node.classList.add("marquee-active");
    };
    const stop = () => {
      node.classList.remove("marquee-active");
      node.style.removeProperty("--marquee-shift");
      node.style.removeProperty("--marquee-duration");
    };
    node.addEventListener("mouseenter", start);
    node.addEventListener("mouseleave", stop);
    return () => {
      stop();
      node.removeEventListener("mouseenter", start);
      node.removeEventListener("mouseleave", stop);
    };
  };
}
