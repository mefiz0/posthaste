<script lang="ts">
  import Icon from "./Icon.svelte";
  import {
    app,
    accountIdForMessage,
    closeAttachmentViewer,
    showToast,
  } from "../lib/stores.svelte";
  import { api } from "../lib/api";
  import { formatBytes } from "../lib/format";
  import { isOfficePreviewable, renderOffice, type OfficePreview } from "../lib/office";

  const attachment = $derived(app.viewerAttachment);

  let dataUrl = $state<string | null>(null);
  let textContent = $state<string | null>(null);
  let officePreview = $state<OfficePreview | null>(null);
  let loading = $state(false);
  let error = $state("");

  type PreviewKind = "image" | "pdf" | "text" | "office" | "audio" | "video" | "other";

  function previewKind(mimeType: string): PreviewKind {
    const type = mimeType.toLowerCase();
    if (isOfficePreviewable(type)) return "office";
    if (type.startsWith("image/")) return "image";
    if (type === "application/pdf") return "pdf";
    if (type.startsWith("text/") || type === "application/json" || type === "application/xml")
      return "text";
    if (type.startsWith("audio/")) return "audio";
    if (type.startsWith("video/")) return "video";
    return "other";
  }

  const kind = $derived(previewKind(attachment?.mimeType ?? ""));

  function decodeDataUrlPayload(url: string): Uint8Array {
    const comma = url.indexOf(",");
    if (comma === -1) return new Uint8Array();
    const meta = url.slice(5, comma);
    const payload = url.slice(comma + 1);
    if (/;base64/i.test(meta)) {
      const binary = atob(payload);
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
      return bytes;
    }
    try {
      return new TextEncoder().encode(decodeURIComponent(payload));
    } catch {
      return new TextEncoder().encode(payload);
    }
  }

  function decodeTextPayload(url: string): string {
    return new TextDecoder("utf-8", { fatal: false }).decode(decodeDataUrlPayload(url));
  }

  // Load the bytes whenever the viewer opens for a different attachment.
  $effect(() => {
    const current = attachment;
    if (!current) return;
    const message = app.selectedMessage;
    if (!message) return;
    let cancelled = false;
    dataUrl = null;
    textContent = null;
    officePreview = null;
    error = "";
    loading = true;
    const currentKind = previewKind(current.mimeType);
    void api
      .getAttachmentDataURL(accountIdForMessage(message), message.id, current.id)
      .then(async (url) => {
        if (cancelled) return;
        dataUrl = url;
        if (currentKind === "text") {
          textContent = decodeTextPayload(url);
        } else if (currentKind === "office") {
          officePreview = await renderOffice(decodeDataUrlPayload(url), current.mimeType);
        }
      })
      .catch(() => {
        if (!cancelled) error = "This attachment could not be loaded. You can still save it.";
      })
      .finally(() => {
        if (!cancelled) loading = false;
      });
    return () => {
      cancelled = true;
    };
  });

  function openExternally(): void {
    const current = attachment;
    const message = app.selectedMessage;
    if (!current || !message) return;
    void api.openAttachment(accountIdForMessage(message), message.id, current.id).catch(() => {
      showToast("Could not open attachment", "error");
    });
  }

  function save(): void {
    const current = attachment;
    const message = app.selectedMessage;
    if (!current || !message) return;
    void api.saveAttachment(accountIdForMessage(message), message.id, current.id).catch(() => {
      showToast("Could not save attachment", "error");
    });
  }
</script>

<div class="overlay open viewer-overlay">
  <button class="backdrop" aria-label="Close attachment preview" onclick={closeAttachmentViewer}></button>
  <div class="dialog viewer" role="dialog" aria-modal="true" aria-label="Attachment preview" tabindex="-1">
    {#if attachment}
      <div class="viewer-head">
        <Icon name="clip" />
        <span class="viewer-name" title={attachment.filename}>{attachment.filename}</span>
        <span class="viewer-size">{formatBytes(attachment.sizeBytes)}</span>
        <span class="grow"></span>
        <button class="cbtn" onclick={openExternally}><Icon name="forward" />Open</button>
        <button class="cbtn" onclick={save}><Icon name="download" />Save</button>
        <button class="viewer-close" title="Close" aria-label="Close preview" onclick={closeAttachmentViewer}>
          <Icon name="x" />
        </button>
      </div>
      <div class="viewer-body">
        {#if loading}
          <div class="viewer-note">Loading…</div>
        {:else if error}
          <div class="viewer-note">{error}</div>
        {:else if kind === "image" && dataUrl}
          <img class="viewer-image" src={dataUrl} alt={attachment.filename} />
        {:else if kind === "pdf" && dataUrl}
          <iframe class="viewer-frame" src={dataUrl} title={attachment.filename}></iframe>
        {:else if kind === "text" && textContent != null}
          <pre class="viewer-text">{textContent}</pre>
        {:else if kind === "office" && officePreview}
          {#if officePreview.kind === "text"}
            <div class="office-doc">
              {#each officePreview.paragraphs as paragraph, index (index)}
                {#if paragraph.trim().length > 0}
                  <p>{paragraph}</p>
                {/if}
              {/each}
            </div>
          {:else}
            <div class="office-sheets">
              {#each officePreview.tables as table (table.name)}
                <h3>{table.name}</h3>
                <table class="office-table">
                  <tbody>
                    {#each table.rows as row, rowIndex (rowIndex)}
                      <tr>
                        {#each row as cell, cellIndex (cellIndex)}
                          <td>{cell}</td>
                        {/each}
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {/each}
            </div>
          {/if}
        {:else if kind === "audio" && dataUrl}
          <audio class="viewer-media" src={dataUrl} controls></audio>
        {:else if kind === "video" && dataUrl}
          <!-- svelte-ignore a11y_media_has_caption -->
          <video class="viewer-media" src={dataUrl} controls></video>
        {:else if dataUrl}
          <div class="viewer-note">
            No inline preview for this file type. Use Open or Save.
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>
