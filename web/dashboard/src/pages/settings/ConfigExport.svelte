<script>
  // Configuration export (GET /admin/config/export): downloads every setting
  // in effect as a config.yaml. The gateway writes secrets as the references
  // they come from, never their values, so the file is safe to keep.
  import { auth } from "$lib/stores/auth.svelte.js";
  import { flash } from "$lib/stores/flash.svelte.js";
  import { apiFetch } from "$lib/api/client.js";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { Download } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const FILENAME = "gomodel-config.yaml";

  let loading = $state(false);

  async function exportConfig() {
    if (loading) {
      return;
    }
    loading = true;
    const generation = auth.generation;
    try {
      const res = await apiFetch("/admin/config/export");
      if (generation < auth.generation) {
        return;
      }
      if (res.status === 401) {
        auth.handleUnauthorized(generation);
        return;
      }
      if (!res.ok) {
        flash.error(m.settings_config_export_failed());
        return;
      }
      const url = URL.createObjectURL(await res.blob());
      const link = document.createElement("a");
      link.href = url;
      link.download = FILENAME;
      link.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      console.error("Failed to export configuration:", e);
      flash.error(m.settings_config_export_failed());
    } finally {
      loading = false;
    }
  }
</script>

<div class="settings-refresh-section">
  <InlineHelpSection
    copyId="config-export-help-copy"
    label={m.settings_config_export_help_label()}
    text={m.settings_config_export_help()}
  >
    {#snippet title()}<h3>{m.settings_config_export_title()}</h3>{/snippet}
  </InlineHelpSection>
  <div class="settings-refresh-actions">
    <button
      type="button"
      class="btn btn-with-icon"
      disabled={loading}
      aria-busy={loading ? "true" : "false"}
      aria-describedby="config-export-help-copy"
      onclick={exportConfig}
    >
      <Icon icon={Download} class="config-export-icon" />
      <span>{m.settings_config_export_action()}</span>
    </button>
  </div>
</div>
