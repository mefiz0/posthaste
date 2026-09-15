<script lang="ts">
  import {
    app,
    openAccountSetup,
    openSyncPanel,
    selectAccount,
    selectSnoozed,
    selectStarred,
    setFolderId,
    syncAll,
    toggleSidebar,
  } from '../lib/stores.svelte';
  import { folderIcon } from '../lib/icons';
  import type { FolderType } from '../lib/types';
  import type { IconName } from '../lib/icons';
  import Icon from './Icon.svelte';

  interface VirtualFolder {
    id: 'starred' | 'snoozed';
    label: string;
    icon: IconName;
  }

  const VIRTUAL_FOLDERS: VirtualFolder[] = [
    { id: 'starred', label: 'Starred', icon: 'star' },
    { id: 'snoozed', label: 'Snoozed', icon: 'clock' },
  ];

  const SYSTEM_ORDER: FolderType[] = ['inbox', 'drafts', 'sent', 'archive', 'junk', 'trash'];

  const systemFolders = $derived(
    [...app.folders]
      .filter((folder) => folder.type !== 'user')
      .sort(
        (a, b) => SYSTEM_ORDER.indexOf(a.type) - SYSTEM_ORDER.indexOf(b.type) || a.name.localeCompare(b.name),
      ),
  );
  const userFolders = $derived(app.folders.filter((folder) => folder.type === 'user'));

  function isActiveVirtual(id: VirtualFolder['id']): boolean {
    return app.view.kind === id;
  }

  function unreadFor(accountId: number): number {
    return app.folders
      .filter((folder) => folder.accountId === accountId && folder.type !== 'drafts')
      .reduce((total, folder) => total + folder.unreadCount, 0);
  }

  const status = $derived.by(() => {
    if (!app.accounts.length)
      return { text: 'No accounts', tone: 'err' as const, retry: false, detail: '' };
    const errored = app.accounts.find(
      (account) => app.syncStates[account.id]?.state === 'error',
    );
    if (errored)
      return {
        text: `Account issue — ${errored.email}`,
        tone: 'err' as const,
        retry: true,
        detail: app.syncStates[errored.id]?.detail ?? '',
      };
    const states = Object.values(app.syncStates);
    if (states.some((state) => state.state === 'syncing'))
      return { text: 'Syncing…', tone: 'busy' as const, retry: false, detail: '' };
    const offline = app.accounts.find(
      (account) => app.syncStates[account.id]?.state === 'offline',
    );
    if (offline)
      return {
        text: 'Offline — retrying',
        tone: 'warn' as const,
        retry: true,
        detail: app.syncStates[offline.id]?.detail ?? '',
      };
    const paused = app.accounts.filter((account) => account.paused);
    if (paused.length === app.accounts.length)
      return { text: 'Accounts paused', tone: 'warn' as const, retry: false, detail: '' };
    if (paused.length)
      return { text: `${paused.length} paused · synced`, tone: 'warn' as const, retry: false, detail: '' };
    return { text: 'All accounts synced', tone: 'ok' as const, retry: false, detail: '' };
  });
</script>

<aside class="sidebar">
  <div class="sb-head">
    <div class="sb-logo"></div>
    <div class="wordmark">Posthaste</div>
    <button
      class="rail-toggle"
      title={app.sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
      aria-label={app.sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
      onclick={() => void toggleSidebar()}
    >
      <Icon name="chev" />
    </button>
  </div>

  <nav class="sb-nav" aria-label="Mailboxes">
    {#each VIRTUAL_FOLDERS as virtual (virtual.id)}
      <button
        class="nav-item"
        class:active={isActiveVirtual(virtual.id)}
        onclick={() => (virtual.id === 'starred' ? void selectStarred() : void selectSnoozed())}
      >
        <Icon name={virtual.icon} />
        <span class="label">{virtual.label}</span>
      </button>
    {/each}

    {#each systemFolders as folder (folder.id)}
      <button
        class="nav-item"
        class:active={app.view.kind === 'folder' && app.view.folderId === folder.id}
        class:unread-count={folder.type === 'inbox' && folder.unreadCount > 0}
        data-folder={folder.type}
        data-context="folder"
        data-folder-id={folder.id}
        onclick={() => void setFolderId(folder.id)}
      >
        <Icon name={folderIcon(folder.type)} />
        <span class="label">{folder.name}</span>
        {#if folder.type === 'inbox' && folder.unreadCount > 0}
          <span class="count">{folder.unreadCount}</span>
        {:else if folder.type === 'drafts' && folder.totalCount > 0}
          <span class="count">{folder.totalCount}</span>
        {/if}
      </button>
    {/each}

    {#if userFolders.length}
      <div class="sb-section">Folders</div>
      {#each userFolders as folder (folder.id)}
        <button
          class="nav-item nav-sub"
          class:active={app.view.kind === 'folder' && app.view.folderId === folder.id}
          data-context="folder"
          data-folder-id={folder.id}
          onclick={() => void setFolderId(folder.id)}
        >
          <span class="nav-iconbox"><Icon name="folder" /></span>
          <span class="label">{folder.name}</span>
        </button>
      {/each}
    {/if}

    <div class="sb-section">Accounts</div>
    {#each app.accounts as account (account.id)}
      <button
        class="nav-item"
        class:active={app.selectedAccountId === account.id}
        title={account.paused ? 'Paused' : account.email}
        onclick={() => void selectAccount(account.id)}
      >
        <span class="acct-dot" style="background:{account.color ?? 'var(--muted)'}"></span>
        <span class="label">{account.email}</span>
        {#if unreadFor(account.id) > 0}
          <span class="count">{unreadFor(account.id)}</span>
        {/if}
      </button>
    {/each}
    <button class="nav-item" onclick={openAccountSetup}>
      <span class="nav-iconbox"><Icon name="user" /></span>
      <span class="label">Add account</span>
    </button>
  </nav>

  <div
    class="sb-foot"
    class:busy={status.tone === 'busy'}
    class:warn={status.tone === 'warn'}
    class:err={status.tone === 'err'}
  >
    <button
      class="sb-status"
      title="Show sync activity"
      aria-label="Show sync activity"
      onclick={openSyncPanel}
    >
      <span class="status-dot"></span>
      <span class="sb-status-text" title={status.detail || status.text}>{status.text}</span>
    </button>
    {#if status.retry}
      <button class="sb-retry" onclick={() => void syncAll(true)}>Retry</button>
    {/if}
  </div>
</aside>
