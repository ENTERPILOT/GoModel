<script>
  // Chart.js wrapper. Rebuilds the chart when the config or theme changes —
  // theme rebuilds are required because colors are read from CSS variables at
  // build time. Pass a `build()` function returning a Chart.js config (type,
  // data, options, plugins); it runs inside an effect so reactive reads are
  // tracked automatically.
  import Chart from "chart.js/auto";
  import { themeStore } from "$lib/stores/ui.svelte.js";

  let { build, class: className = "", ariaLabel = "" } = $props();

  let canvas = $state(null);
  let chart = null;

  $effect(() => {
    // Track theme changes so charts pick up the new palette.
    void themeStore.tick;
    if (!canvas || typeof build !== "function") return;
    const config = build();
    if (!config) {
      if (chart) {
        chart.destroy();
        chart = null;
      }
      return;
    }
    if (chart) {
      chart.destroy();
      chart = null;
    }
    chart = new Chart(canvas.getContext("2d"), config);
    return () => {
      if (chart) {
        chart.destroy();
        chart = null;
      }
    };
  });
</script>

<canvas bind:this={canvas} class={className} aria-label={ariaLabel}></canvas>
