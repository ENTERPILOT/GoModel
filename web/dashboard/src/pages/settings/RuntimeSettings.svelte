<script>
  // Generic deployment-wide settings registered by extensions. The backend
  // supplies labels, descriptions and allowed values; the dashboard never
  // needs Pro-specific configuration knowledge.
  import { auth } from "$lib/stores/auth.svelte.js";
  import { flash } from "$lib/stores/flash.svelte.js";
  import { getJSON, sendJSON } from "$lib/api/client.js";

  let settings = $state([]);
  let loading = $state(false);
  let savingKey = $state("");
  let error = $state("");

  async function load() {
    loading = true;
    error = "";
    try {
      const result = await getJSON("/admin/runtime/settings", {
        label: "runtime settings",
      });
      if (result.stale) return;
      if (!result.ok) {
        error = "Unable to load runtime settings.";
        return;
      }
      settings = Array.isArray(result.data?.settings)
        ? result.data.settings
        : [];
    } catch (e) {
      console.error("Failed to load runtime settings:", e);
      error = "Unable to load runtime settings.";
    } finally {
      loading = false;
    }
  }

  async function save(setting, value, select) {
    if (setting.locked || savingKey) return;
    savingKey = setting.key;
    try {
      const result = await sendJSON(
        `/admin/runtime/settings/${encodeURIComponent(setting.key)}`,
        "PUT",
        { value },
        { label: `save ${setting.label}` },
      );
      if (result.stale) {
        select.value = setting.value;
        return;
      }
      if (!result.ok) {
        select.value = setting.value;
        flash.error(`Unable to save ${setting.label}.`);
        return;
      }
      settings = settings.map((item) =>
        item.key === setting.key ? result.data : item,
      );
      flash.success(`${setting.label} saved.`);
    } catch (e) {
      select.value = setting.value;
      console.error("Failed to save runtime setting:", e);
      flash.error(`Unable to save ${setting.label}.`);
    } finally {
      savingKey = "";
    }
  }

  $effect(() => {
    void auth.refreshTick;
    load();
  });
</script>

{#if settings.length > 0 || error}
  <div class="settings-refresh-section runtime-settings-section">
    <div class="runtime-settings-copy">
      <h3>AI Processing</h3>
      {#if error}
        <p class="form-field-error" role="alert">{error}</p>
      {/if}
    </div>
    <div class="runtime-settings-fields" aria-busy={loading ? "true" : "false"}>
      {#each settings as setting (setting.key)}
        <div class="runtime-setting-row">
          <div>
            <label class="form-field-label" for={`runtime-setting-${setting.key}`}>
              {setting.label}
            </label>
            {#if setting.description}
              <p class="runtime-setting-description">{setting.description}</p>
            {/if}
            {#if setting.locked}
              <p class="runtime-setting-managed">
                Managed by {setting.managed_by || "environment"}
              </p>
            {/if}
          </div>
          <select
            id={`runtime-setting-${setting.key}`}
            class="form-select settings-select"
            value={setting.value}
            disabled={setting.locked || savingKey === setting.key}
            onchange={(event) =>
              save(setting, event.currentTarget.value, event.currentTarget)}
          >
            {#each setting.options || [] as option (option.value)}
              <option value={option.value} title={option.description || ""}>
                {option.label}
              </option>
            {/each}
          </select>
        </div>
      {/each}
    </div>
  </div>
{/if}

<style>
  .runtime-settings-copy {
    min-width: 180px;
  }

  .runtime-settings-fields {
    display: grid;
    gap: 16px;
    width: min(560px, 100%);
  }

  .runtime-setting-row {
    display: grid;
    grid-template-columns: minmax(220px, 1fr) 180px;
    gap: 20px;
    align-items: center;
  }

  .runtime-setting-description,
  .runtime-setting-managed {
    margin: 4px 0 0;
    color: var(--text-muted);
    font-size: 12px;
  }

  .runtime-setting-managed {
    color: var(--warning);
  }

  @media (max-width: 720px) {
    .runtime-setting-row {
      grid-template-columns: 1fr;
      gap: 8px;
    }
  }
</style>
