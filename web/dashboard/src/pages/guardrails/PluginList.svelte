<script>
  // Loaded plugins panel (GET /admin/plugins): collapsed by default, hidden
  // entirely when the gateway has no plugins or predates the endpoint.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { pluginsStore } from "$lib/stores/plugins.svelte.js";
  import { phaseLabel } from "$lib/utils/pluginPhases.js";
  import { pluginHealthy, pluginSourceIsBuiltin } from "$lib/utils/plugins.js";
  import { ChevronDown, ChevronRight } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  let open = $state(false);
</script>

{#if pluginsStore.plugins.length > 0}
  <section class="settings-panel settings-plugins">
    <div class="editor-header">
      <button
        type="button"
        class="settings-plugins-toggle"
        aria-expanded={open}
        aria-controls="settings-plugins-body"
        aria-label={open ? m.plugins_hide() : m.plugins_show()}
        onclick={() => (open = !open)}
      >
        <Icon icon={open ? ChevronDown : ChevronRight} class="form-action-icon" />
        <h3>{m.plugins_title()}</h3>
        <span class="provider-badge"
          >{m.plugins_count({ count: pluginsStore.plugins.length })}</span
        >
      </button>
    </div>
    <p class="form-hint">{m.plugins_help()}</p>

    {#if open}
      <div class="table-wrapper" id="settings-plugins-body">
        <table class="data-table settings-plugins-table">
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
                    <div class="settings-plugin-description">{plugin.description}</div>
                  {/if}
                </td>
                <td class="mono font-size-md">{plugin.version || "—"}</td>
                <td>
                  <div class="settings-plugin-kinds">
                    {#each plugin.kinds as kind (kind)}
                      <span class="settings-plugin-kind">{phaseLabel(kind)}</span>
                    {/each}
                  </div>
                </td>
                <td class="mono font-size-md settings-plugin-source">
                  {pluginSourceIsBuiltin(plugin) ? m.plugins_source_builtin() : plugin.source || "—"}
                </td>
                <td>
                  {#if pluginHealthy(plugin)}
                    <span class="settings-plugin-health is-ok">{m.plugins_health_ok()}</span>
                  {:else}
                    <span class="settings-plugin-health is-error">{m.plugins_health_error()}</span>
                    {#if plugin.error}
                      <div class="settings-plugin-error">{plugin.error}</div>
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
  .settings-plugins {
    min-width: 0;
    margin-top: 20px;
  }

  .settings-plugins-toggle {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    cursor: pointer;
    font: inherit;
  }

  .settings-plugins-toggle :global(h3) {
    margin: 0;
  }

  .settings-plugin-description {
    margin-top: 4px;
    color: var(--text-muted);
    font-size: 12px;
  }

  .settings-plugin-kinds {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }

  .settings-plugin-kind {
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

  .settings-plugin-source {
    word-break: break-all;
  }

  .settings-plugin-health {
    font-size: 12px;
    font-weight: 600;
  }

  .settings-plugin-health.is-ok {
    color: var(--success);
  }

  .settings-plugin-health.is-error {
    color: var(--danger);
  }

  .settings-plugin-error {
    margin-top: 4px;
    color: var(--danger);
    font-size: 12px;
    word-break: break-word;
  }
</style>
