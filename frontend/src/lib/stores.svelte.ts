import { api } from "./api";
import {
  buildCommands,
  type Command,
  type CommandActions,
  type PaletteContext,
} from "./palette";
import {
  clampMenuPosition,
  detectContextMenuSubject,
  type ContextMenuItem,
  type ContextMenuSubject,
} from "./contextmenu";
import { folderIcon } from "./icons";
import type { ActionId } from "./keys";
import type {
  Account,
  AccountSyncState,
  AppSettings,
  Attachment,
  BackendEvent,
  ComposeMode,
  DraftInput,
  Folder,
  FolderType,
  ManualAccountInput,
  MessageDetail,
  MessageFilter,
  MessageSummary,
  SyncStateKind,
} from "./types";

export interface CurrentView {
  kind: "folder" | "starred" | "snoozed";
  folderId: number | null;
  title: string;
}

export interface ToastItem {
  id: number;
  message: string;
  level: "info" | "error";
}

/** One line in the sync activity log shown by the sync panel. */
export interface SyncLogEntry {
  id: number;
  /** Display time, e.g. 14:32:07. */
  time: string;
  text: string;
  level: "info" | "success" | "warn" | "error";
}

const SYNC_LOG_LIMIT = 300;

interface ComposeState {
  open: boolean;
  mode: ComposeMode;
  /** Increments on every open so the drawer can re-seed its fields. */
  token: number;
  draftId: string | null;
  dirty: boolean;
  saving: boolean;
  confirmingClose: boolean;
}

const freshCompose = (): ComposeState => ({
  open: false,
  mode: "new",
  token: 0,
  draftId: null,
  dirty: false,
  saving: false,
  confirmingClose: false,
});

/**
 * Shared app state. One exported $state object mutated only through the
 * action functions below; components stay presentational.
 */
export const app = $state({
  ready: false,
  accounts: [] as Account[],
  folders: [] as Folder[],
  selectedAccountId: null as number | null,
  view: { kind: "folder", folderId: null, title: "Inbox" } as CurrentView,
  messages: [] as MessageSummary[],
  listTotal: 0,
  listLoading: false,
  listPage: 0,
  listHasMore: false,
  filter: "all" as MessageFilter,
  searchOpen: false,
  searchQuery: "",
  selectedMessageId: null as number | null,
  selectedMessage: null as MessageDetail | null,
  messageLoading: false,
  readingOpen: false,
  threadId: null as number | null,
  threadMessages: [] as MessageSummary[],
  /** null = auto: the panel opens when the conversation has several messages. */
  threadOpen: null as boolean | null,
  viewerAttachment: null as Attachment | null,
  sidebarCollapsed: false,
  paletteOpen: false,
  paletteMode: "commands" as "commands" | "move",
  shortcutsOpen: false,
  settingsOpen: false,
  settingsKeyListening: null as string | null,
  accountSetupOpen: false,
  syncPanelOpen: false,
  compose: freshCompose(),
  toasts: [] as ToastItem[],
  syncLog: [] as SyncLogEntry[],
  syncStates: {} as Record<number, AccountSyncState>,
  settings: null as AppSettings | null,
  contextMenu: {
    open: false,
    x: 0,
    y: 0,
    items: [] as ContextMenuItem[],
  },
});

// ---------- derived helpers (plain functions: reactivity flows from reads) ----------

export function currentFolder(): Folder | null {
  return app.folders.find((folder) => folder.id === app.view.folderId) ?? null;
}

export function visibleMessages(): MessageSummary[] {
  let list = app.messages;
  switch (app.filter) {
    case "unread":
      list = list.filter((message) => !message.flags.seen);
      break;
    case "starred":
      list = list.filter((message) => message.flags.flagged);
      break;
    case "attachments":
      list = list.filter((message) => message.hasAttachments);
      break;
  }
  const query = app.searchQuery.trim().toLowerCase();
  if (!query) return list;
  return list.filter((message) =>
    `${message.fromName} ${message.fromAddress} ${message.subject} ${message.snippet}`
      .toLowerCase()
      .includes(query),
  );
}

/** Applies a quick filter to the current list without changing the folder. */
export function setFilter(filter: MessageFilter): void {
  app.filter = filter;
}

export function accountIdForFolderId(folderId: number | null): number | null {
  if (folderId == null) return app.selectedAccountId;
  return (
    app.folders.find((folder) => folder.id === folderId)?.accountId ??
    app.selectedAccountId
  );
}

export function accountIdForMessage(message: MessageSummary): number {
  return accountIdForFolderId(message.folderId) ?? 0;
}

export function defaultAccount(): Account | null {
  return (
    app.accounts.find((account) => account.isDefault) ?? app.accounts[0] ?? null
  );
}

export function paletteContext(): PaletteContext {
  return {
    hasSelection: app.selectedMessageId != null,
    readingOpen: app.readingOpen,
    viewTitle: app.view.title,
    viewKind: app.view.kind,
    composeOpen: app.compose.open,
    hasThread: app.threadMessages.length > 1,
  };
}

