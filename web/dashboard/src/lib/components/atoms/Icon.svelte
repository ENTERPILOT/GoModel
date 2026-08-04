<script>
  // Lucide icon by kebab-case name. Renders the SVG inline so CSS classes
  // style it directly. Icons come from the curated registry in icons.js —
  // add new names there.
  import { iconRegistry } from "./icons.js";

  let { name = "", class: className = "", ...rest } = $props();

  const nodes = $derived(iconRegistry[name] || []);
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
  {#snippet iconNodes(children)}
    {#each children as [tag, attrs, kids], i (i)}
      <!-- xmlns tells the compiler to create these in the SVG namespace;
           snippet bodies don't inherit it from the surrounding <svg>. -->
      <svelte:element this={tag} xmlns="http://www.w3.org/2000/svg" {...attrs}>
        {#if Array.isArray(kids)}{@render iconNodes(kids)}{/if}
      </svelte:element>
    {/each}
  {/snippet}
  {@render iconNodes(nodes)}
</svg>
