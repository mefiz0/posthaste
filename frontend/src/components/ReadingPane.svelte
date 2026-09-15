<script lang="ts">
  import Icon from "./Icon.svelte";
  import MessageFrame from "./MessageFrame.svelte";
  import { slide } from "svelte/transition";
  import { prefersReducedMotion } from "svelte/motion";
  import {
    app,
    accountIdForMessage,
    actions,
    closeReading,
    markUnread,
    openAttachmentViewer,
    openCompose,
    openMovePalette,
    selectThreadMessage,
    showToast,
    threadPanelVisible,
    toggleStar,
    toggleThreadPanel,
  } from "../lib/stores.svelte";
  import { api } from "../lib/api";
  import { formatBytes, formatDateFull, formatListTime, initials } from "../lib/format";
  import type { IconName } from "../lib/icons";
  import type { Attachment } from "../lib/types";

  /** Attachments shown before the bar collapses behind a "show more" toggle. */
  const ATTACH_LIMIT = 4;

  const message = $derived(app.selectedMessage);
  const hasHtml = $derived(message?.bodyHtml != null && message.bodyHtml.length > 0);

  let activeTab = $state<"html" | "text">("text");
  let allowRemote = $state(false);
  let remoteHtml = $state<string | null>(null);
  let loadingRemote = $state(false);
  let menuOpen = $state(false);
  let attachmentsExpanded = $state(false);
  let threadList = $state<HTMLElement | null>(null);

  const threadVisible = $derived(threadPanelVisible());

  // Reset per-message view state whenever another message is opened.
  $effect(() => {
    void message?.id;
    activeTab = message?.bodyHtml ? "html" : "text";
    allowRemote = false;
    remoteHtml = null;
    loadingRemote = false;
    menuOpen = false;
    attachmentsExpanded = false;
  });

  // Leaving the HTML tab withdraws the opt-in: coming back starts blocked
  // again, per the per-message, per-view nature of the choice.
  $effect(() => {
    if (activeTab === "text") {
      allowRemote = false;
      remoteHtml = null;
    }
  });

  // Keep the open message's row in view inside the conversation panel while
  // navigating the list or the panel itself.
  $effect(() => {
    void app.selectedMessageId;
    threadList?.querySelector(".th-item.active")?.scrollIntoView({ block: "nearest" });
  });

  const remoteBlocked = $derived(hasHtml && !allowRemote && (message?.hasRemoteContent ?? false));
  const isHtmlActive = $derived(hasHtml && activeTab === "html");

  /**
   * The opt-in fetches a remote-inclusive re-sanitization of the message's
   * raw source from the engine; the stored HTML stays remote-stripped. The
   * fetched variant is kept for this message only and dropped on any reset.
   */
  async function loadRemote(): Promise<void> {
    const current = message;
    if (!current || loadingRemote) return;
    if (remoteHtml != null) {
      allowRemote = true;
      return;
    }
    loadingRemote = true;
    try {
      const html = await api.getMessageHTML(accountIdForMessage(current), current.id, true);
      remoteHtml = html;
      allowRemote = true;
    } catch {
      showToast("Could not load remote content", "error");
    } finally {
      loadingRemote = false;
    }
  }

  const fileAttachments = $derived((message?.attachments ?? []).filter((attachment) => !attachment.isInline));
  const shownAttachments = $derived(
    attachmentsExpanded ? fileAttachments : fileAttachments.slice(0, ATTACH_LIMIT),
  );
  const hiddenAttachmentCount = $derived(Math.max(0, fileAttachments.length - ATTACH_LIMIT));

  const cidMap = $derived(
    new Map(
      (message?.attachments ?? [])
        .filter((attachment) => attachment.contentId)
        .map((attachment) => [(attachment.contentId ?? "").toLowerCase(), attachment.id]),
    ),
  );

  async function resolveCid(cid: string): Promise<string | null> {
    const current = message;
    if (!current) return null;
    const attachmentId = cidMap.get(cid.toLowerCase());
    if (attachmentId == null) return null;
    try {
      return await api.getAttachmentDataURL(accountIdForMessage(current), current.id, attachmentId);
    } catch {
      return null;
    }
  }

  function viewAttachment(attachment: Attachment): void {
    openAttachmentViewer(attachment);
  }

  const toLine = $derived.by(() => {
    const current = message;
    if (!current) return "";
    const others = current.toAddresses.filter((address) => address !== current.fromAddress);
    return others.length ? `to ${others.slice(0, 2).join(", ")}${others.length > 2 ? "…" : ""}` : "to me";
  });

  const paragraphs = $derived(
    (message?.bodyText ?? "").split(/\n{2,}/).filter((paragraph) => paragraph.trim().length > 0),
  );

  const menuItems = $derived.by(() => {
    const items: { id: string; label: string; icon: IconName; danger?: boolean; run: () => void }[] = [
      { id: "replyall", label: "Reply all", icon: "reply", run: () => openCompose("replyAll") },
      { id: "unread", label: "Mark unread", icon: "box", run: () => void markUnread() },
      { id: "archive", label: "Archive", icon: "archive", run: () => actions.archive() },
      { id: "move", label: "Move to…", icon: "move", run: () => { menuOpen = false; openMovePalette(); } },
      { id: "delete", label: "Delete", icon: "trash", danger: true, run: () => actions.deleteMessage() },
    ];
    return items;
  });
</script>

