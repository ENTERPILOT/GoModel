<script>
  // Chart.js wrapper. Rebuilds the chart when the config or theme changes —
  // theme rebuilds are required because colors are read from CSS variables at
  // build time. Pass a `build()` function returning a Chart.js config (type,
  // data, options, plugins); it runs inside the canvas attachment, so reactive
  // reads are tracked automatically and the chart is destroyed (and rebuilt)
  // with the attachment.
  import Chart from "chart.js/auto";
  import { themeStore } from "$lib/stores/ui.svelte.js";

  let { build, class: className = "", ariaLabel = "" } = $props();

  function chart(canvas) {
    // Track theme changes so charts pick up the new palette.
    void themeStore.tick;
    if (typeof build !== "function") return;
    const config = build();
    if (!config) return;
    const instance = new Chart(canvas.getContext("2d"), config);
    return () => instance.destroy();
  }
</script>

<canvas class={className} aria-label={ariaLabel} {@attach chart}></canvas>