// ---------- toast ----------

let toastSequence = 1;

export function showToast(
  message: string,
  level: "info" | "error" = "info",
): void {
  const id = toastSequence++;
  app.toasts.push({ id, message, level });
  setTimeout(() => {
    const index = app.toasts.findIndex((toast) => toast.id === id);
    if (index !== -1) app.toasts.splice(index, 1);
  }, 1800);
}

// ---------- sync activity log ----------

let syncLogSequence = 1;

/** Formats a timestamp as local HH:MM:SS for a log line. */
function formatLogTime(date: Date): string {
  return `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}:${String(date.getSeconds()).padStart(2, "0")}`;
}

/** Appends one line to the bounded sync activity log. */
function pushSyncLog(
  text: string,
  level: SyncLogEntry["level"] = "info",
): void {
  app.syncLog.push({
    id: syncLogSequence++,
    time: formatLogTime(new Date()),
    text,
    level,
  });
  if (app.syncLog.length > SYNC_LOG_LIMIT) {
    app.syncLog.splice(0, app.syncLog.length - SYNC_LOG_LIMIT);
  }
}

/**
 * Loads the engine's retained sync activity, replacing the local log. Called at
 * start and whenever the panel opens, so activity from before the UI
 * subscribed is still visible.
 */
export async function refreshSyncActivity(): Promise<void> {
  try {
    const activity = await api.getSyncActivity();
    app.syncLog = activity.map((entry) => ({
      id: syncLogSequence++,
      time: formatLogTime(new Date(entry.at)),
      text: `${accountLabel(entry.accountId)} — ${entry.text}`,
      level: entry.level,
    }));
  } catch {
    // Keep whatever is already displayed on a transient failure.
  }
}

/** The account's email for a log line, falling back to its internal id. */
function accountLabel(accountId: number): string {
  return (
    app.accounts.find((account) => account.id === accountId)?.email ??
    `account ${accountId}`
  );
}

// ---------- data loading ----------

export async function refreshAccounts(): Promise<void> {
  app.accounts = await api.listAccounts();
}

export async function refreshFolders(): Promise<void> {
  app.folders = await api.listFolders(app.selectedAccountId);
}

/** Matches the engine's message-list page size. */
const LIST_PAGE_SIZE = 200;

/** The list query for the current view, shared by the first page and paging. */
function listRequest(): {
  accountId: number | null;
  request: { special?: "starred"; folderId?: number };
} {
  return {
    accountId: accountIdForFolderId(app.view.folderId),
    request:
      app.view.kind === "starred"
        ? { special: "starred" as const }
        : { folderId: app.view.folderId ?? undefined },
  };
}

export async function refreshMessages(): Promise<void> {
  if (app.view.kind === "snoozed") {
    app.messages = [];
    app.listTotal = 0;
    app.listHasMore = false;
    return;
  }
  app.listLoading = true;
  app.listPage = 0;
  try {
    const { accountId, request } = listRequest();
    const messages = await api.listMessages(accountId, request, 0);
    app.messages = messages;
    app.listHasMore = messages.length === LIST_PAGE_SIZE;
    app.listTotal =
      app.view.kind === "starred"
        ? messages.length
        : (currentFolder()?.totalCount ?? messages.length);
    if (
      app.selectedMessageId != null &&
      !messages.some((message) => message.id === app.selectedMessageId)
    ) {
      const next = messages[0] ?? null;
      app.selectedMessageId = next?.id ?? null;
      app.selectedMessage = null;
      app.readingOpen = next != null;
      if (next) void loadMessage(next);
    }
  } finally {
    app.listLoading = false;
  }
}

/**
 * Appends the next page of the current view. Called by the list as it nears
 * the bottom so a large folder keeps loading instead of stopping at one page.
 */
export async function loadMoreMessages(): Promise<void> {
  if (app.listLoading || !app.listHasMore || app.view.kind === "snoozed")
    return;
  app.listLoading = true;
  const nextPage = app.listPage + 1;
  try {
    const { accountId, request } = listRequest();
    const messages = await api.listMessages(accountId, request, nextPage);
    const known = new Set(app.messages.map((message) => message.id));
    for (const message of messages) {
      if (!known.has(message.id)) app.messages.push(message);
    }
    app.listPage = nextPage;
    app.listHasMore = messages.length === LIST_PAGE_SIZE;
  } catch {
    app.listHasMore = false;
  } finally {
    app.listLoading = false;
  }
}

async function loadMessage(message: MessageSummary): Promise<void> {
  app.messageLoading = true;
  try {
    const accountId = accountIdForMessage(message);
    const detail = await api.getMessage(accountId, message.id);
    if (app.selectedMessageId === message.id) {
      app.selectedMessage = detail;
      void loadThreadMessages(accountId, detail.threadId);
    }
  } catch {
    showToast("Could not load message", "error");
  } finally {
    app.messageLoading = false;
  }
}

