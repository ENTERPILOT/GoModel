// Trailing-edge debounce for search inputs: the callback runs once the user
// stops typing for `delay` ms. Call `.cancel()` from a teardown so a pending
// fetch cannot fire after the component is gone.

export interface DebouncedFn<A extends unknown[]> {
  (...args: A): void;
  cancel(): void;
}

export function debounced<A extends unknown[]>(
  fn: (...args: A) => void,
  delay = 300,
): DebouncedFn<A> {
  let timer: ReturnType<typeof setTimeout> | null = null;
  const run = ((...args: A) => {
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      fn(...args);
    }, delay);
  }) as DebouncedFn<A>;
  run.cancel = () => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
  };
  return run;
}
