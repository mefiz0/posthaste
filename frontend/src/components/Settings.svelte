<script lang="ts">
  import Icon from './Icon.svelte';
  import { focusTrap } from '../lib/focus';
  import { overlayFade } from '../lib/transitions';
  import {
    app,
    closeSettings,
    openAccountSetup,
    removeAccount,
    saveSettings,
    setAccountPaused,
  } from '../lib/stores.svelte';
  import {
    eventToChord,
    findChordOwner,
    formatChordParts,
    getKeymap,
    serializeChord,
    type KeyChord,
  } from '../lib/keys';
  import type { AppSettings } from '../lib/types';

  let draft = $state<AppSettings | null>(null);
  let confirmRemoveId = $state<number | null>(null);
  let rebindError = $state('');

  $effect(() => {
    if (app.settingsOpen && app.settings) {
      draft = structuredClone(app.settings);
      confirmRemoveId = null;
      rebindError = '';
    }
  });

  const effectiveKeymap = $derived(draft ? getKeymap(draft.keymap) : []);

  function listeningId(): string | null {
    return app.settingsKeyListening;
  }

  function startRebind(actionId: string): void {
    app.settingsKeyListening = actionId;
    rebindError = '';
  }

  function cancelRebind(): void {
    app.settingsKeyListening = null;
    rebindError = '';
  }

  function onCaptureKeydown(event: KeyboardEvent): void {
    const actionId = listeningId();
    if (!actionId || !draft) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.key === 'Escape') {
      cancelRebind();
      return;
    }
    if (event.key === 'Shift' || event.key === 'Control' || event.key === 'Alt' || event.key === 'Meta') return;
    const chord: KeyChord = eventToChord(event);
    const owner = findChordOwner(effectiveKeymap, chord);
    if (owner && owner !== actionId) {
      rebindError = `Already used by "${effectiveKeymap.find((binding) => binding.id === owner)?.label ?? owner}".`;
      app.settingsKeyListening = null;
      return;
    }
    draft.keymap = { ...draft.keymap, [actionId]: serializeChord(chord) };
    app.settingsKeyListening = null;
    rebindError = '';
  }

  function resetKeymap(): void {
    if (!draft) return;
    draft.keymap = {};
    rebindError = '';
  }

  async function onSave(): Promise<void> {
    if (!draft) return;
    await saveSettings(structuredClone(draft));
    closeSettings();
  }

  function onRemove(accountId: number): void {
    if (confirmRemoveId === accountId) {
      confirmRemoveId = null;
      void removeAccount(accountId);
    } else {
      confirmRemoveId = accountId;
    }
  }

  function eagerMb(value: number): number {
    return Math.round(value / (1024 * 1024));
  }
</script>

<svelte:window onkeydown={onCaptureKeydown} />

