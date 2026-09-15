<script lang="ts">
  import { onMount } from 'svelte';
  import { prefersReducedMotion } from 'svelte/motion';
  import { SPRITE_SVG } from './lib/icons';
  import { app, init, openContextMenu, openPalette, runKeyAction, toggleSidebar } from './lib/stores.svelte';
  import Icon from './components/Icon.svelte';
  import { createDispatcher, formatChordParts, isTypingTarget, type ActionId, type DispatcherContext } from './lib/keys';
  import Sidebar from './components/Sidebar.svelte';
  import MessageList from './components/MessageList.svelte';
  import ReadingPane from './components/ReadingPane.svelte';
  import CommandPalette from './components/CommandPalette.svelte';
  import ComposeDrawer from './components/ComposeDrawer.svelte';
  import ShortcutsOverlay from './components/ShortcutsOverlay.svelte';
  import ContextMenu from './components/ContextMenu.svelte';
  import AttachmentViewer from './components/AttachmentViewer.svelte';
  import SyncPanel from './components/SyncPanel.svelte';
  import Outbox from './components/Outbox.svelte';
  import AccountSetup from './components/AccountSetup.svelte';
  import Settings from './components/Settings.svelte';
  import Toast from './components/Toast.svelte';

  onMount(() => {
    void init();
  });

  // Slightly longer than the 200ms column-width transition it waits for.
  const RAIL_SETTLE_MS = 220;

  // Collapsing reflows the sidebar content only after the column has finished
  // narrowing; switching at the start would snap the icons to the centre of
  // the wide column first. Expanding switches straight away, where the
  // clipping hides the change.
  let railContent = $state(app.sidebarCollapsed);

  $effect(() => {
    if (!app.sidebarCollapsed) {
      railContent = false;
      return;
    }
    const timer = setTimeout(
      () => (railContent = true),
      prefersReducedMotion.current ? 0 : RAIL_SETTLE_MS,
    );
    return () => clearTimeout(timer);
  });

  function overlayContext(): DispatcherContext['overlay'] {
    if (app.contextMenu.open) return 'contextMenu';
    if (app.paletteOpen) return 'palette';
    if (app.compose.open) return 'compose';
    if (app.settingsKeyListening) return 'capture';
    if (app.viewerAttachment) return 'viewer';
    if (app.syncPanelOpen) return 'sync';
    if (app.outboxOpen) return 'sync';
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

  function onWindowContextMenu(event: MouseEvent): void {
    openContextMenu(event);
  }

  const paletteKbd = $derived(formatChordParts({ key: 'k', ctrl: true }));
</script>

<svelte:window onkeydown={onWindowKeydown} oncontextmenu={onWindowContextMenu} />

{@html SPRITE_SVG}

<div
  class="app"
  data-reading={app.readingOpen ? '1' : '0'}
  data-rail={app.sidebarCollapsed ? '1' : '0'}
  data-rail-content={railContent ? '1' : '0'}
>
  <Sidebar />
  <header class="topbar">
    <button
      class="rail-toggle"
      title={app.sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
      aria-label={app.sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
      aria-pressed={app.sidebarCollapsed}
      onclick={() => void toggleSidebar()}
    >
      <Icon name="chev" />
    </button>
    <div class="crumb"><span>{app.view.title}</span></div>
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
{#if app.outboxOpen}
  <Outbox />
{/if}
{#if app.accountSetupOpen}
  <AccountSetup />
{/if}
<ContextMenu />
<Toast />
