<script>
  // Renders a lucide icon inline as SVG so CSS classes style it directly.
  // `icon` is the icon itself, imported from "lucide" by the caller
  // (`import { Pencil } from "lucide"` → `<Icon icon={Pencil} />`), not a
  // name string: an unknown icon is then a build error rather than a
  // silently blank SVG. Lucide icons are plain [tag, attrs, children?]
  // arrays, so they are safe to pass around and store as data.
  let { icon = [], class: className = "", ...rest } = $props();
</script>

<svg
  xmlns="http://www.w3.org/2000/svg"
  width="24"
  height="24"
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width="2"
  stroke-linecap="round"
  stroke-linejoin="round"
  class={className}
  aria-hidden="true"
  focusable="false"
  {...rest}
>
  {#snippet iconNodes(nodes)}
    {#each nodes as [tag, attrs, children], i (i)}
      <!-- xmlns tells the compiler to create these in the SVG namespace;
           snippet bodies don't inherit it from the surrounding <svg>. -->
      <svelte:element this={tag} xmlns="http://www.w3.org/2000/svg" {...attrs}>
        {#if Array.isArray(children)}{@render iconNodes(children)}{/if}
      </svelte:element>
    {/each}
  {/snippet}
  {@render iconNodes(icon)}
</svg>
