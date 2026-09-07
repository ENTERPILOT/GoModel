<script>
  // Loaded plugins section at the bottom of the Plugins & Guardrails page
  // (GET /admin/plugins): every plugin type with its version, hooks, source,
  // and health; a plugin that failed to load shows its error. Guardrails
  // carry a shield and are listed first. Always expanded; hidden entirely
  // when the endpoint is unavailable (404/503), which the page-level
  // guardrails alert already explains.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import { ShieldCheck } from "lucide";
  import { pluginsStore } from "$lib/stores/plugins.svelte.js";
  import { phaseLabel } from "$lib/utils/pluginPhases.js";
  import { pluginHealthy, pluginSourceIsBuiltin } from "$lib/utils/plugins.js";
  import { formatNumber } from "$lib/utils/format.js";
  import * as m from "$lib/paraglide/messages.js";
</script>

{#if pluginsStore.available}
<section class="settings-panel plugins-panel">
  <div class="editor-header">
    <h3 class="plugins-title">
      {m.plugins_title()}
      {#if pluginsStore.loaded}
        <span class="provider-badge">{formatNumber(pluginsStore.plugins.length)}</span>
      {/if}
    </h3>
  </div>
  <p class="form-hint">{m.plugins_help()}</p>

  {#if pluginsStore.error}
    <p class="form-error" role="alert">{pluginsStore.error}</p>
  {:else if !pluginsStore.loaded}
    <LoadingState label={m.plugins_loading()} />
  {:else if pluginsStore.plugins.length === 0}
    <p class="empty-state">{m.plugins_empty()}</p>
  {:else}
  <div class="table-wrapper">
  <table class="data-table plugins-table">
    <thead>
      <tr>
        <th>{m.plugins_name()}</th>
        <th>{m.plugins_version()}</th>
        <th>{m.plugins_kinds()}</th>
        <th>{m.plugins_source()}</th>
        <th>{m.plugins_health()}</th>
      </tr>
    </thead>
    <tbody>
      {#each pluginsStore.plugins as plugin (plugin.name)}
        <tr>
          <td>
            <div class="plugin-name mono font-size-md">
              {#if plugin.guardrail}
                <span class="plugin-guardrail" role="img" title={m.plugins_guardrail()} aria-label={m.plugins_guardrail()}>
                  <Icon icon={ShieldCheck} class="form-action-icon" />
                </span>
              {/if}
              {plugin.name}
            </div>
            {#if plugin.description}
              <div class="plugin-description">{plugin.description}</div>
            {/if}
          </td>
          <td class="mono font-size-md">{plugin.version || "—"}</td>
          <td>
            <div class="plugin-kinds">
              {#each plugin.kinds as kind (kind)}
                <span class="plugin-kind">{phaseLabel(kind)}</span>
              {/each}
            </div>
          </td>
          <td class="mono font-size-md plugin-source">
            {pluginSourceIsBuiltin(plugin) ? m.plugins_source_builtin() : plugin.source || "—"}
          </td>
          <td>
            {#if pluginHealthy(plugin)}
              <span class="plugin-health is-ok">{m.plugins_health_ok()}</span>
            {:else}
              <span class="plugin-health is-error">{m.plugins_health_error()}</span>
              {#if plugin.error}
                <div class="plugin-error">{plugin.error}</div>
              {/if}
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
  </div>
  {/if}
</section>
{/if}

<style>
  .plugins-panel {
    min-width: 0;
    margin-top: 20px;
  }

  .plugins-title {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    margin: 0;
  }

  .plugin-name {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

  .plugin-guardrail {
    display: inline-flex;
    color: var(--accent);
  }

  .plugin-description {
    margin-top: 4px;
    color: var(--text-muted);
    font-size: 12px;
  }

  .plugin-kinds {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }

  .plugin-kind {
    display: inline-flex;
    align-items: center;
    padding: 2px 7px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    color: var(--text-muted);
    font-size: 9px;
    font-weight: 800;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    white-space: nowrap;
    line-height: 1.5;
  }

  .plugin-source {
    word-break: break-all;
  }

  .plugin-health {
    font-size: 12px;
    font-weight: 600;
  }

  .plugin-health.is-ok {
    color: var(--success);
  }

  .plugin-health.is-error {
    color: var(--danger);
  }

  .plugin-error {
    margin-top: 4px;
    color: var(--danger);
    font-size: 12px;
    word-break: break-word;
  }
</style>
