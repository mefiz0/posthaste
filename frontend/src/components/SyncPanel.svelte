<script lang="ts">
  import Icon from "./Icon.svelte";
  import { app, closeSyncPanel, syncAll } from "../lib/stores.svelte";

  const accounts = $derived(
    app.accounts.map((account) => ({
      ...account,
      state: app.syncStates[account.id]?.state ?? "idle",
    })),
  );
  // Newest activity first, so opening the panel shows the current work.
  const entries = $derived([...app.syncLog].reverse());
</script>

<div class="overlay open sync-overlay">
  <button class="backdrop" aria-label="Close sync activity" onclick={closeSyncPanel}></button>
  <div class="dialog sync-panel" role="dialog" aria-modal="true" aria-label="Sync activity">
    <div class="sync-head">
      <Icon name="refresh" />
      <h2>Sync activity</h2>
      <span class="grow"></span>
      <button class="btn" onclick={() => void syncAll(true)}><Icon name="refresh" />Sync now</button>
      <button class="sync-close" title="Close" aria-label="Close" onclick={closeSyncPanel}>
        <Icon name="x" />
      </button>
    </div>

    <div class="sync-accounts">
      {#each accounts as account (account.id)}
        <div class="sync-account">
          <span class="acct-dot" style="background:{account.color ?? 'var(--muted)'}"></span>
          <span class="sync-account-email">{account.email}</span>
          <span class="sync-account-state" data-state={account.state}>{account.state}</span>
        </div>
      {/each}
    </div>

    <div class="sync-log" role="log" aria-label="Sync log">
      {#if entries.length}
        {#each entries as entry (entry.id)}
          <div class="sync-line" data-level={entry.level}>
            <span class="sync-time">{entry.time}</span>
            <span class="sync-text">{entry.text}</span>
          </div>
        {/each}
      {:else}
        <div class="sync-empty">No sync activity yet.</div>
      {/if}
    </div>
  </div>
</div>
