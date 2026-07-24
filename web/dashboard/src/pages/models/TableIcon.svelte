<script>
  // Table action icons (x, trash, edit, dollar, gauge, failover), emitted
  // with the .table-icon-svg class so the existing styles keep applying.
  let { name = "" } = $props();
</script>

{#if name === "x"}
  <svg class="table-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path d="M18 6L6 18"></path>
    <path d="M6 6l12 12"></path>
  </svg>
{:else if name === "trash"}
  <svg class="table-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path d="M3 6h18"></path>
    <path d="M8 6V4h8v2"></path>
    <path d="M19 6l-1 14H6L5 6"></path>
    <path d="M10 11v6"></path>
    <path d="M14 11v6"></path>
  </svg>
{:else if name === "edit"}
  <svg class="table-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path d="M12 20h9"></path>
    <path d="M16.5 3.5a2.12 2.12 0 1 1 3 3L7 19l-4 1 1-4 12.5-12.5z"></path>
  </svg>
{:else if name === "dollar"}
  <svg class="table-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <circle cx="12" cy="12" r="10"></circle>
    <path d="M16 8h-6a2 2 0 1 0 0 4h4a2 2 0 1 1 0 4H8"></path>
    <path d="M12 18V6"></path>
  </svg>
{:else if name === "gauge"}
  <svg class="table-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path d="m12 14 4-4"></path>
    <path d="M3.34 19a10 10 0 1 1 17.32 0"></path>
  </svg>
{:else if name === "failover"}
  <svg class="table-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path d="m18 14 4 4-4 4"></path>
    <path d="m18 2 4 4-4 4"></path>
    <path d="M2 18h1.973a4 4 0 0 0 3.3-1.7l5.454-8.6a4 4 0 0 1 3.3-1.7H22"></path>
    <path d="M2 6h1.972a4 4 0 0 1 3.6 2.2"></path>
    <path d="M22 18h-6.041a4 4 0 0 1-3.3-1.8l-.359-.45"></path>
  </svg>
{/if}
