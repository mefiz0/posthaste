<script lang="ts">
  import { untrack } from 'svelte';
  import Icon from './Icon.svelte';
  import { focusTrap } from '../lib/focus';
  import {
    app,
    defaultAccount,
    discardCompose,
    markComposeDirty,
    registerComposeHandlers,
    requestCloseCompose,
    cancelCloseCompose,
    saveCompose,
    sendCompose,
    showToast,
  } from '../lib/stores.svelte';
  import { api } from '../lib/api';
  import { formatBytes } from '../lib/format';
  import { buildComposePrefill, formatAddress, isAddressLike, splitAddress } from '../lib/compose';
  import type { Contact, DraftInput, PickedFile } from '../lib/types';

  interface Chip {
    name: string;
    address: string;
  }

  let toChips = $state<Chip[]>([]);
  let ccChips = $state<Chip[]>([]);
  let toText = $state('');
  let ccText = $state('');
  let subject = $state('');
  let body = $state('');
  let attachments = $state<PickedFile[]>([]);
  let picking = $state(false);
  let fromAccountId = $state<number | null>(null);
  let showCc = $state(false);
  let suggestions = $state<Contact[]>([]);
  let suggestionTarget = $state<'to' | 'cc' | null>(null);
  let suggestionIndex = $state(0);

  const mode = $derived(app.compose.mode);
  const title = $derived(
    mode === 'new' ? 'New message' : mode === 'forward' ? 'Forward' : mode === 'replyAll' ? 'Reply all' : 'Reply',
  );
  const saving = $derived(app.compose.saving);
  const confirmingClose = $derived(app.compose.confirmingClose);

  // Seed the fields once per open. Everything except the open-token is read
  // untracked: background event refreshes must not clobber in-progress edits.
  $effect(() => {
    const token = app.compose.token;
    untrack(() => {
      if (!token || !app.compose.open) return;
      const current = app.selectedMessage;
      const prefill =
        mode === 'new' || !current
          ? { toAddresses: [], ccAddresses: [], subject: '', bodyText: '' }
          : buildComposePrefill(mode, current, defaultAccount()?.email ?? '');
      toChips = prefill.toAddresses.map(splitAddress);
      ccChips = prefill.ccAddresses.map(splitAddress);
      toText = '';
      ccText = '';
      showCc = ccChips.length > 0;
      subject = prefill.subject;
      body = prefill.bodyText;
      attachments = [];
      fromAccountId = current
        ? app.accounts.find((account) => account.id === accountIdOf(current))?.id ?? defaultAccount()?.id ?? null
        : defaultAccount()?.id ?? null;
      suggestions = [];
      suggestionTarget = null;
    });
  });

  function accountIdOf(message: { folderId: number }): number {
    return app.folders.find((folder) => folder.id === message.folderId)?.accountId ?? defaultAccount()?.id ?? 0;
  }

  function touch(): void {
    markComposeDirty();
  }

  // ---------- recipients ----------

  let suggestTimer: ReturnType<typeof setTimeout> | null = null;

  function lastToken(text: string): string {
    const parts = text.split(/[;,]/);
    return (parts[parts.length - 1] ?? '').trim();
  }

  function stripLastToken(text: string): string {
    const index = Math.max(text.lastIndexOf(','), text.lastIndexOf(';'));
    return index === -1 ? '' : `${text.slice(0, index + 1)} `;
  }

  function onRecipientInput(target: 'to' | 'cc'): void {
    touch();
    const text = target === 'to' ? toText : ccText;
    const token = lastToken(text);
    if (suggestTimer !== null) clearTimeout(suggestTimer);
    if (!token || !fromAccountId) {
      suggestions = [];
      suggestionTarget = null;
      return;
    }
    suggestionTarget = target;
    suggestionIndex = 0;
    suggestTimer = setTimeout(() => {
      void api
        .listContacts(fromAccountId ?? 0, token)
        .then((contacts) => {
          if (suggestionTarget === target) suggestions = contacts;
        })
        .catch(() => {
          suggestions = [];
        });
    }, 120);
  }

  function onRecipientBlur(target: 'to' | 'cc'): void {
    // Delay so clicking a suggestion still lands.
    setTimeout(() => {
      if (suggestionTarget === target) {
        suggestions = [];
        suggestionTarget = null;
      }
      commitChips(target);
    }, 120);
  }

  function pickSuggestion(contact: Contact, target: 'to' | 'cc'): void {
    const chip: Chip = { name: contact.name, address: contact.address };
    if (target === 'to') {
      toText = stripLastToken(toText);
      toChips = [...toChips, chip];
    } else {
      ccText = stripLastToken(ccText);
      ccChips = [...ccChips, chip];
    }
    suggestions = [];
    suggestionTarget = null;
    showCc = showCc || target === 'cc';
    touch();
  }

  function commitChips(target: 'to' | 'cc'): void {
    const raw = target === 'to' ? toText : ccText;
    const parts = raw.split(/[;,]/).map((part) => part.trim()).filter(Boolean);
    const complete = parts.filter(isAddressLike).map(splitAddress);
    const rest = parts.filter((part) => !isAddressLike(part)).join(', ');
    if (!complete.length) {
      if (target === 'to') toText = rest;
      else ccText = rest;
      return;
    }
    if (target === 'to') {
      toChips = [...toChips, ...complete];
      toText = rest;
    } else {
      ccChips = [...ccChips, ...complete];
      ccText = rest;
      showCc = true;
    }
  }

  function removeChip(target: 'to' | 'cc', index: number): void {
    if (target === 'to') toChips = toChips.filter((_, i) => i !== index);
    else ccChips = ccChips.filter((_, i) => i !== index);
    touch();
  }

  function onRecipientKeydown(event: KeyboardEvent, target: 'to' | 'cc'): void {
    if (suggestionTarget === target && suggestions.length) {
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        suggestionIndex = Math.min(suggestionIndex + 1, suggestions.length - 1);
        return;
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault();
        suggestionIndex = Math.max(suggestionIndex - 1, 0);
        return;
      }
      if (event.key === 'Enter' || event.key === 'Tab') {
        event.preventDefault();
        const contact = suggestions[suggestionIndex];
        if (contact) pickSuggestion(contact, target);
        return;
      }
      if (event.key === 'Escape') {
        event.stopPropagation();
        suggestions = [];
        suggestionTarget = null;
        return;
      }
    }
    if (event.key === 'Enter' || event.key === ',' || event.key === ';') {
      event.preventDefault();
      commitChips(target);
      return;
    }
    if (event.key === 'Escape') {
      event.stopPropagation();
      requestCloseCompose();
    }
  }

  // ---------- draft assembly ----------

  function addressesOf(target: 'to' | 'cc'): string[] {
    const chips = target === 'to' ? toChips : ccChips;
    const text = target === 'to' ? toText : ccText;
    const fromText = text
      .split(/[;,]/)
      .map((part) => part.trim())
      .filter((part) => isAddressLike(part))
      .map((part) => splitAddress(part))
      .map((chip) => formatAddress(chip.name, chip.address));
    return [...chips.map((chip) => formatAddress(chip.name, chip.address)), ...fromText];
  }

  function buildDraft(): DraftInput {
    commitChips('to');
    commitChips('cc');
    const current = app.selectedMessage;
    return {
      draftId: app.compose.draftId ?? undefined,
      accountId: fromAccountId ?? undefined,
      inReplyToMessageId: mode !== 'new' && current ? String(current.id) : undefined,
      toAddresses: addressesOf('to'),
      ccAddresses: addressesOf('cc'),
      bccAddresses: [],
      subject: subject.trim(),
      bodyText: body,
      attachments: attachments.length ? attachments.map((file) => file.path) : undefined,
    };
  }

  function onSave(): void {
    void saveCompose(buildDraft(), true);
  }

  function onSend(): void {
    void sendCompose(buildDraft());
  }

  // Register global handlers so Ctrl+Enter (send) works from the key dispatcher.
  $effect(() => {
    return registerComposeHandlers({
      send: onSend,
      attach: () => void attachFiles(),
    });
  });

  // Debounced autosave while dirty, per the draft-protection requirement.
  $effect(() => {
    if (!app.compose.open) return;
    const timer = setInterval(() => {
      if (app.compose.dirty && !app.compose.saving && app.compose.open) {
        void saveCompose(buildDraft(), false);
      }
    }, 2000);
    return () => clearInterval(timer);
  });

  // ---------- attachments ----------

  /**
   * Opens the native file dialog and adds the picks as chips. The paths ride
   * to the engine with the next save/send, which reads the files and keeps
   * their names; an empty result means the dialog was canceled.
   */
  async function attachFiles(): Promise<void> {
    if (picking) return;
    picking = true;
    try {
      const picked = await api.pickAttachments();
      const known = new Set(attachments.map((file) => file.path));
      const fresh = picked.filter((file) => !known.has(file.path));
      if (fresh.length) {
        attachments = [...attachments, ...fresh];
        touch();
      }
    } catch {
      showToast('Could not attach files', 'error');
    } finally {
      picking = false;
    }
  }

  function removeAttachment(index: number): void {
    attachments = attachments.filter((_, i) => i !== index);
    touch();
  }
