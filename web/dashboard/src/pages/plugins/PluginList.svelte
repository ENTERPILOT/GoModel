<script>
  // Loaded plugins table (GET /admin/plugins): every plugin type with its
  // version, hooks, source, and health; a plugin that failed to load shows
  // its error.
  import { pluginsStore } from "$lib/stores/plugins.svelte.js";
  import { phaseLabel } from "$lib/utils/pluginPhases.js";
  import { pluginHealthy, pluginSourceIsBuiltin } from "$lib/utils/plugins.js";
  import * as m from "$lib/paraglide/messages.js";
</script>

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
            <div class="mono font-size-md">{plugin.name}</div>
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

<style>
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
