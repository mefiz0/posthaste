<script lang="ts">
  import Icon from './Icon.svelte';
  import { focusTrap } from '../lib/focus';
  import { app, closePalette, commands, moveToFolder, paletteContext } from '../lib/stores.svelte';
  import { groupBySection, queryCommands, type Command } from '../lib/palette';
  import { overlayFade } from '../lib/transitions';
  import { folderIcon } from '../lib/icons';
  import type { IconName } from '../lib/icons';
  import type { Folder } from '../lib/types';

  let query = $state('');
  let activeIndex = $state(0);
  let listElement = $state<HTMLElement | null>(null);

  const moveMode = $derived(app.paletteMode === 'move');
  const items = $derived.by(() => {
    if (moveMode) {
      const moveCommands: Command[] = app.folders
        .filter((folder) => folder.type !== 'drafts')
        .map((folder) => ({
          id: `folder-${folder.id}`,
          label: folder.name,
          section: 'Move to folder',
          icon: folderIcon(folder.type),
          available: () => true,
          run: () => void moveToFolder(folder.id),
        }));
      return queryCommands(query, paletteContext(), moveCommands);
    }
    return queryCommands(query, paletteContext(), commands);
  });
  const groups = $derived(groupBySection(items));

  // Reset the palette each time it opens.
  $effect(() => {
    void app.paletteOpen;
    query = '';
    activeIndex = 0;
  });

  $effect(() => {
    void activeIndex;
    void query;
    listElement?.querySelector('.p-item.active')?.scrollIntoView({ block: 'nearest' });
  });

  function runItem(item: Command): void {
    closePalette();
    item.run();
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      activeIndex = Math.min(activeIndex + 1, items.length - 1);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      activeIndex = Math.max(activeIndex - 1, 0);
    } else if (event.key === 'Enter') {
      event.preventDefault();
      const item = items[activeIndex];
      if (item) runItem(item);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      closePalette();
    }
  }
</script>

<div class="overlay open" transition:overlayFade role="presentation">
  <button class="backdrop" aria-label="Close command palette" onclick={closePalette}></button>
  <div class="dialog palette" role="dialog" aria-modal="true" aria-label="Command palette" use:focusTrap>
    <div class="palette-input">
      <Icon name="palette" />
      <input
        data-autofocus
        type="text"
        placeholder={moveMode ? 'Move to…' : 'Search commands…'}
        spellcheck="false"
        aria-label="Search commands"
        bind:value={query}
        oninput={() => (activeIndex = 0)}
        onkeydown={onKeydown}
      />
      <kbd>Esc</kbd>
    </div>
    <div class="palette-list" role="listbox" aria-label="Commands" bind:this={listElement}>
      {#if !items.length}
        <div class="palette-empty">No matching commands.</div>
      {/if}
      {#each groups as group, groupIndex (group.section)}
        {#if groupIndex > 0}
          <div class="p-sep" role="presentation"></div>
        {/if}
        <div class="p-section">{group.section}</div>
        {#each group.commands as command (command.id)}
          {@const flatIndex = items.indexOf(command)}
          <button
            class="p-item"
            class:active={flatIndex === activeIndex}
            data-pid={command.id}
            role="option"
            aria-selected={flatIndex === activeIndex}
            onclick={() => runItem(command)}
            onmousemove={() => (activeIndex = flatIndex)}
          >
            <span class="p-ic"><Icon name={command.icon as IconName} /></span>
            <span class="p-label">{command.label}</span>
            {#if command.keyHint}
              <span class="p-key">{command.keyHint}</span>
            {/if}
          </button>
        {/each}
      {/each}
    </div>
  </div>
</div>
