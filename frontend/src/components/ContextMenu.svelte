<script lang="ts">
  import Icon from './Icon.svelte';
  import { focusTrap } from '../lib/focus';
  import type { ContextMenuItem } from '../lib/contextmenu';
  import { app, closeContextMenu } from '../lib/stores.svelte';

  let menuElement = $state<HTMLElement | null>(null);
  let activeIndex = $state(0);

  const items = $derived(app.contextMenu.items);

  // Re-seed the highlight each time the menu opens.
  $effect(() => {
    if (!app.contextMenu.open) return;
    activeIndex = items.findIndex((item) => !item.disabled);
    if (activeIndex < 0) activeIndex = 0;
  });

  // Keep the keyboard highlight and the real focus in sync.
  $effect(() => {
    if (!app.contextMenu.open) return;
    const index = activeIndex;
    menuElement
      ?.querySelectorAll<HTMLButtonElement>('.ctx-item')
      [index]?.focus();
  });

  function run(item: ContextMenuItem): void {
    if (item.disabled) return;
    closeContextMenu();
    item.run();
  }

  function nextEnabled(from: number, delta: number): number {
    if (!items.length) return 0;
    let index = from;
    for (let step = 0; step < items.length; step += 1) {
      index = (index + delta + items.length) % items.length;
      if (!items[index]?.disabled) return index;
    }
    return from;
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      closeContextMenu();
      return;
    }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      event.stopPropagation();
      activeIndex = nextEnabled(activeIndex, event.key === 'ArrowDown' ? 1 : -1);
      return;
    }
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      event.stopPropagation();
      const item = items[activeIndex];
      if (item) run(item);
    }
  }
</script>

{#if app.contextMenu.open}
  <div class="ctx-layer" role="presentation">
    <button class="ctx-backdrop" aria-label="Close menu" onclick={closeContextMenu}></button>
    <div
      class="ctx-menu"
      role="menu"
      aria-label="Context actions"
      tabindex="-1"
      use:focusTrap
      bind:this={menuElement}
      onkeydown={onKeydown}
      style="left:{app.contextMenu.x}px; top:{app.contextMenu.y}px"
    >
      {#each items as item, index (item.id)}
        {#if item.separatorBefore && index > 0}
          <div class="ctx-sep" role="separator"></div>
        {/if}
        <button
          class="ctx-item"
          class:active={index === activeIndex}
          class:danger={item.danger}
          role="menuitem"
          aria-disabled={item.disabled}
          disabled={item.disabled}
          onmousemove={() => (activeIndex = index)}
          onclick={() => run(item)}
        >
          {#if item.icon}
            <span class="ctx-ic"><Icon name={item.icon} /></span>
          {/if}
          <span class="ctx-label">{item.label}</span>
          {#if item.hint}
            <span class="ctx-hint">{item.hint}</span>
          {/if}
        </button>
      {/each}
    </div>
  </div>
{/if}
