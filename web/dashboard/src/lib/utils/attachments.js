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
