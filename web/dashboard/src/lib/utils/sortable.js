// Drag-to-reorder for vertical lists, as a Svelte attachment plus the pure
// index math behind it (tested in tests/sortable.test.js — keep this module
// free of Svelte and $lib imports).
//
//   <ul {@attach sortableList({ onreorder: (from, to) => ... })}>
//     <li data-sortable-item>
//       <button data-sortable-handle>⋮⋮</button> …
//     </li>
//   </ul>
//
// Pointer events (mouse, touch, pen) drive the drag: the grabbed item follows
// the pointer, its siblings slide out of the way, and releasing reports the
// move as (fromIndex, toIndex) so the owner reorders its state. Escape cancels.
// Pair it with keyboard handling on the handle (ArrowUp/ArrowDown) for
// accessibility — see PlaygroundMessage.

/**
 * Return a copy of `items` with the element at `from` moved to `to`.
 * Out-of-range or identical indexes return the input unchanged.
 */
export function moveItem(items, from, to) {
  const list = Array.isArray(items) ? items : [];
  if (from === to || from < 0 || to < 0 || from >= list.length || to >= list.length) return list;
  const next = list.slice();
  const [moved] = next.splice(from, 1);
  next.splice(to, 0, moved);
  return next;
}

/**
 * Index the dragged item would land on, given the pointer's Y position and
 * the vertical midpoints of the items in their resting order. The item drops
 * before the first midpoint below the pointer, so dragging downward past an
 * item's centre swaps with it.
 *
 * @param {number} from index of the dragged item
 * @param {number} pointerY
 * @param {number[]} midpoints resting-position midpoints, one per item
 */
export function sortableTargetIndex(from, pointerY, midpoints) {
  const count = midpoints.length;
  if (count === 0) return -1;
  let target = from;
  if (pointerY < midpoints[from]) {
    target = from;
    for (let i = from - 1; i >= 0; i--) {
      if (pointerY < midpoints[i]) target = i;
      else break;
    }
  } else {
    for (let i = from + 1; i < count; i++) {
      if (pointerY > midpoints[i]) target = i;
      else break;
    }
  }
  return target;
}

/**
 * Vertical offset each sibling needs while the dragged item hovers over
 * `to`: items between the two positions shift by the dragged item's height.
 */
export function sortableShift(index, from, to, draggedHeight) {
  if (index === from || from === to) return 0;
  if (from < to && index > from && index <= to) return -draggedHeight;
  if (from > to && index >= to && index < from) return draggedHeight;
  return 0;
}

/**
 * @param {{
 *   onreorder: (from: number, to: number) => void,
 *   handleSelector?: string,
 *   itemSelector?: string,
 * }} options
 */
export function sortableList({
  onreorder,
  handleSelector = "[data-sortable-handle]",
  itemSelector = "[data-sortable-item]",
}) {
  return (/** @type {HTMLElement} */ container) => {
    /** @type {null | {
     *   pointerId: number, handle: HTMLElement, items: HTMLElement[],
     *   from: number, to: number, startY: number, grabOffset: number,
     *   midpoints: number[], height: number,
     * }} */
    let drag = null;

    const items = () => /** @type {HTMLElement[]} */ ([...container.querySelectorAll(itemSelector)])
      .filter((item) => item.parentElement === container);

    function cleanup(cancelled) {
      if (!drag) return;
      const { handle, pointerId, items: list, from, to } = drag;
      drag = null;
      for (const item of list) {
        item.style.transform = "";
        item.style.transition = "";
        item.style.zIndex = "";
        item.classList.remove("is-dragging");
      }
      container.classList.remove("is-sorting");
      document.body.classList.remove("sortable-dragging");
      try {
        handle.releasePointerCapture(pointerId);
      } catch {
        // Capture may already be gone.
      }
      if (!cancelled && from !== to) onreorder(from, to);
    }

    function onPointerDown(/** @type {PointerEvent} */ event) {
      if (drag || event.button !== 0) return;
      const handle = /** @type {HTMLElement | null} */ (
        /** @type {Element} */ (event.target).closest(handleSelector)
      );
      if (!handle || handle.hasAttribute("disabled") || handle.getAttribute("aria-disabled") === "true") return;
      const item = /** @type {HTMLElement | null} */ (handle.closest(itemSelector));
      const list = items();
      const from = item ? list.indexOf(item) : -1;
      if (from < 0 || list.length < 2) return;
      event.preventDefault();
      const midpoints = list.map((el) => {
        const rect = el.getBoundingClientRect();
        return rect.top + rect.height / 2;
      });
      drag = {
        pointerId: event.pointerId,
        handle,
        items: list,
        from,
        to: from,
        startY: event.clientY,
        // The handle usually sits at the card's top edge; probing with the
        // card's centre rather than the pointer makes the swap happen when
        // the moving card itself passes its neighbour.
        grabOffset: midpoints[from] - event.clientY,
        midpoints,
        height: list[from].getBoundingClientRect().height + gapBetween(list),
      };
      handle.setPointerCapture(event.pointerId);
      list[from].classList.add("is-dragging");
      list[from].style.zIndex = "2";
      for (const el of list) {
        if (el !== list[from]) el.style.transition = "transform 0.15s ease";
      }
      container.classList.add("is-sorting");
      document.body.classList.add("sortable-dragging");
    }

    function gapBetween(list) {
      if (list.length < 2) return 0;
      const a = list[0].getBoundingClientRect();
      const b = list[1].getBoundingClientRect();
      return Math.max(0, b.top - a.bottom);
    }

    function onPointerMove(/** @type {PointerEvent} */ event) {
      if (!drag || event.pointerId !== drag.pointerId) return;
      const { items: list, from, midpoints, height, startY, grabOffset } = drag;
      const dy = event.clientY - startY;
      drag.to = sortableTargetIndex(from, event.clientY + grabOffset, midpoints);
      list[from].style.transform = `translateY(${dy}px)`;
      list.forEach((el, index) => {
        if (index === from) return;
        const shift = sortableShift(index, from, drag.to, height);
        el.style.transform = shift ? `translateY(${shift}px)` : "";
      });
    }

    function onPointerUp(/** @type {PointerEvent} */ event) {
      if (!drag || event.pointerId !== drag.pointerId) return;
      cleanup(false);
    }

    function onPointerCancel(/** @type {PointerEvent} */ event) {
      if (!drag || event.pointerId !== drag.pointerId) return;
      cleanup(true);
    }

    function onKeydown(/** @type {KeyboardEvent} */ event) {
      if (drag && event.key === "Escape") cleanup(true);
    }

    container.addEventListener("pointerdown", onPointerDown);
    container.addEventListener("pointermove", onPointerMove);
    container.addEventListener("pointerup", onPointerUp);
    container.addEventListener("pointercancel", onPointerCancel);
    window.addEventListener("keydown", onKeydown);
    return () => {
      cleanup(true);
      container.removeEventListener("pointerdown", onPointerDown);
      container.removeEventListener("pointermove", onPointerMove);
      container.removeEventListener("pointerup", onPointerUp);
      container.removeEventListener("pointercancel", onPointerCancel);
      window.removeEventListener("keydown", onKeydown);
    };
  };
}