<section class="reading-pane" aria-label="Reading pane" data-context="reading">
  {#if message}
    {#key message.id}
      <div class="rp-main">
        <div class="rp-toolbar">
          <button class="back-btn" onclick={closeReading}><Icon name="reply" />Back</button>
          <span class="grow"></span>
          <button onclick={() => openCompose("reply")}><Icon name="reply" />Reply</button>
          <button onclick={() => openCompose("forward")}><Icon name="forward" />Forward</button>
          <button
            onclick={() => void toggleStar(message.id)}
            aria-pressed={message.flags.flagged}
          >
            <Icon name="star" fill={message.flags.flagged} />{message.flags.flagged ? "Starred" : "Star"}
          </button>
          {#if app.threadMessages.length > 1}
            <button onclick={toggleThreadPanel} aria-pressed={threadVisible}>
              <Icon name="thread" />Thread
            </button>
          {/if}
          <span class="anchor">
            <button onclick={() => (menuOpen = !menuOpen)} aria-haspopup="menu" aria-expanded={menuOpen}>
              <Icon name="more" />More
            </button>
            {#if menuOpen}
              <button class="menu-backdrop" aria-label="Close menu" onclick={() => (menuOpen = false)}></button>
              <div class="menu" role="menu" aria-label="More actions">
                {#each menuItems as item (item.id)}
                  <button role="menuitem" class:danger={item.danger} onclick={item.run}>
                    <Icon name={item.icon} />{item.label}
                  </button>
                {/each}
              </div>
            {/if}
          </span>
        </div>

        <div class="rp-head">
          <h1 class="rp-subject">{message.subject}</h1>
          <div class="rp-meta">
            <div class="avatar">{initials(message.fromName)}</div>
            <div>
              <div class="rp-from">{message.fromName}</div>
              <div class="rp-addr">{message.fromName} &lt;{message.fromAddress}&gt;</div>
              <div class="rp-to">{toLine} · {formatDateFull(new Date(message.dateIso))}</div>
            </div>
          </div>

          {#if hasHtml || remoteBlocked}
            <div class="rp-headrow">
              {#if hasHtml}
                <div class="rp-tabs" role="tablist" aria-label="Message body format">
                  <button role="tab" aria-selected={activeTab === "html"} onclick={() => (activeTab = "html")}>HTML</button>
                  <button role="tab" aria-selected={activeTab === "text"} onclick={() => (activeTab = "text")}>Plain text</button>
                </div>
              {/if}
              {#if remoteBlocked}
                <button class="rp-remote-btn" onclick={() => void loadRemote()} disabled={loadingRemote}>
                  <Icon name="moon" />
                  {loadingRemote ? "Loading…" : "Remote content blocked — load it"}
                </button>
              {/if}
            </div>
          {/if}
        </div>

        {#if fileAttachments.length}
          <div class="rp-attachbar" aria-label="Attachments">
            <Icon name="clip" />
            <span class="rp-attachbar-count">
              {fileAttachments.length}
              {fileAttachments.length === 1 ? "attachment" : "attachments"}
            </span>
            <div class="rp-attachbar-list">
              {#each shownAttachments as attachment (attachment.id)}
                <button
                  class="attach-chip"
                  title="Preview {attachment.filename}"
                  onclick={() => viewAttachment(attachment)}
                >
                  <span class="attach-chip-name">{attachment.filename}</span>
                  <span class="attach-chip-size">
                    {attachment.fetchState === "fetched" ? "" : "not downloaded · "}{formatBytes(attachment.sizeBytes)}
                  </span>
                </button>
              {/each}
            </div>
            {#if hiddenAttachmentCount > 0}
              <button class="attach-toggle" onclick={() => (attachmentsExpanded = !attachmentsExpanded)}>
                {attachmentsExpanded ? "Show less" : `+${hiddenAttachmentCount} more`}
              </button>
            {/if}
          </div>
        {/if}

        <div class="rp-content" class:html={isHtmlActive}>
          {#if isHtmlActive}
            <MessageFrame
              html={allowRemote && remoteHtml != null ? remoteHtml : (message.bodyHtml ?? "")}
              {allowRemote}
              {resolveCid}
            />
          {:else}
            <div class="rp-text">
              {#each paragraphs as paragraph, index (index)}
                <p>{paragraph}</p>
              {/each}
            </div>
          {/if}
        </div>
      </div>
    {/key}

    {#if threadVisible}
      <aside
        class="rp-thread"
        aria-label="Conversation"
        transition:slide={{
          axis: "x",
          duration: prefersReducedMotion.current ? 0 : 180,
        }}
      >
        <div class="rp-thread-head">
          <span class="rp-thread-title">Conversation</span>
          <span class="rp-thread-count">{app.threadMessages.length}</span>
          <span class="grow"></span>
          <button
            class="rp-thread-hide"
            title="Hide conversation"
            aria-label="Hide conversation"
            onclick={toggleThreadPanel}
          >
            <Icon name="x" />
          </button>
        </div>
        <div
          class="rp-thread-list"
          role="listbox"
          aria-label="Messages in this conversation"
          bind:this={threadList}
        >
          {#each app.threadMessages as item (item.id)}
            <button
              class="th-item"
              class:active={item.id === app.selectedMessageId}
              class:unread={!item.flags.seen}
              role="option"
              aria-selected={item.id === app.selectedMessageId}
              onclick={() => void selectThreadMessage(item.id)}
            >
              <span class="th-dot"></span>
              <span class="th-body">
                <span class="th-top">
                  <span class="th-from">{item.fromName}</span>
                  <span class="th-time">{formatListTime(new Date(item.dateIso))}</span>
                </span>
                <span class="th-snippet">{item.snippet}</span>
              </span>
              {#if item.hasAttachments}
                <span class="th-clip"><Icon name="clip" /></span>
              {/if}
            </button>
          {/each}
        </div>
      </aside>
    {/if}
  {:else}
    <div class="empty">
      <div class="big">{app.messageLoading ? "Loading…" : "No message selected"}</div>
    </div>
  {/if}
</section>
