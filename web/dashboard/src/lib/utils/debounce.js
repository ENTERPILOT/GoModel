// Trailing-edge debounce for search inputs: the callback runs once the user
// stops typing for `delay` ms. Call `.cancel()` from a teardown so a pending
// fetch cannot fire after the component is gone.

export function debounced(fn, delay = 300) {
  let timer = null;
  const run = (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      fn(...args);
    }, delay);
  };
  run.cancel = () => {
    clearTimeout(timer);
    timer = null;
  };
  return run;
}
