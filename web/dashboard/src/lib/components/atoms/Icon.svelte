<script>
  // Lucide icon by kebab-case name. Renders the SVG inline so CSS classes
  // style it directly.
  import { icons } from "lucide";

  let { name = "", class: className = "", ...rest } = $props();

  function pascalCase(kebab) {
    return String(kebab || "")
      .split("-")
      .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
      .join("");
  }

  function attrsToString(attrs) {
    return Object.entries(attrs)
      .map(([key, value]) => `${key}="${String(value)}"`)
      .join(" ");
  }

  function renderNode([tag, attrs, children]) {
    const inner = Array.isArray(children)
      ? children.map(renderNode).join("")
      : "";
    return `<${tag} ${attrsToString(attrs || {})}>${inner}</${tag}>`;
  }

  const svg = $derived.by(() => {
    const icon = icons[pascalCase(name)];
    if (!icon) return "";
    return icon.map(renderNode).join("");
  });
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
  <!-- eslint-disable-next-line svelte/no-at-html-tags -- trusted static icon data -->
  {@html svg}
</svg>