/**
 * Loads the open conversation for the thread panel. Summaries already known
 * from the list or the previous load are reused, so flag changes made in
 * either place stay visible in both.
 */
async function loadThreadMessages(
  accountId: number,
  threadId: number,
): Promise<void> {
  if (app.threadId !== threadId) {
    app.threadId = threadId;
    app.threadMessages = [];
    app.threadOpen = null;
  }
  try {
    const messages = await api.getThread(accountId, threadId);
    if (app.threadId !== threadId) return;
    app.threadMessages = messages.map(
      (message) => messageSummaryFor(message.id) ?? message,
    );
  } catch {
    if (app.threadId === threadId) app.threadMessages = [];
  }
}

/** Whether the reading pane shows the conversation panel beside the message. */
export function threadPanelVisible(): boolean {
  if (!app.threadMessages.length) return false;
  return app.threadOpen ?? app.threadMessages.length > 1;
}

export function toggleThreadPanel(): void {
  app.threadOpen = !threadPanelVisible();
}

/** Opens another message of the current conversation in the reading pane. */
export async function selectThreadMessage(messageId: number): Promise<void> {
  if (messageId === app.selectedMessageId) return;
  await selectMessage(messageId, { open: true });
}

async function applyView(view: CurrentView): Promise<void> {
  app.view = view;
  app.searchOpen = false;
  app.searchQuery = "";
  app.filter = "all";
  await refreshMessages();
  const first = app.messages[0] ?? null;
  app.selectedMessageId = first?.id ?? null;
  app.readingOpen = first != null;
  if (first) await selectMessage(first.id);
}

export async function setFolderId(folderId: number): Promise<void> {
  const folder = app.folders.find((candidate) => candidate.id === folderId);
  if (!folder) return;
  await applyView({ kind: "folder", folderId, title: folder.name });
}

async function gotoFolderType(type: FolderType): Promise<void> {
  const folder =
    app.folders.find(
      (candidate) =>
        candidate.type === type &&
        candidate.accountId === app.selectedAccountId,
    ) ?? app.folders.find((candidate) => candidate.type === type);
  if (!folder) return;
  await setFolderId(folder.id);
}

export async function selectStarred(): Promise<void> {
  await applyView({ kind: "starred", folderId: null, title: "Starred" });
}

export async function selectSnoozed(): Promise<void> {
  await applyView({ kind: "snoozed", folderId: null, title: "Snoozed" });
}

export async function selectAccount(accountId: number): Promise<void> {
  app.selectedAccountId =
    app.selectedAccountId === accountId ? null : accountId;
  await refreshFolders();
  if (app.view.kind === "folder") {
    const current = currentFolder();
    if (!current) {
      await gotoFolderType("inbox");
      return;
    }
  }
  await refreshMessages();
}

// ---------- message actions ----------

/**
 * Finds a summary for an id across the list, the open conversation, and the
 * open detail. The conversation fallback is what lets a reply stored in
 * another folder be opened from the thread panel.
 */
function messageSummaryFor(messageId: number): MessageSummary | null {
  return (
    app.messages.find((message) => message.id === messageId) ??
    app.threadMessages.find((message) => message.id === messageId) ??
    (app.selectedMessage?.id === messageId ? app.selectedMessage : null)
  );
}

export async function selectMessage(
  messageId: number,
  options: { open?: boolean; markRead?: boolean } = {},
): Promise<void> {
  app.selectedMessageId = messageId;
  app.viewerAttachment = null;
  if (options.open) app.readingOpen = true;
  const summary = messageSummaryFor(messageId);
  if (
    options.markRead !== false &&
    summary &&
    !summary.flags.seen &&
    !summary.flags.draft
  ) {
    summary.flags.seen = true;
    const folder = currentFolder();
    if (folder && folder.id === summary.folderId && folder.unreadCount > 0)
      folder.unreadCount -= 1;
    const accountId = accountIdForMessage(summary);
    void api.setFlags(accountId, messageId, { seen: true }).catch(() => {
      summary.flags.seen = false;
      showToast("Could not update message", "error");
    });
  }
  if (summary) await loadMessage(summary);
}

export function moveSelection(delta: number): void {
  const messages = visibleMessages();
  if (!messages.length) return;
  const index = messages.findIndex(
    (message) => message.id === app.selectedMessageId,
  );
  let next = index === -1 ? 0 : index + delta;
  if (next < 0) next = 0;
  if (next > messages.length - 1) next = messages.length - 1;
  const target = messages[next];
  if (target) void selectMessage(target.id);
}

export function closeReading(): void {
  app.readingOpen = false;
}

export async function toggleStar(messageId?: number): Promise<void> {
  const id = messageId ?? app.selectedMessageId;
  if (id == null) return;
  const summary = messageSummaryFor(id);
  if (!summary) return;
  const flagged = !summary.flags.flagged;
  summary.flags.flagged = flagged;
  const accountId = accountIdForMessage(summary);
  try {
    await api.setFlags(accountId, id, { flagged });
  } catch {
    summary.flags.flagged = !flagged;
    showToast("Could not update message", "error");
  }
}