<div class="overlay open" transition:overlayFade role="presentation">
  <button class="backdrop" aria-label="Close settings" onclick={closeSettings}></button>
  <div class="dialog pane" role="dialog" aria-modal="true" aria-label="Settings" use:focusTrap>
    <div class="pane-head">
      <h2>Settings</h2>
      <span class="spacer"></span>
      <button class="x" title="Close" aria-label="Close settings" onclick={closeSettings}>
        <Icon name="more" />
      </button>
    </div>

    {#if draft}
      <h3>General</h3>
      <div class="switchrow">
        <span class="grow">Desktop notifications
          <span class="note">New mail, send failures, and account problems. Never routine sync activity.</span>
        </span>
        <input type="checkbox" bind:checked={draft.notificationsEnabled} aria-label="Enable desktop notifications" />
      </div>
      <div class="switchrow">
        <span class="grow">Minimize to system tray on close
          <span class="note">
            On GNOME the tray needs the AppIndicator extension; if it is unavailable Posthaste minimizes to the
            taskbar instead, so the window never silently disappears.
          </span>
        </span>
        <input type="checkbox" bind:checked={draft.minimizeToTray} aria-label="Minimize to system tray on close" />
      </div>
      <div class="switchrow">
        <span class="grow">Verbose logging
          <span class="note">More detail in the local log file. Message content and credentials are always scrubbed.</span>
        </span>
        <input type="checkbox" bind:checked={draft.verboseLogging} aria-label="Enable verbose logging" />
      </div>
      <div class="switchrow">
        <span class="grow">Crash reporting
          <span class="note">
            Off by default and separate from telemetry. When on, a crash writes a local report containing only the
            panic message and stack trace, your OS and architecture, and the app version. Message bodies, subjects,
            addresses, attachment names, and credentials never leave the device.
          </span>
        </span>
        <input type="checkbox" bind:checked={draft.crashReportingEnabled} aria-label="Enable crash reporting" />
      </div>
      <div class="switchrow">
        <span class="grow">Eagerly fetch attachments smaller than (MB)
          <span class="note">Larger attachments download on first open to save bandwidth and disk.</span>
        </span>
        <input
          class="text-input"
          style="width:72px"
          type="number"
          min="0"
          aria-label="Eager attachment threshold in megabytes"
          value={eagerMb(draft.attachmentEagerThresholdBytes)}
          oninput={(event) => {
            const mb = Number((event.target as HTMLInputElement).value);
            if (draft) draft.attachmentEagerThresholdBytes = Math.max(0, Math.round(mb * 1024 * 1024));
          }}
        />
      </div>

      <h3>Accounts</h3>
      {#each app.accounts as account (account.id)}
        <div class="acct-row">
          <span class="acct-dot" style="background:{account.color ?? 'var(--muted)'}"></span>
          <span class="email">{account.email}</span>
          {#if account.isDefault}<span class="badge">Default</span>{/if}
          {#if account.paused}<span class="badge" style="color:var(--muted);border-color:var(--border)">Paused</span>{/if}
          <span class="grow"></span>
          <button class="btn" onclick={() => void setAccountPaused(account.id, !account.paused)}>
            {account.paused ? 'Resume' : 'Pause'}
          </button>
          <button class="btn danger" onclick={() => onRemove(account.id)}>
            {confirmRemoveId === account.id ? 'Really remove?' : 'Remove'}
          </button>
        </div>
      {/each}
      <div class="pane-foot" style="margin-top:8px">
        <button class="btn" onclick={openAccountSetup}><Icon name="user" />Add account</button>
      </div>

      <h3>Keyboard shortcuts</h3>
      <p class="note">Click a shortcut, then press the new key combination. Esc cancels.</p>
      {#if rebindError}<p class="form-error">{rebindError}</p>{/if}
      {#each effectiveKeymap as binding (binding.id)}
        <div class="km-row">
          <span>{binding.label}</span>
          <span class="sc-keys">
            {#if listeningId() === binding.id}
              <kbd class="listening">Press keys…</kbd>
            {:else}
              {#each binding.chords as alternative (alternative.map((chord) => chord.key).join('+'))}
                {#each alternative as chord (chord.key + String(chord.ctrl) + String(chord.alt))}
                  {#each formatChordParts(chord) as part, index (index)}
                    <kbd>{part}</kbd>
                  {/each}
                {/each}
              {/each}
            {/if}
          </span>
          <button class="km-change" onclick={() => (listeningId() === binding.id ? cancelRebind() : startRebind(binding.id))}>
            {listeningId() === binding.id ? 'Cancel' : 'Change'}
          </button>
        </div>
      {/each}
      <div class="pane-foot" style="margin-top:8px">
        <button class="btn" onclick={resetKeymap}>Reset to defaults</button>
      </div>

      <h3>Crash reports</h3>
      <p class="note">
        Crash reporting is planned for a later release. When it ships it will be strictly opt-in, off by default,
        and this section will list exactly what a report contains. No telemetry of any kind exists today.
      </p>

      <div class="pane-foot">
        <span class="grow"></span>
        <button class="btn" onclick={closeSettings}>Cancel</button>
        <button class="btn primary" onclick={() => void onSave()}>Save</button>
      </div>
    {/if}
  </div>
</div>
