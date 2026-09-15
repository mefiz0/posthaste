<script lang="ts">
  import Icon from "./Icon.svelte";
  import {
    app,
    closeOutbox,
    discardOutbox,
    editOutbox,
    refreshOutbox,
    retryOutbox,
  } from "../lib/stores.svelte";
  import { formatDateFull } from "../lib/format";
  import { overlayFade } from "../lib/transitions";
  import type { OutboxItem } from "../lib/types";

  const items = $derived(app.outbox);

  function when(item: OutboxItem): string {
    const date = new Date(item.createdIso);
    return Number.isNaN(date.getTime()) ? "" : formatDateFull(date);
  }

  function accountLabel(accountId: number): string {
    return app.accounts.find((account) => account.id === accountId)?.email ?? "";
  }
</script>

<div class="overlay open sync-overlay" transition:overlayFade>
  <button class="backdrop" aria-label="Close outbox" onclick={closeOutbox}></button>
  <div class="dialog sync-panel" role="dialog" aria-modal="true" aria-label="Outbox">
    <div class="sync-head">
      <Icon name="sent" />
      <h2>Outbox</h2>
      <span class="grow"></span>
      <button class="btn" onclick={() => void refreshOutbox()}><Icon name="refresh" />Refresh</button>
      <button class="sync-close" title="Close" aria-label="Close" onclick={closeOutbox}>
        <Icon name="x" />
      </button>
    </div>

    <div class="outbox-list" role="list">
      {#if app.outboxLoading && !items.length}
        <div class="sync-empty">Loading…</div>
      {:else if items.length}
        {#each items as item (item.id)}
          <div class="outbox-item" role="listitem">
            <div class="outbox-main">
              <div class="outbox-subject">{item.subject || "(no subject)"}</div>
              <div class="outbox-meta">
                <span class="outbox-to">To: {item.to || "—"}</span>
                {#if app.accounts.length > 1 && accountLabel(item.accountId)}
                  <span class="outbox-account">{accountLabel(item.accountId)}</span>
                {/if}
                <span class="outbox-time">{when(item)}</span>
              </div>
              {#if item.state === "failed"}
                <div class="outbox-error" title={item.error}>
                  {item.error || "Delivery failed"}
                  {#if item.attempts > 1}· {item.attempts} attempts{/if}
                </div>
              {:else}
                <div class="outbox-pending">Waiting to send · retrying automatically</div>
              {/if}
            </div>
            <div class="outbox-actions">
              {#if item.state === "failed"}
                <button class="cbtn" onclick={() => void retryOutbox(item)}><Icon name="refresh" />Retry</button>
                <button class="cbtn" onclick={() => void editOutbox(item)}><Icon name="edit" />Edit and resend</button>
                <button class="cbtn" onclick={() => void discardOutbox(item)}>Discard</button>
              {:else}
                <button class="cbtn" onclick={() => void discardOutbox(item)}>Cancel</button>
              {/if}
            </div>
          </div>
        {/each}
      {:else}
        <div class="sync-empty">No failed or pending messages.</div>
      {/if}
    </div>
  </div>
</div>