export async function markUnread(messageId?: number): Promise<void> {
  const id = messageId ?? app.selectedMessageId;
  if (id == null) return;
  const summary = messageSummaryFor(id);
  if (!summary) return;
  summary.flags.seen = false;
  const folder = app.folders.find(
    (candidate) => candidate.id === summary.folderId,
  );
  if (folder) folder.unreadCount += 1;
  try {
    await api.setFlags(accountIdForMessage(summary), id, { seen: false });
  } catch {
    summary.flags.seen = true;
    showToast("Could not update message", "error");
  }
}

export async function markRead(messageId?: number): Promise<void> {
  const id = messageId ?? app.selectedMessageId;
  if (id == null) return;
  const summary = messageSummaryFor(id);
  if (!summary) return;
  summary.flags.seen = true;
  const folder = app.folders.find(
    (candidate) => candidate.id === summary.folderId,
  );
  if (folder && folder.unreadCount > 0) folder.unreadCount -= 1;
  try {
    await api.setFlags(accountIdForMessage(summary), id, { seen: true });
  } catch {
    summary.flags.seen = false;
    showToast("Could not update message", "error");
  }
}

async function removeCurrent(kind: "archive" | "delete"): Promise<void> {
  const id = app.selectedMessageId;
  if (id == null) return;
  const summary = app.messages.find((message) => message.id === id);
  const accountId = summary
    ? accountIdForMessage(summary)
    : (accountIdForFolderId(app.view.folderId) ?? 0);
  const remaining = visibleMessages().filter((message) => message.id !== id);
  app.messages = app.messages.filter((message) => message.id !== id);
  app.listTotal = Math.max(0, app.listTotal - 1);
  const next = remaining[0] ?? null;
  app.selectedMessageId = next?.id ?? null;
  app.selectedMessage = null;
  app.readingOpen = next != null;
  try {
    if (kind === "archive") {
      await api.archiveMessage(accountId, id);
      showToast("Archived");
    } else {
      await api.deleteMessage(accountId, id);
      showToast("Deleted");
    }
    await refreshFolders();
    if (next) await selectMessage(next.id);
  } catch {
    showToast(
      kind === "archive"
        ? "Could not archive message"
        : "Could not delete message",
      "error",
    );
    await refreshMessages();
  }
}

export async function markAllRead(): Promise<void> {
  if (app.view.kind !== "folder" || app.view.folderId == null) return;
  const accountId = accountIdForFolderId(app.view.folderId) ?? 0;
  for (const message of app.messages) message.flags.seen = true;
  const folder = currentFolder();
  if (folder) folder.unreadCount = 0;
  try {
    await api.markAllRead(accountId, app.view.folderId);
    showToast("Marked all as read");
  } catch {
    showToast("Could not mark messages read", "error");
  }
}

export async function moveToFolder(targetFolderId: number): Promise<void> {
  const id = app.selectedMessageId;
  if (id == null) return;
  const summary = app.messages.find((message) => message.id === id);
  const accountId = summary
    ? accountIdForMessage(summary)
    : (accountIdForFolderId(app.view.folderId) ?? 0);
  const target = app.folders.find((folder) => folder.id === targetFolderId);
  const remaining = visibleMessages().filter((message) => message.id !== id);
  app.messages = app.messages.filter((message) => message.id !== id);
  const next = remaining[0] ?? null;
  app.selectedMessageId = next?.id ?? null;
  app.selectedMessage = null;
  app.readingOpen = next != null;
  try {
    await api.moveMessage(accountId, id, targetFolderId);
    showToast(target ? `Moved to ${target.name}` : "Moved");
    await refreshFolders();
    if (next) await selectMessage(next.id);
  } catch {
    showToast("Could not move message", "error");
    await refreshMessages();
  }
}

// ---------- contextual menu ----------

/**
 * Opens the menu for whatever the pointer landed on. Always swallows the event
 * so the webview's own menu never appears; the shell suppresses it natively
 * too, this keeps the browser mock and any frame-level fallback consistent.
 */
export function openContextMenu(event: MouseEvent): void {
  event.preventDefault();
  const items = contextMenuItems(detectContextMenuSubject(event.target));
  if (!items.length) {
    closeContextMenu();
    return;
  }
  const position = clampMenuPosition(
    event.clientX,
    event.clientY,
    items.length,
  );
  app.contextMenu = { open: true, x: position.x, y: position.y, items };
}

export function closeContextMenu(): void {
  if (!app.contextMenu.open) return;
  app.contextMenu = { open: false, x: 0, y: 0, items: [] };
}

