<script lang="ts">
  import { onMount } from 'svelte';
  import { SPRITE_SVG } from './lib/icons';
  import { app, init, openPalette, runKeyAction } from './lib/stores.svelte';
  import { createDispatcher, formatChordParts, isTypingTarget, type ActionId, type DispatcherContext } from './lib/keys';
  import Sidebar from './components/Sidebar.svelte';
  import MessageList from './components/MessageList.svelte';
  import ReadingPane from './components/ReadingPane.svelte';
  import CommandPalette from './components/CommandPalette.svelte';
  import ComposeDrawer from './components/ComposeDrawer.svelte';
  import ShortcutsOverlay from './components/ShortcutsOverlay.svelte';
  import AttachmentViewer from './components/AttachmentViewer.svelte';
  import SyncPanel from './components/SyncPanel.svelte';
  import AccountSetup from './components/AccountSetup.svelte';
  import Settings from './components/Settings.svelte';
  import Toast from './components/Toast.svelte';

  onMount(() => {
    void init();
  });

  function overlayContext(): DispatcherContext['overlay'] {
    if (app.paletteOpen) return 'palette';
    if (app.compose.open) return 'compose';
    if (app.settingsKeyListening) return 'capture';
    if (app.viewerAttachment) return 'viewer';
    if (app.syncPanelOpen) return 'sync';
    if (app.shortcutsOpen) return 'shortcuts';
    if (app.settingsOpen) return 'settings';
    if (app.accountSetupOpen) return 'setup';
    return 'none';
  }

  function dispatcherRun(action: ActionId): void {
    if (action === 'scrollPage') {
      const scroller = document.querySelector<HTMLElement>('.rp-content');
      if (scroller) scroller.scrollBy({ top: scroller.clientHeight * 0.85, behavior: 'smooth' });
      return;
    }
    runKeyAction(action);
  }

  const keyContext = { typing: false };

  const dispatcher = createDispatcher(
    dispatcherRun,
    () => ({
      overlay: overlayContext(),
      typing: keyContext.typing,
      readingOpen: app.readingOpen,
      searchOpen: app.searchOpen,
    }),
    // User keymap overrides; refreshed whenever the settings store updates.
    () => app.settings?.keymap,
  );

  function onWindowKeydown(event: KeyboardEvent): void {
    keyContext.typing = isTypingTarget(event.target);
    dispatcher.handleKeydown(event);
  }

  const paletteKbd = $derived(formatChordParts({ key: 'k', ctrl: true }));
</script>

<svelte:window onkeydown={onWindowKeydown} />

{@html SPRITE_SVG}

<div class="app" data-reading={app.readingOpen ? '1' : '0'} data-rail={app.sidebarCollapsed ? '1' : '0'}>
  <Sidebar />
  <header class="topbar">
    <div class="crumb"><span class="mark"></span><span>{app.view.title}</span></div>
    <div class="spacer"></div>
    <button class="search-trigger" title="Search mail" onclick={() => openPalette('commands')}>
      <svg class="ic"><use href="#i-search" /></svg>
      <span>Search…</span>
      <kbd>{paletteKbd.join('')}</kbd>
    </button>
  </header>

  <div class="workspace">
    <MessageList />
    <ReadingPane />
  </div>
</div>

{#if app.paletteOpen}
  <CommandPalette />
{/if}
{#if app.compose.open}
  <ComposeDrawer />
{/if}
{#if app.shortcutsOpen}
  <ShortcutsOverlay />
{/if}
{#if app.settingsOpen}
  <Settings />
{/if}
{#if app.viewerAttachment}
  <AttachmentViewer />
{/if}
{#if app.syncPanelOpen}
  <SyncPanel />
{/if}
{#if app.accountSetupOpen}
  <AccountSetup />
{/if}
<Toast />
