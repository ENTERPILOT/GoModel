<script>
  // The shared "no data" artwork: the GoModel hexagon mark around three
  // outlined placeholder bars. `label` is baked into the drawing, so hide it when the surrounding
  // empty state already spells the message out in text.
  let { label = "No data" } = $props();
</script>

<svg
  class="empty-state-icon"
  viewBox="0 0 220 258"
  fill="none"
  role={label ? "img" : undefined}
  aria-label={label || undefined}
  aria-hidden={label ? undefined : "true"}
  xmlns="http://www.w3.org/2000/svg"
>
  <!-- GoModel hexagon mark: sharp mitered edges like the logotype -->
  <path
    d="M110 20 L187.9 65 L187.9 155 L110 200 L32.1 155 L32.1 65 Z"
    stroke="var(--accent)"
    stroke-width="7"
    stroke-linejoin="miter"
    opacity="0.5"
  ></path>
  <!-- empty chart baseline (the floor) -->
  <path d="M64 153 H156" stroke="currentColor" stroke-width="6" stroke-linecap="round" opacity="0.5"></path>
  <!-- three outlined placeholder bars, floating clear of the floor and of the hexagon -->
  <rect x="68.5" y="103.5" width="17" height="35" rx="5" stroke="currentColor" stroke-width="5" opacity="0.5"></rect>
  <rect x="101.5" y="76.5" width="17" height="62" rx="5" stroke="currentColor" stroke-width="5" opacity="0.5"></rect>
  <rect x="134.5" y="92.5" width="17" height="46" rx="5" stroke="currentColor" stroke-width="5" opacity="0.5"></rect>
  {#if label}
    <text x="110" y="246" text-anchor="middle" fill="currentColor" font-size="28">{label}</text>
  {/if}
</svg>