function contextMenuItems(subject: ContextMenuSubject): ContextMenuItem[] {
  switch (subject.kind) {
    case "editing":
      return editingMenuItems(subject.element);
    case "message":
      // Right-clicking a row selects it (without opening or marking it read)
      // so the same selection-based actions serve both the row and reading pane.
      if (app.selectedMessageId !== subject.messageId) {
        void selectMessage(subject.messageId, { markRead: false });
      }
      return messageMenuItems();
    case "reading":
      return app.selectedMessageId == null
        ? appMenuItems()
        : messageMenuItems();
    case "folder":
      return folderMenuItems(subject.folderId);
    case "app":
      return appMenuItems();
  }
}

function messageMenuItems(): ContextMenuItem[] {
  const id = app.selectedMessageId;
  if (id == null) return appMenuItems();
  const summary = app.messages.find((message) => message.id === id);
  const flagged = summary?.flags.flagged ?? false;
  const seen = summary?.flags.seen ?? true;
  return [
    {
      id: "reply",
      label: "Reply",
      icon: "reply",
      hint: "R",
      run: () => withLoadedMessage(() => openCompose("reply")),
    },
    {
      id: "replyall",
      label: "Reply all",
      icon: "reply",
      hint: "A",
      run: () => withLoadedMessage(() => openCompose("replyAll")),
    },
    {
      id: "forward",
      label: "Forward",
      icon: "forward",
      hint: "F",
      run: () => withLoadedMessage(() => openCompose("forward")),
    },
    {
      id: "star",
      label: flagged ? "Unstar" : "Star",
      icon: "star",
      hint: "S",
      separatorBefore: true,
      run: () => void toggleStar(id),
    },
    seen
      ? {
          id: "unread",
          label: "Mark unread",
          icon: "box",
          hint: "U",
          run: () => void markUnread(id),
        }
      : {
          id: "read",
          label: "Mark read",
          icon: "check",
          run: () => void markRead(id),
        },
    {
      id: "move",
      label: "Move to…",
      icon: "move",
      hint: "M",
      separatorBefore: true,
      run: openMovePalette,
    },
    {
      id: "archive",
      label: "Archive",
      icon: "archive",
      hint: "E",
      run: () => void removeCurrent("archive"),
    },
    {
      id: "delete",
      label: "Delete",
      icon: "trash",
      hint: "#",
      danger: true,
      run: () => void removeCurrent("delete"),
    },
  ];
}

function folderMenuItems(folderId: number): ContextMenuItem[] {
  const folder = app.folders.find((candidate) => candidate.id === folderId);
  if (!folder) return appMenuItems();
  return [
    {
      id: "open",
      label: "Open",
      icon: folderIcon(folder.type),
      run: () => void setFolderId(folderId),
    },
    {
      id: "markallread",
      label: "Mark all as read",
      icon: "checkall",
      run: () => void markFolderRead(folderId),
    },
    {
      id: "sync",
      label: "Sync now",
      icon: "refresh",
      separatorBefore: true,
      run: () => void syncAll(true),
    },
  ];
}

function editingMenuItems(
  element: HTMLInputElement | HTMLTextAreaElement,
): ContextMenuItem[] {
  const hasSelection =
    (element.selectionStart ?? 0) !== (element.selectionEnd ?? 0);
  return [
    {
      id: "cut",
      label: "Cut",
      disabled: !hasSelection,
      run: () => clipboardEdit("cut", element),
    },
    {
      id: "copy",
      label: "Copy",
      disabled: !hasSelection,
      run: () => clipboardEdit("copy", element),
    },
    {
      id: "paste",
      label: "Paste",
      run: () => clipboardEdit("paste", element),
    },
    {
      id: "selectall",
      label: "Select all",
      separatorBefore: true,
      run: () => element.select(),
    },
  ];
}

function appMenuItems(): ContextMenuItem[] {
  const selection = window.getSelection()?.toString() ?? "";
  const items: ContextMenuItem[] = [];
  if (selection) {
    items.push({
      id: "copy",
      label: "Copy",
      run: () => void navigator.clipboard?.writeText(selection),
    });
  }
  items.push({
    id: "refresh",
    label: "Refresh",
    icon: "refresh",
    separatorBefore: items.length > 0,
    run: () => void syncAll(true),
  });
  items.push({
    id: "settings",
    label: "Open Settings",
    icon: "settings",
    run: openSettings,
  });
  return items;
}

/**
 * Runs an action that needs the full message detail (reply, forward). The
 * message was already selected on right-click, so this only waits for the
 * detail load when the click beats it.
 */
function withLoadedMessage(run: () => void): void {
  const id = app.selectedMessageId;
  if (id == null) return;
  if (app.selectedMessage?.id === id) {
    run();
    return;
  }
  void selectMessage(id, { markRead: false }).then(() => {
    if (app.selectedMessageId === id) run();
  });
}

