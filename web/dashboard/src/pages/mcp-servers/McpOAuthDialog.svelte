<script>
  // OAuth sign-in dialog for one MCP server. Continue asks the gateway to
  // discover the server's authorization server and register a client, then
  // sends the browser there; the callback returns to this page. The client
  // fields are only needed when the authorization server has no dynamic
  // client registration.
  import FormField from "$lib/components/molecules/FormField.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import { mcpServers } from "./mcpServers.svelte.js";
  import { mcpPresetForURL } from "./mcpPresets.js";
  import { LogIn } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const name = $derived(String(mcpServers.oauthServer?.name || ""));
  const redirectURI = $derived(mcpServers.oauthOpen ? mcpServers.oauthRedirectURI() : "");
  // A preset whose authorization server has no dynamic client registration
  // links to the vendor page where the operator registers the OAuth app.
  const manualPreset = $derived.by(() => {
    const preset = mcpPresetForURL(mcpServers.oauthServer?.url);
    return preset?.oauthClient === "manual" ? preset : null;
  });
</script>

<EditorDialog
  open={mcpServers.oauthOpen}
  title={m.mcp_oauth_title({ name })}
  error={mcpServers.oauthError}
  submitting={mcpServers.oauthSubmitting}
  submitLabel={m.mcp_oauth_continue()}
  submittingLabel={m.mcp_oauth_continuing()}
  submitIcon={LogIn}
  canClose={() => !mcpServers.oauthSubmitting}
  onclose={() => mcpServers.closeSignIn()}
  onsubmit={() => mcpServers.submitSignIn()}
>
  {#snippet headerHint()}
    <p class="form-hint">{m.mcp_oauth_help()}</p>
  {/snippet}

  <div class="form-field">
    <span class="form-field-label">{m.mcp_oauth_redirect_uri()}</span>
    <code class="mono mcp-oauth-redirect">{redirectURI}</code>
    <small class="form-hint">{m.mcp_oauth_redirect_uri_help()}</small>
  </div>

  <details class="form-section" open={mcpServers.oauthClientRequired}>
    <summary class="form-field-label">{m.mcp_oauth_client_section()}</summary>
    {#if manualPreset}
      <p class="form-hint">
        {m.mcp_preset_oauth_client_steps({ name: manualPreset.name })}
        <a href={manualPreset.clientURL} target="_blank" rel="noopener noreferrer"
          >{m.mcp_preset_oauth_manual_link({ name: manualPreset.name })}</a
        >
      </p>
    {:else}
      <p class="form-hint">{m.mcp_oauth_client_help()}</p>
    {/if}
    <FormField id="mcp-oauth-client-id" label={m.mcp_oauth_client_id()}>
      <input
        id="mcp-oauth-client-id"
        type="text"
        class="mono"
        autocomplete="off"
        bind:value={mcpServers.oauthForm.client_id}
      />
    </FormField>
    <FormField id="mcp-oauth-client-secret" label={m.mcp_oauth_client_secret()}>
      <input
        id="mcp-oauth-client-secret"
        type="password"
        class="mono"
        autocomplete="off"
        bind:value={mcpServers.oauthForm.client_secret}
      />
      <small class="form-hint">{m.mcp_oauth_client_secret_help()}</small>
    </FormField>
  </details>
</EditorDialog>

<style>
  .mcp-oauth-redirect {
    display: block;
    word-break: break-all;
    font-size: 12px;
  }
</style>
