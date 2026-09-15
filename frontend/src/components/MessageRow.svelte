<script lang="ts">
  import Icon from './Icon.svelte';
  import { formatListTime } from '../lib/format';
  import type { MessageSummary } from '../lib/types';
  import { app, selectMessage, toggleStar } from '../lib/stores.svelte';

  let { message }: { message: MessageSummary } = $props();

  const selected = $derived(app.selectedMessageId === message.id);
  const starred = $derived(message.flags.flagged);
</script>

<div
  class="row"
  class:unread={!message.flags.seen}
  class:starred
  class:selected
  role="option"
  aria-selected={selected}
  aria-label="{message.fromName}: {message.subject}"
  tabindex="-1"
  data-context="message"
  data-message-id={message.id}
  onclick={() => void selectMessage(message.id, { open: true })}
  onkeydown={(event) => {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      void selectMessage(message.id, { open: true });
    }
  }}
>
  <div class="dotcol"><span class="dot"></span></div>
  <div class="content">
    <div class="row-top">
      <span class="sender">{message.fromName}</span>
      <span class="gap"></span>
      <span class="time">{formatListTime(new Date(message.dateIso))}</span>
      <button
        class="star"
        title={starred ? 'Unstar' : 'Star'}
        aria-label={starred ? 'Unstar message' : 'Star message'}
        aria-pressed={starred}
        onclick={(event) => {
          event.stopPropagation();
          void toggleStar(message.id);
        }}
      >
        <Icon name="star" fill={starred} />
      </button>
    </div>
    <div class="row-subject">
      <span class="subject">{message.subject}</span>
      {#if message.hasAttachments}
        <span class="clip"><Icon name="clip" /></span>
      {/if}
    </div>
    <div class="preview">{message.snippet}</div>
  </div>
</div>
