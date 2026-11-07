<script>
  // Output of the last media request: generated images, a player for the
  // synthesized speech, the transcript, or a summary of the embedding vectors.
  // The raw body is in the JSON panel.
  import * as m from "$lib/paraglide/messages.js";
  import { playgroundStore as chat } from "./playground.svelte.js";
  import { playgroundMediaStore as media } from "./playgroundMedia.svelte.js";

  const hasResult = $derived(
    (chat.mode === "image" && media.images.length > 0) ||
      (chat.mode === "speech" && media.audioURL !== "") ||
      (chat.mode === "transcription" && media.transcript !== "") ||
      (chat.mode === "embeddings" && media.embeddings?.count > 0),
  );

  function previewText(vector) {
    if (vector.base64) return m.playground_embeddings_base64_value();
    const values = vector.preview.join(", ");
    return vector.length > vector.preview.length ? "[" + values + ", …]" : "[" + values + "]";
  }
</script>

<div class="playground-media-result" aria-live="polite" aria-busy={media.sending}>
  {#if media.sending}
    <p class="playground-media-status" role="status">
      <span class="loading-spinner" aria-hidden="true"></span>
      <span>{m.playground_sending()}</span>
    </p>
  {:else if !hasResult}
    <p class="empty-state">{m.playground_media_empty()}</p>
  {:else if chat.mode === "image"}
    <div class="playground-media-images">
      {#each media.images as image, index (index)}
        <figure class="playground-media-image">
          <img src={image.src} alt={image.revisedPrompt || media.imagePrompt} />
          {#if image.revisedPrompt}
            <figcaption>{image.revisedPrompt}</figcaption>
          {/if}
        </figure>
      {/each}
    </div>
  {:else if chat.mode === "speech"}
    <audio class="playground-media-audio" controls src={media.audioURL}></audio>
  {:else if chat.mode === "embeddings"}
    <div class="playground-embeddings">
      <p class="playground-embeddings-summary">
        <span>{m.playground_embeddings_vectors({ count: media.embeddings.count })}</span>
        {#if media.embeddings.dimensions !== null}
          <span>{m.playground_embeddings_dimensions({ count: media.embeddings.dimensions })}</span>
        {/if}
        {#if media.embeddings.promptTokens !== null}
          <span>{m.playground_embeddings_prompt_tokens({ count: media.embeddings.promptTokens })}</span>
        {/if}
      </p>
      <ol class="playground-embeddings-list">
        {#each media.embeddings.vectors as vector, position (position)}
          <li>
            <span class="playground-embeddings-index mono">#{vector.index}</span>
            <code class="playground-embeddings-values">{previewText(vector)}</code>
            {#if media.embeddings.dimensions === null && vector.length !== null}
              <span class="playground-embeddings-length">
                {m.playground_embeddings_dimensions({ count: vector.length })}
              </span>
            {/if}
          </li>
        {/each}
      </ol>
    </div>
  {:else}
    <pre class="playground-media-transcript">{media.transcript}</pre>
  {/if}
</div>

<style>
  .playground-media-result {
    display: flex;
    flex: 1 1 0;
    flex-direction: column;
    min-height: 0;
    padding: 4px 2px 12px;
    overflow-y: auto;
  }

  .playground-media-result .empty-state {
    margin: auto;
    max-width: 420px;
  }

  .playground-media-status {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    margin: auto;
    color: var(--text-muted);
    font-size: 13px;
  }

  .playground-media-images {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 12px;
  }

  .playground-media-image {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 0;
  }

  .playground-media-image img {
    display: block;
    width: 100%;
    height: auto;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }

  .playground-media-image figcaption {
    color: var(--text-muted);
    font-size: 12px;
    line-height: 1.4;
  }

  .playground-media-audio {
    width: 100%;
    max-width: 560px;
  }

  .playground-media-transcript {
    margin: 0;
    padding: 12px 14px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    font-family: inherit;
    font-size: 13px;
    line-height: 1.6;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  .playground-embeddings {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .playground-embeddings-summary {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 16px;
    margin: 0;
    color: var(--text);
    font-size: 13px;
    font-weight: 600;
  }

  .playground-embeddings-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .playground-embeddings-list li {
    display: flex;
    align-items: baseline;
    gap: 10px;
    padding: 8px 12px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    font-size: 12px;
  }

  .playground-embeddings-index,
  .playground-embeddings-length {
    flex: none;
    color: var(--text-muted);
  }

  .playground-embeddings-values {
    flex: 1 1 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }

  @media (max-width: 768px) {
    .playground-media-result {
      flex: 0 1 auto;
      min-height: 160px;
    }
  }
</style>
