import type {
  Account,
  AppSettings,
  BackendEvent,
  Contact,
  DiscoveredConfig,
  DraftInput,
  Folder,
  ManualAccountInput,
  MessageDetail,
  MessageSummary,
  MessageView,
  OAuthResult,
  PickedFile,
  SearchFilter,
  SyncActivityEntry,
  ThreadSummary,
  FlagPatch,
  Unsubscribe,
} from "./types";

/**
 * The single seam between the UI and the Go engine. Every backend reach goes
 * through this interface; the Wails services layer implements it exactly and
 * the mock backend substitutes for it until bindings exist.
 */
export interface Backend {
  // Accounts
  listAccounts(): Promise<Account[]>;
  discover(email: string): Promise<DiscoveredConfig | null>;
  addAccountManual(input: ManualAccountInput): Promise<Account>;
  verifyAccount(input: ManualAccountInput): Promise<void>;
  /**
   * Opens the provider consent screen; `complete` resolves when the engine
   * pushes the flow's outcome (or the timeout lapses), so no call is held
   * open for the whole consent. `cancelOAuth` abandons the flow by state id.
   */
  startOAuth(
    email: string,
  ): Promise<{ url: string; stateId: string; complete: Promise<OAuthResult> }>;
  cancelOAuth(stateId: string): Promise<void>;
  removeAccount(accountId: number): Promise<void>;
  setAccountPaused(accountId: number, paused: boolean): Promise<void>;

  // Folders and messages
  listFolders(accountId: number | null): Promise<Folder[]>;
  listMessages(
    accountId: number | null,
    view: MessageView,
    page: number,
  ): Promise<MessageSummary[]>;
  getMessage(accountId: number, messageId: number): Promise<MessageDetail>;
  /**
   * Returns the message HTML for the sandboxed frame. With allowRemote the
   * engine re-sanitizes the on-disk raw source with remote loads permitted;
   * the stored form stays sanitized at rest.
   */
  getMessageHTML(
    accountId: number,
    messageId: number,
    allowRemote: boolean,
  ): Promise<string>;
  setFlags(
    accountId: number,
    messageId: number,
    patch: FlagPatch,
  ): Promise<void>;
  archiveMessage(accountId: number, messageId: number): Promise<void>;
  deleteMessage(accountId: number, messageId: number): Promise<void>;
  moveMessage(
    accountId: number,
    messageId: number,
    targetFolderId: number,
  ): Promise<void>;
  markAllRead(accountId: number, folderId: number): Promise<void>;
  search(
    accountId: number | null,
    filter: SearchFilter,
  ): Promise<MessageSummary[]>;

  // Compose
  /** Opens the native multi-select file dialog; empty result = canceled. */
  pickAttachments(): Promise<PickedFile[]>;
  saveDraft(draft: DraftInput): Promise<{ id: string }>;
  sendDraft(draft: DraftInput): Promise<{ queued: boolean }>;

  // Threads
  listThreads(accountId: number, folderId?: number): Promise<ThreadSummary[]>;
  getThread(accountId: number, threadId: number): Promise<MessageSummary[]>;

  // Attachments
  getAttachmentDataURL(
    accountId: number,
    messageId: number,
    attachmentId: number,
  ): Promise<string>;
  openAttachment(
    accountId: number,
    messageId: number,
    attachmentId: number,
  ): Promise<void>;
  saveAttachment(
    accountId: number,
    messageId: number,
    attachmentId: number,
  ): Promise<void>;

  // Contacts, settings, sync
  listContacts(accountId: number, prefix: string): Promise<Contact[]>;
  getSettings(): Promise<AppSettings>;
  saveSettings(settings: AppSettings): Promise<void>;
  syncNow(accountId?: number): Promise<void>;
  /** Recent sync activity retained by the engine (oldest first). */
  getSyncActivity(): Promise<SyncActivityEntry[]>;

  /** Subscribes to engine push events; returns an unsubscribe function. */
  onEvent(handler: (event: BackendEvent) => void): Unsubscribe;
}
