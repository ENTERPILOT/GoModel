<script>
  // Timezone override selector bound to the shared timezone store. Saving or
  // clearing the override triggers a global data refresh so every page
  // refetches in the new effective timezone.
  import { auth } from "$lib/stores/auth.svelte.js";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";

  function save() {
    timezone.saveOverride();
    auth.refresh();
  }

  function clear() {
    timezone.clearOverride();
    auth.refresh();
  }
</script>

<div class="settings-panel-header">
  <InlineHelpSection
    copyId="timezone-help-copy"
    label="timezone help"
    text="Day-based analytics, charts, and date filters use your effective timezone. Usage and audit logs keep UTC in the hover title while rendering row timestamps in your effective timezone."
  >
    {#snippet title()}<h3>Timezone</h3>{/snippet}
  </InlineHelpSection>
</div>

<div class="settings-form-grid">
  <div class="form-field">
    <label class="form-field-label" for="timezone-override-select"
      >Timezone Override</label
    >
    <select
      id="timezone-override-select"
      class="form-select settings-select"
      bind:value={timezone.override}
      aria-describedby="timezone-help-copy"
      onfocus={() => timezone.ensureOptions()}
      onchange={save}
    >
      <option value="">Automatic ({timezone.detectedTimeZoneLabel()})</option>
      {#each timezone.options as timeZone (timeZone.value)}
        <option value={timeZone.value}>{timeZone.label}</option>
      {/each}
    </select>
  </div>
</div>
<div class="form-actions">
  {#if timezone.override}
    <button type="button" class="btn" onclick={clear}
      >Use Browser Timezone</button
    >
  {/if}
</div>

<style>
  .settings-panel-header {
    display: flex;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 20px;
  }

  /* margin-bottom dropped: the heading sits in an inline-help-title-row,
     whose global margin-bottom: 0 always outranked the 6px declared here. */
  .settings-panel-header :global(h3) {
    font-size: 18px;
  }

  .settings-form-grid {
    display: grid;
    grid-template-columns: minmax(280px, 420px);
    gap: 16px;
  }

  @media (max-width: 768px) {
    .settings-form-grid {
        grid-template-columns: 1fr;
      }
  }
</style>