</script>

<div class="overlay open" role="presentation">
  <button class="backdrop" aria-label="Close compose" onclick={requestCloseCompose}></button>
  <div
    class="dialog compose"
    role="dialog"
    aria-modal="true"
    aria-label="{title} — compose"
    use:focusTrap={{ autofocusSelector: '[data-autofocus]' }}
  >
    <div class="compose-head">
      <span class="ctitle">{title}</span>
      {#if app.compose.dirty && !confirmingClose}
        <span class="provider-tag" aria-live="polite">Unsaved changes</span>
      {/if}
      <span class="spacer"></span>
      <button class="x" title="Close" aria-label="Close compose" onclick={requestCloseCompose}>
        <Icon name="more" />
      </button>
    </div>

    <div class="compose-fields">
      {#if app.accounts.length > 1}
        <div class="cf">
          <label for="compose-from">From:</label>
          <div class="recip">
            <select
              id="compose-from"
              class="text-input"
              style="flex:1"
              bind:value={fromAccountId}
              onchange={touch}
            >
              {#each app.accounts as account (account.id)}
                <option value={account.id}>{account.displayName} &lt;{account.email}&gt;</option>
              {/each}
            </select>
          </div>
        </div>
      {/if}
      <div class="cf" style="position:relative">
        <label for="compose-to">To:</label>
        <div class="recip">
          {#each toChips as chip, index (index)}
            <span class="chip">
              <span>{chip.name ? `${chip.name} <${chip.address}>` : chip.address}</span>
              <button title="Remove recipient" aria-label="Remove {chip.address}" onclick={() => removeChip('to', index)}>×</button>
            </span>
          {/each}
          <input
            id="compose-to"
            data-autofocus
            type="text"
            spellcheck="false"
            placeholder="name@example.com"
            aria-label="To recipients"
            bind:value={toText}
            oninput={() => onRecipientInput('to')}
            onblur={() => onRecipientBlur('to')}
            onkeydown={(event) => onRecipientKeydown(event, 'to')}
          />
        </div>
        {#if suggestionTarget === 'to' && suggestions.length}
          <div class="ac-menu" role="listbox" aria-label="Contact suggestions">
            {#each suggestions as contact, index (contact.address)}
              <button
                class="ac-item"
                class:active={index === suggestionIndex}
                role="option"
                aria-selected={index === suggestionIndex}
                onmousedown={(event) => event.preventDefault()}
                onclick={() => pickSuggestion(contact, 'to')}
              >
                <span class="ac-name">{contact.name || contact.address}</span>
                <span class="ac-addr">{contact.address}</span>
              </button>
            {/each}
          </div>
        {/if}
      </div>

      {#if showCc}
        <div class="cf" style="position:relative">
          <label for="compose-cc">Cc:</label>
          <div class="recip">
            {#each ccChips as chip, index (index)}
              <span class="chip">
                <span>{chip.name ? `${chip.name} <${chip.address}>` : chip.address}</span>
                <button title="Remove recipient" aria-label="Remove {chip.address}" onclick={() => removeChip('cc', index)}>×</button>
              </span>
            {/each}
            <input
              id="compose-cc"
              type="text"
              spellcheck="false"
              placeholder="name@example.com"
              aria-label="Cc recipients"
              bind:value={ccText}
              oninput={() => onRecipientInput('cc')}
              onblur={() => onRecipientBlur('cc')}
              onkeydown={(event) => onRecipientKeydown(event, 'cc')}
            />
          </div>
          {#if suggestionTarget === 'cc' && suggestions.length}
            <div class="ac-menu" role="listbox" aria-label="Contact suggestions">
              {#each suggestions as contact, index (contact.address)}
                <button
                  class="ac-item"
                  class:active={index === suggestionIndex}
                  role="option"
                  aria-selected={index === suggestionIndex}
                  onmousedown={(event) => event.preventDefault()}
                  onclick={() => pickSuggestion(contact, 'cc')}
                >
                  <span class="ac-name">{contact.name || contact.address}</span>
                  <span class="ac-addr">{contact.address}</span>
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {:else}
        <div class="cf">
          <label for="compose-cc-add">Cc:</label>
          <div class="recip">
            <input
              id="compose-cc-add"
              type="text"
              spellcheck="false"
              placeholder="Add Cc"
              aria-label="Add Cc recipients"
              onfocus={() => (showCc = true)}
              readonly
            />
          </div>
        </div>
      {/if}

      <div class="cf">
        <label for="compose-subject">Subject:</label>
        <input
          id="compose-subject"
          type="text"
          spellcheck="false"
          placeholder="Subject"
          aria-label="Subject"
          bind:value={subject}
          oninput={touch}
        />
      </div>
    </div>

    <div class="compose-body">
      <textarea
        spellcheck="false"
        placeholder="Write your message…"
        aria-label="Message body"
        bind:value={body}
        oninput={touch}
      ></textarea>
    </div>

    {#if attachments.length}
      <div class="attach-row" style="padding:0 15px 8px">
        {#each attachments as file, index (file.path)}
          <span class="chip">
            <span>{file.name} · {formatBytes(file.sizeBytes)}</span>
            <button title="Remove attachment" aria-label="Remove {file.name}" onclick={() => removeAttachment(index)}>×</button>
          </span>
        {/each}
      </div>
    {/if}

    <div class="compose-foot">
      {#if confirmingClose}
        <span class="compose-confirm">Unsent changes will be kept as a draft only after saving.</span>
        <span class="grow"></span>
        <button class="btn" onclick={cancelCloseCompose}>Keep editing</button>
        <button class="btn danger" onclick={discardCompose}>Discard</button>
      {:else}
        <button class="cbtn" onclick={() => void attachFiles()} disabled={picking}>
          <Icon name="clip" />{picking ? 'Attaching…' : 'Attach'}
        </button>
        <button class="cbtn" disabled title="Rich formatting is planned; compose is plain text in v1">Format</button>
        <span class="grow"></span>
        <button class="cbtn" onclick={onSave} disabled={saving}>Save draft</button>
        <button class="cbtn send" onclick={onSend} disabled={saving}><Icon name="sent" />Send</button>
      {/if}
    </div>
  </div>
</div>