/** Applies the edit command to a focused field, inserting pasted text at the caret. */
function clipboardEdit(
  command: "cut" | "copy" | "paste",
  element: HTMLInputElement | HTMLTextAreaElement,
): void {
  element.focus();
  if (command !== "paste") {
    document.execCommand(command);
    return;
  }
  const clipboard = navigator.clipboard;
  if (!clipboard?.readText) return;
  void clipboard.readText().then((text) => {
    const start = element.selectionStart ?? element.value.length;
    const end = element.selectionEnd ?? start;
    element.setRangeText(text, start, end, "end");
    element.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

/** Marks every message in a folder read, even when it is not the active view. */
async function markFolderRead(folderId: number): Promise<void> {
  const accountId = accountIdForFolderId(folderId);
  if (accountId == null) return;
  const folder = app.folders.find((candidate) => candidate.id === folderId);
  if (folder) folder.unreadCount = 0;
  if (app.view.folderId === folderId) {
    for (const message of app.messages) message.flags.seen = true;
  }
  try {
    await api.markAllRead(accountId, folderId);
  } catch {
    await refreshFolders();
  }
}

// ---------- search bar ----------

export function openSearch(): void {
  app.searchOpen = true;
}

export function closeSearch(): void {
  app.searchOpen = false;
  app.searchQuery = "";
}

// ---------- overlays ----------

export function openPalette(mode: "commands" | "move" = "commands"): void {
  app.paletteMode = mode;
  app.paletteOpen = true;
}

export function closePalette(): void {
  app.paletteOpen = false;
  app.paletteMode = "commands";
}

export function openMovePalette(): void {
  if (app.selectedMessageId == null) return;
  openPalette("move");
}

// ---------- attachment viewer ----------

/** Opens the in-app viewer for one attachment of the selected message. */
export function openAttachmentViewer(attachment: Attachment): void {
  app.viewerAttachment = attachment;
}

export function closeAttachmentViewer(): void {
  app.viewerAttachment = null;
}

export function toggleShortcuts(): void {
  app.shortcutsOpen = !app.shortcutsOpen;
}

export function openSettings(): void {
  app.settingsOpen = true;
}

export function closeSettings(): void {
  app.settingsOpen = false;
  app.settingsKeyListening = null;
}

export function openAccountSetup(): void {
  app.accountSetupOpen = true;
}

export function closeAccountSetup(): void {
  app.accountSetupOpen = false;
}

export function openSyncPanel(): void {
  app.syncPanelOpen = true;
  void refreshSyncActivity();
}

export function closeSyncPanel(): void {
  app.syncPanelOpen = false;
}

export function toggleSidebar(): void {
  app.sidebarCollapsed = !app.sidebarCollapsed;
}

export function syncAll(explicit = false): Promise<void> {
  if (explicit) showToast("Refreshing…");
  return api.syncNow(app.selectedAccountId ?? undefined).catch(() => {
    showToast("Sync failed", "error");
  });
}

// ---------- compose ----------

let composeSendHandler: (() => void) | null = null;
let composeAttachHandler: (() => void) | null = null;

export function registerComposeHandlers(handlers: {
  send: () => void;
  attach: () => void;
}): () => void {
  composeSendHandler = handlers.send;
  composeAttachHandler = handlers.attach;
  return () => {
    composeSendHandler = null;
    composeAttachHandler = null;
  };
}

export function openCompose(mode: ComposeMode = "new"): void {
  app.compose = {
    ...freshCompose(),
    open: true,
    mode,
    token: app.compose.token + 1,
  };
  app.paletteOpen = false;
}

export function markComposeDirty(): void {
  app.compose.dirty = true;
  app.compose.confirmingClose = false;
}

export function composeSaved(draftId: string): void {
  app.compose.draftId = draftId;
  app.compose.dirty = false;
}

export function setComposeSaving(saving: boolean): void {
  app.compose.saving = saving;
}

export function requestCloseCompose(): void {
  if (app.compose.dirty) {
    app.compose.confirmingClose = true;
  } else {
    app.compose.open = false;
  }
}

export function cancelCloseCompose(): void {
  app.compose.confirmingClose = false;
}

export function discardCompose(): void {
  app.compose = freshCompose();
}

export async function saveCompose(
  draft: DraftInput,
  explicit = false,
): Promise<void> {
  setComposeSaving(true);
  try {
    const { id } = await api.saveDraft(draft);
    composeSaved(id);
    if (explicit) {
      showToast("Draft saved");
      void refreshFolders();
    }
  } catch {
    showToast("Could not save draft", "error");
  } finally {
    setComposeSaving(false);
  }
}

export async function sendCompose(draft: DraftInput): Promise<void> {
  if (
    !draft.toAddresses.length &&
    !draft.ccAddresses.length &&
    !draft.bccAddresses.length
  ) {
    showToast("Add at least one recipient", "error");
    return;
  }
  setComposeSaving(true);
  try {
    await api.sendDraft(draft);
    app.compose = freshCompose();
    showToast("Message sent");
    void refreshFolders();
  } catch {
    setComposeSaving(false);
    showToast("Message not sent — it stays queued", "error");
  }
}

// ---------- accounts & settings ----------

export async function addAccount(input: ManualAccountInput): Promise<Account> {
  const account = await api.addAccountManual(input);
  await refreshAccounts();
  await refreshFolders();
  showToast(`Account ${account.email} added`);
  return account;
}

export async function removeAccount(accountId: number): Promise<void> {
  try {
    await api.removeAccount(accountId);
    if (app.selectedAccountId === accountId) app.selectedAccountId = null;
    await refreshAccounts();
    await refreshFolders();
    showToast("Account removed");
  } catch {
    showToast("Could not remove account", "error");
  }
}

export async function setAccountPaused(
  accountId: number,
  paused: boolean,
): Promise<void> {
  try {
    await api.setAccountPaused(accountId, paused);
    await refreshAccounts();
  } catch {
    showToast("Could not update account", "error");
  }
}

export async function saveSettings(settings: AppSettings): Promise<void> {
  try {
    await api.saveSettings(settings);
    app.settings = structuredClone(settings);
    showToast("Settings saved");
  } catch {
    showToast("Could not save settings", "error");
  }
}

// ---------- events ----------

/**
 * Previous sync state per account, so the sign-in-failed toast fires once per
 * transition instead of repeating on every event while the account stays parked.
 */
const previousSyncStates = new Map<number, SyncStateKind>();

/**
 * The engine emits this generic toast when a send fails; the send-state
 * handler already shows a failure toast for the same event, so this exact
 * message is dropped to keep one notice per failure.
 */
const SEND_FAILURE_TOAST = "A message failed to send — check the Outbox";

/**
 * Re-fetches the open message in the background so server-side changes
 * (flags replay, moves) show up without a spinner. Keeps the current detail
 * on failure; the next explicit open retries.
 */
async function reloadSelectedMessage(): Promise<void> {
  const id = app.selectedMessageId;
  if (id == null) return;
  const summary = messageSummaryFor(id);
  if (!summary) return;
  const accountId = accountIdForMessage(summary);
  try {
    const detail = await api.getMessage(accountId, id);
    if (app.selectedMessageId === id) {
      app.selectedMessage = detail;
      void loadThreadMessages(accountId, detail.threadId);
    }
  } catch {
    // Keep the stale detail on transient failures; the list is authoritative.
  }
}

function handleEvent(event: BackendEvent): void {
  switch (event.type) {
    case "sync-state": {
      const previous = previousSyncStates.get(event.accountId);
      previousSyncStates.set(event.accountId, event.state);
      app.syncStates[event.accountId] = {
        accountId: event.accountId,
        state: event.state,
        detail: event.detail,
      };
      const label = accountLabel(event.accountId);
      if (event.state !== previous) {
        switch (event.state) {
          case "syncing":
            pushSyncLog(`${label} — sync started`, "info");
            break;
          case "idle":
            pushSyncLog(`${label} — sync complete`, "success");
            break;
          case "offline":
            pushSyncLog(
              `${label} — offline, retrying${event.detail ? `: ${event.detail}` : ""}`,
              "warn",
            );
            break;
          case "paused":
            pushSyncLog(`${label} — paused`, "warn");
            break;
          case "error":
            pushSyncLog(
              `${label} — account needs attention${event.detail ? `: ${event.detail}` : ""}`,
              "error",
            );
            break;
        }
      }
      if (event.state === "error" && previous !== "error") {
        showToast(
          "Account needs attention — sign-in failed. Check the password in Settings.",
          "error",
        );
      }
      break;
    }
    case "sync-progress": {
      const label = accountLabel(event.accountId);
      switch (event.phase) {
        case "pass-start":
          pushSyncLog(`${label} — checking ${event.total} folders`, "info");
          break;
        case "folder-start":
          pushSyncLog(
            `${label} — ${event.folder} (${event.total} message${event.total === 1 ? "" : "s"})`,
            "info",
          );
          break;
        case "folder-done":
          pushSyncLog(
            event.new > 0
              ? `${label} — ${event.folder}: ${event.new} new`
              : `${label} — ${event.folder}: up to date`,
            event.new > 0 ? "success" : "info",
          );
          break;
        case "pass-done":
          pushSyncLog(`${label} — pass complete`, "success");
          break;
      }
      break;
    }
    case "messages-changed":
      void refreshMessages();
      void refreshFolders();
      void reloadSelectedMessage();
      break;
    case "folders-changed":
      void refreshFolders();
      break;
    case "accounts-changed":
      void refreshAccounts();
      void refreshFolders();
      break;
    case "unread-count": {
      const folder = app.folders.find(
        (candidate) => candidate.id === event.folderId,
      );
      if (folder) folder.unreadCount = event.unreadCount;
      break;
    }
    case "send-state":
      if (event.state === "failed")
        showToast("A message failed to send — it stays in the outbox", "error");
      break;
    case "toast":
      if (event.message !== SEND_FAILURE_TOAST)
        showToast(event.message, event.level);
      break;
    case "settings-changed":
      app.settings = event.settings;
      break;
    case "ui:compose":
      if (!app.compose.open) openCompose("new");
      break;
  }
}

// ---------- command registry ----------

function selectedMessageOrNull(): MessageDetail | null {
  return app.selectedMessage;
}

export const actions: CommandActions = {
  compose: () => openCompose("new"),
  openSearch,
  markAllRead: () => void markAllRead(),
  refresh: () => void syncAll(true),
  selectAll: () => showToast("Selected all messages"),
  reply: () => {
    if (selectedMessageOrNull()) openCompose("reply");
  },
  replyAll: () => {
    if (selectedMessageOrNull()) openCompose("replyAll");
  },
  forward: () => {
    if (selectedMessageOrNull()) openCompose("forward");
  },
  archive: () => void removeCurrent("archive"),
  deleteMessage: () => void removeCurrent("delete"),
  markUnread: () => void markUnread(),
  toggleStar: () => void toggleStar(),
  moveMessage: openMovePalette,
  toggleThread: toggleThreadPanel,
  openSettings,
  manageAccounts: openSettings,
  showShortcuts: () => {
    app.shortcutsOpen = true;
  },
};

export const commands: Command[] = buildCommands(actions);

// ---------- key action routing ----------

export function runKeyAction(action: ActionId): void {
  switch (action) {
    case "moveDown":
      moveSelection(1);
      break;
    case "moveUp":
      moveSelection(-1);
      break;
    case "openMessage":
      if (app.selectedMessageId != null) app.readingOpen = true;
      break;
    case "back":
      if (app.searchOpen) {
        closeSearch();
      } else {
        closeReading();
      }
      break;
    case "gotoInbox":
      void gotoFolderType("inbox");
      break;
    case "gotoStarred":
      void selectStarred();
      break;
    case "gotoDrafts":
      void gotoFolderType("drafts");
      break;
    case "gotoArchive":
      void gotoFolderType("archive");
      break;
    case "search":
      openSearch();
      break;
    case "shortcuts":
      toggleShortcuts();
      break;
    case "shortcutsClose":
      app.shortcutsOpen = false;
      break;
    case "viewerClose":
      closeAttachmentViewer();
      break;
    case "syncClose":
      closeSyncPanel();
      break;
    case "overlayClose":
      closeSettings();
      closeAccountSetup();
      break;
    case "compose":
      openCompose("new");
      break;
    case "sendCompose":
      composeSendHandler?.();
      break;
    case "attachInCompose":
      composeAttachHandler?.();
      break;
    case "composeClose":
      requestCloseCompose();
      break;
    case "commandPalette":
      openPalette("commands");
      break;
    case "star":
      void toggleStar();
      break;
    case "markUnread":
      void markUnread();
      break;
    case "archive":
      void removeCurrent("archive");
      break;
    case "delete":
      void removeCurrent("delete");
      break;
    case "reply":
      if (selectedMessageOrNull()) openCompose("reply");
      break;
    case "replyAll":
      if (selectedMessageOrNull()) openCompose("replyAll");
      break;
    case "forward":
      if (selectedMessageOrNull()) openCompose("forward");
      break;
    case "move":
      openMovePalette();
      break;
    case "scrollPage":
      break;
    default:
      break;
  }
}

// ---------- init ----------

let initialized = false;

export async function init(): Promise<void> {
  if (initialized) return;
  initialized = true;
  // Subscribe before the first awaits: the engine starts its workers at
  // process startup, so an account can already be syncing (and emit
  // sync-state) while these initial queries are in flight. Listening first
  // means that early state change is not missed.
  api.onEvent(handleEvent);
  const [settings, accounts] = await Promise.all([
    api.getSettings(),
    api.listAccounts(),
  ]);
  app.settings = settings;
  app.accounts = accounts;
  // Seed an idle default per account, but never overwrite a state an event
  // already delivered while the queries above were in flight.
  for (const account of accounts) {
    if (!app.syncStates[account.id]) {
      app.syncStates[account.id] = {
        accountId: account.id,
        state: "idle" as SyncStateKind,
      };
    }
  }
  await refreshFolders();
  const inbox =
    app.folders.find((folder) => folder.type === "inbox") ?? app.folders[0];
  if (inbox) {
    app.view = { kind: "folder", folderId: inbox.id, title: inbox.name };
    await refreshMessages();
    const first = app.messages[0];
    if (first) {
      app.selectedMessageId = first.id;
      app.readingOpen = true;
      await selectMessage(first.id);
    }
  }
  app.ready = true;
  // Pull activity the engine retained from passes that ran before this
  // webview subscribed.
  void refreshSyncActivity();
  // A fresh install has no accounts, so the three-pane shell would sit empty
  // with no way forward. Open the setup flow so the first thing shown is how
  // to connect a mailbox.
  if (!app.accounts.length) openAccountSetup();
}
