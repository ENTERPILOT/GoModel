<script>
  // Renders text with the characters that matched a search query marked, so a
  // row that shows up for a non-contiguous match (`dep1017` reaching
  // `deepseek-v4.1-flash:1017`) explains itself. The split comes from the same
  // matcher the filters use, so a highlighted row is always a row whose match
  // the filter accepted, and the other way round.
  //
  // Props:
  //   text    the plain text to render
  //   needle  the active query; empty renders the text untouched
  //   class   extra classes on the wrapper (the wrapper is a single element so
  //           a marquee or a clip around it still has one child to measure)
  import { matchSegments } from "$lib/utils/textMatch.js";

  let { text = "", needle = "", class: className = "" } = $props();

  const segments = $derived(matchSegments(text, needle));
</script>

<span class={["text-match", className]}>
  {#each segments as segment, index (index)}
    {#if segment.hit}<mark class="text-match-hit">{segment.text}</mark>{:else}{segment.text}{/if}
  {/each}
</span>

<style>
  /* The hit only shifts the tone of the text it covers, so a cell keeps its
     own colour and weight and the highlight reads the same in both themes.
     A <mark> defaults to yellow on black, which would ignore the palette;
     overriding both properties keeps it on the theme's accent. */
  .text-match-hit {
    background: color-mix(in srgb, var(--accent) 34%, transparent);
    color: var(--text);
    border-radius: 2px;
    padding: 0 1px;
    margin: 0 -1px;
  }
</style>
