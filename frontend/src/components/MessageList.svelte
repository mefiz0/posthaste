<script lang="ts">
  import {
    app,
    closeSearch,
    loadMoreMessages,
    openCompose,
    setFilter,
    visibleMessages,
  } from "../lib/stores.svelte";
  import type { MessageFilter } from "../lib/types";
  import MessageRow from "./MessageRow.svelte";
  import Icon from "./Icon.svelte";

  let listScroll = $state<HTMLElement | null>(null);

  const messages = $derived(visibleMessages());
  const isEmpty = $derived(messages.length === 0);

  const FILTERS: { id: MessageFilter; label: string }[] = [
    { id: "all", label: "All" },
    { id: "unread", label: "Unread" },
    { id: "starred", label: "Starred" },
    { id: "attachments", label: "Attachments" },
  ];

  const meta = $derived.by(() => {
    if (app.searchQuery.trim() || app.filter !== "all")
      return `${messages.length} of ${app.listTotal}`;
    return app.listTotal ? `${app.listTotal} messages` : "";
  });

  // Keep the selected row in view while navigating with j/k, like the mockup.
  $effect(() => {
    void app.selectedMessageId;
    listScroll?.querySelector(".row.selected")?.scrollIntoView({ block: "nearest" });
  });

  // Load the next page as the list nears the bottom.
  function onScroll(event: Event): void {
    const element = event.currentTarget as HTMLElement;
    if (element.scrollHeight - element.scrollTop - element.clientHeight < 400) {
      void loadMoreMessages();
    }
  }
</script>

<section class="list-pane">
  <div class="list-head">
    <span class="title">{app.view.title}</span>
    <span class="meta">{meta}</span>
    <button
      class="icon-btn"
      title="Compose (C)"
      aria-label="Compose message"
      onclick={() => openCompose("new")}
    >
      <Icon name="edit" />
    </button>
  </div>
  <div class="filter-bar" role="group" aria-label="Filter messages">
    {#each FILTERS as filter (filter.id)}
      <button
        class="filter-chip"
        class:active={app.filter === filter.id}
        aria-pressed={app.filter === filter.id}
        onclick={() => setFilter(filter.id)}
      >
        {filter.label}
      </button>
    {/each}
  </div>
  <div class="search-bar" class:open={app.searchOpen}>
    <Icon name="search" />
    <input
      type="text"
      placeholder="Search mail…  from:  has:attachment  is:unread"
      spellcheck="false"
      aria-label="Search mail"
      bind:value={app.searchQuery}
      onkeydown={(event) => {
        if (event.key === "Escape") {
          event.preventDefault();
          closeSearch();
        }
      }}
    />
    <kbd>Esc</kbd>
  </div>
  <div
    class="list-scroll"
    role="listbox"
    aria-label="Messages"
    bind:this={listScroll}
    onscroll={onScroll}
  >
    {#if isEmpty}
      <div class="empty">
        {#if app.listLoading}
          <div class="big">Loading…</div>
        {:else if app.searchQuery.trim()}
          <div class="big">No messages found.</div>
          <div>Try a different search.</div>
        {:else if app.filter !== "all"}
          <div class="big">No matching messages.</div>
          <div>Try another filter.</div>
        {:else}
          <div class="big">You’re all caught up.</div>
        {/if}
      </div>
    {:else}
      {#each messages as message (message.id)}
        <MessageRow {message} />
      {/each}
      {#if app.listHasMore}
        <div class="list-more">{app.listLoading ? "Loading…" : ""}</div>
      {/if}
    {/if}
  </div>
</section>
