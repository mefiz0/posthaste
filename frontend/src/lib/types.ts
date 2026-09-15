/**
 * App-side types mirroring the Go domain objects. The Wails binding layer maps
 * these 1:1; field names stay camelCase across the bridge.
 */

export type FolderType =
  "inbox" | "sent" | "drafts" | "archive" | "trash" | "junk" | "user";

export type SendState = "draft" | "queued" | "sending" | "sent" | "failed";

export type SyncStateKind = "idle" | "syncing" | "offline" | "error" | "paused";

/** Quick filters applied on top of the current folder view. */
export type MessageFilter = "all" | "unread" | "starred" | "attachments";

export type AttachmentFetchState = "not_fetched" | "fetched";

export type ComposeMode = "new" | "reply" | "replyAll" | "forward";

export interface Account {
  id: number;
  email: string;
  displayName: string;
  isDefault: boolean;
  paused: boolean;
  color?: string;
}

export interface Folder {
  id: number;
  accountId: number;
  name: string;
  imapPath: string;
  type: FolderType;
  unreadCount: number;
  totalCount: number;
}

export interface MessageFlags {
  seen: boolean;
  flagged: boolean;
  answered: boolean;
  draft: boolean;
}

export interface MessageSummary {
  id: number;
  folderId: number;
  threadId: number;
  fromName: string;
  fromAddress: string;
  subject: string;
  dateIso: string;
  flags: MessageFlags;
  hasAttachments: boolean;
  accountColor?: string;
  /** Short plain-text excerpt shown in the message list. */
  snippet: string;
}

export interface MessageDetail extends MessageSummary {
  toAddresses: string[];
  ccAddresses: string[];
  bodyText: string;
  /** Sanitized at ingest by the Go engine; still only ever rendered in a sandboxed iframe. */
  bodyHtml?: string;
  attachments: Attachment[];
  inReplyTo?: string;
  references?: string[];
  /** True when the message's HTML would load remote resources if allowed. */
  hasRemoteContent?: boolean;
}

export interface Attachment {
  id: number;
  filename: string;
  mimeType: string;
  sizeBytes: number;
  contentHash: string;
  isInline: boolean;
  contentId?: string;
  fetchState: AttachmentFetchState;
}

export interface ThreadSummary {
  id: number;
  subject: string;
  messageCount: number;
  lastDateIso: string;
  unreadCount: number;
  hasAttachments: boolean;
  participants: string[];
}

/** One file chosen in the native attach dialog, before it is ingested. */
export interface PickedFile {
  /** Base file name, shown on the compose chip and kept for the sent MIME. */
  name: string;
  /** Absolute local path; the engine reads the bytes from here. */
  path: string;
  sizeBytes: number;
}

export interface DraftInput {
  /** Set when updating an existing draft; absent when composing fresh. */
  draftId?: string;
  accountId?: number;
  inReplyToMessageId?: string;
  toAddresses: string[];
  ccAddresses: string[];
  bccAddresses: string[];
  subject: string;
  bodyText: string;
  /** Absolute local paths of the files to attach, as returned by pickAttachments. */
  attachments?: string[];
}

/** One outgoing message shown in the outbox view. */
export interface OutboxItem {
  id: string;
  accountId: number;
  to: string;
  subject: string;
  state: SendState;
  attempts: number;
  error?: string;
  createdIso: string;
  /** When the next automatic retry is due, absent once failed. */
  nextAttemptIso?: string;
}

/** The editable content of a failed send, returned by getOutboxDraft. */
export interface OutboxDraft {
  draft: DraftInput;
  /** File names already held in the blob store and kept on resend. */
  attachmentNames?: string[];
}

export interface SearchFilter {
  text: string;
  from?: string;
  to?: string;
  subject?: string;
  hasAttachment?: boolean;
  isUnread?: boolean;
  isStarred?: boolean;
  afterIso?: string;
  beforeIso?: string;
  folderName?: string;
}

export interface Contact {
  name: string;
  address: string;
}

export type ServerSecurity = "tls" | "starttls" | "none";

export interface ServerConfig {
  host: string;
  port: number;
  security: ServerSecurity;
  username: string;
}

export interface DiscoveredConfig {
  email: string;
  providerName: string;
  requiresOAuth: boolean;
  imap: ServerConfig;
  smtp: ServerConfig;
  /** Provider documentation URL for app-password setup, when applicable. */
  appPasswordUrl?: string;
}

export interface ManualAccountInput {
  email: string;
  password: string;
  displayName?: string;
  /** 'oauth' means the credential already lives in the OS keyring. */
  auth: "password" | "oauth";
  imap: ServerConfig;
  smtp: ServerConfig;
}

export interface OAuthResult {
  success: boolean;
  error?: string;
}

export interface AccountSyncState {
  accountId: number;
  state: SyncStateKind;
  detail?: string;
  lastSyncIso?: string;
}

export interface AppSettings {
  notificationsEnabled: boolean;
  minimizeToTray: boolean;
  verboseLogging: boolean;
  /** Opt-in crash reporting; off by default, separate from telemetry. */
  crashReportingEnabled: boolean;
  attachmentEagerThresholdBytes: number;
  /** Action id -> replacement chord string, e.g. { archive: 'A' }. */
  keymap: Record<string, string>;
}

export type FlagPatch = {
  seen?: boolean;
  flagged?: boolean;
};

export interface MessageView {
  folderId?: number;
  special?: "starred";
}

/** Push events from the Go engine; the frontend never polls for these. */
export type BackendEvent =
  | {
      type: "sync-state";
      accountId: number;
      state: SyncStateKind;
      detail?: string;
    }
  | {
      type: "sync-progress";
      accountId: number;
      folder?: string;
      phase: "pass-start" | "folder-start" | "folder-done" | "pass-done";
      new: number;
      total: number;
      at: string;
    }
  | { type: "messages-changed"; accountId: number; folderId?: number }
  | { type: "folders-changed"; accountId: number }
  | {
      type: "send-state";
      accountId: number;
      draftId: string;
      state: SendState;
      error?: string;
    }
  | {
      type: "unread-count";
      accountId: number;
      folderId: number;
      unreadCount: number;
    }
  | { type: "toast"; level: "info" | "error"; message: string }
  | { type: "accounts-changed" }
  | { type: "settings-changed"; settings: AppSettings }
  /** An OAuth consent flow resolved; the UI matches flows by stateId. */
  | { type: "oauth-complete"; stateId: string; ok: boolean; error?: string }
  /** Tray "Compose New" request: the shell asks the UI to open the drawer. */
  | { type: "ui:compose" };

/** One retained sync activity line returned by the engine. */
export interface SyncActivityEntry {
  at: string;
  accountId: number;
  text: string;
  level: "info" | "success" | "warn" | "error";
}

export type Unsubscribe = () => void;
