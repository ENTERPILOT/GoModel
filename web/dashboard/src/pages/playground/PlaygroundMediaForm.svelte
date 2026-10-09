<script>
  // Inputs of the active media mode (image prompt + size/count, speech text +
  // voice/format, or an audio file to transcribe) and the Send/Stop button.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import { Eraser, Send, Square } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
  import { playgroundStore as chat } from "./playground.svelte.js";
  import { playgroundMediaStore as media } from "./playgroundMedia.svelte.js";
  import { IMAGE_SIZES, MAX_IMAGE_COUNT, SPEECH_FORMATS, SPEECH_VOICES } from "./playgroundMedia.js";

  const voiceOptions = SPEECH_VOICES.map((voice) => ({ value: voice, label: voice }));

  // Cmd/Ctrl+Enter sends from the prompt textareas; plain Enter adds a line.
  function onKeydown(event) {
    if (event.key !== "Enter" || !(event.metaKey || event.ctrlKey) || event.isComposing) return;
    event.preventDefault();
    if (media.canSend) media.send();
  }
</script>

<form
  class="playground-media-form"
  onsubmit={(event) => {
    event.preventDefault();
    if (media.sending) media.stop();
    else media.send();
  }}
>
  {#if chat.mode === "image"}
    <FormField id="playground-image-prompt" label={m.playground_media_prompt_label()}>
      <textarea
        id="playground-image-prompt"
        rows="3"
        placeholder={m.playground_media_image_placeholder()}
        bind:value={media.imagePrompt}
        onkeydown={onKeydown}
        disabled={media.sending}
      ></textarea>
    </FormField>
    <div class="playground-media-options">
      <FormField id="playground-image-size" label={m.playground_media_size_label()}>
        <select id="playground-image-size" class="form-select" bind:value={media.imageSize} disabled={media.sending}>
          {#each IMAGE_SIZES as size (size)}
            <option value={size}>{size || m.playground_media_default_option()}</option>
          {/each}
        </select>
      </FormField>
      <FormField id="playground-image-count" label={m.playground_media_count_label()}>
        <input
          id="playground-image-count"
          class="form-input"
          type="number"
          min="1"
          max={MAX_IMAGE_COUNT}
          bind:value={media.imageCount}
          disabled={media.sending}
        />
      </FormField>
    </div>
  {:else if chat.mode === "speech"}
    <FormField id="playground-speech-input" label={m.playground_media_speech_input_label()}>
      <textarea
        id="playground-speech-input"
        rows="3"
        placeholder={m.playground_media_speech_placeholder()}
        bind:value={media.speechInput}
        onkeydown={onKeydown}
        disabled={media.sending}
      ></textarea>
    </FormField>
    <div class="playground-media-options">
      <FormField id="playground-speech-voice" label={m.playground_media_voice_label()}>
        <SearchSelect
          id="playground-speech-voice"
          options={voiceOptions}
          value={media.speechVoice}
          onchange={(value) => {
            media.speechVoice = value;
          }}
          ariaLabel={m.playground_media_voice_label()}
          disabled={media.sending}
          allowCustom
          mono
        />
      </FormField>
      <FormField id="playground-speech-format" label={m.playground_media_format_label()}>
        <select id="playground-speech-format" class="form-select" bind:value={media.speechFormat} disabled={media.sending}>
          {#each SPEECH_FORMATS as format (format)}
            <option value={format}>{format}</option>
          {/each}
        </select>
      </FormField>
    </div>
  {:else if chat.mode === "transcription"}
    <FormField id="playground-transcription-file" label={m.playground_media_file_label()}>
      <input
        id="playground-transcription-file"
        class="form-input"
        type="file"
        accept="audio/*,video/*"
        disabled={media.sending}
        onchange={(event) => {
          media.transcriptionFile = event.currentTarget.files?.[0] || null;
        }}
      />
    </FormField>
    <div class="playground-media-options">
      <FormField id="playground-transcription-language" label={m.playground_media_language_label()}>
        <input
          id="playground-transcription-language"
          class="form-input"
          placeholder={m.playground_media_language_placeholder()}
          bind:value={media.transcriptionLanguage}
          disabled={media.sending}
        />
      </FormField>
      <FormField id="playground-transcription-prompt" label={m.playground_media_prompt_label()}>
        <input
          id="playground-transcription-prompt"
          class="form-input"
          placeholder={m.playground_media_transcription_prompt_placeholder()}
          bind:value={media.transcriptionPrompt}
          disabled={media.sending}
        />
      </FormField>
    </div>
  {/if}

  <div class="playground-media-actions">
    <span class="playground-media-path mono" title={m.playground_help()}>POST {media.endpointPath}</span>
    <button
      type="button"
      class="btn btn-with-icon"
      disabled={media.sending || (!media.response && !media.error)}
      onclick={() => media.clear()}
    >
      <Icon icon={Eraser} class="table-icon-svg" />
      <span>{m.playground_clear()}</span>
    </button>
    {#if media.sending}
      <button type="submit" class="btn btn-danger-outline btn-with-icon">
        <Icon icon={Square} class="table-icon-svg" />
        <span>{m.playground_stop()}</span>
      </button>
    {:else}
      <button type="submit" class="btn btn-primary btn-with-icon" disabled={!media.canSend}>
        <Icon icon={Send} class="table-icon-svg" />
        <span>{m.common_action_send()}</span>
      </button>
    {/if}
  </div>
</form>

<style>
  .playground-media-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 12px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }

  .playground-media-form textarea {
    max-height: 30vh;
    background: var(--bg);
    line-height: 1.5;
    field-sizing: content;
  }

  .playground-media-options {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 12px;
  }

  .playground-media-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .playground-media-path {
    flex: 1 1 0;
    min-width: 0;
    color: var(--text-muted);
    font-size: 12px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
