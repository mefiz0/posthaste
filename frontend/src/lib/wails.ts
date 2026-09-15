/**
 * The Wails-backed implementation of the Backend seam. Thin service calls plus
 * a set of exported, pure mapping helpers that adapt the generated binding
 * shapes onto the app-side types — generated model shapes never leak past this
 * file.
 */
import { Events } from "@wailsio/runtime";
import {
  AccountService,
  AppService,
  AttachmentService,
  ComposeService,
  MailService,
  SettingsService,
} from "../bindings/github.com/mefiz0/posthaste/internal/app/index.js";
import type {
  AccountInfo,
  AppSettingsInfo,
  AttachmentInfo,
  ContactInfo,
  DiscoveredConfigInfo,
  DraftInput as DraftInputBinding,
  FlagPatch as FlagPatchBinding,
  FolderInfo,
  ManualAccountInput as ManualAccountInputBinding,
  MessageDetailInfo,
  MessageSummaryInfo,
  MessageView as MessageViewBinding,
  PickedFile as PickedFileBinding,
  SearchFilterInput,
  ServerConfigInfo,
  ThreadSummaryInfo,
} from "../bindings/github.com/mefiz0/posthaste/internal/app/models.js";
import type { Backend } from "./backend";
import type {
  Account,
  AppSettings,
  BackendEvent,
  Contact,
  DiscoveredConfig,
  DraftInput,
  FlagPatch,
  Folder,
  FolderType,
  ManualAccountInput,
  MessageDetail,
  MessageSummary,
  MessageView,
  OAuthResult,
  PickedFile,
  SearchFilter,
  SendState,
  ServerConfig,
  ServerSecurity,
  SyncActivityEntry,
  SyncStateKind,
  ThreadSummary,
  Unsubscribe,
} from "./types";

// ---------- narrowing helpers (the only place `unknown` is unwrapped) ----------

function asRecord(value: unknown): Record<string, unknown> | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return null;
  }
  return value as Record<string, unknown>;
}

function asString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function asNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value)
    ? value
    : undefined;
}

function asBoolean(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

function asStringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}

/**
 * The event bridge delivers one payload object per event, but a multi-arg
 * emit would arrive as an array; unwrap that form so both shapes parse.
 */
function eventPayload(data: unknown): unknown {
  if (Array.isArray(data) && data.length === 1) return data[0];
  return data;
}

// ---------- pure mappers: binding DTO -> app type ----------

/** Maps a bound account onto the app shape; fields are already aligned. */
export function mapAccount(info: AccountInfo): Account {
  return {
    id: info.id,
    email: info.email,
    displayName: info.displayName,
    isDefault: info.isDefault,
    paused: info.paused,
    color: info.color,
  };
}

const FOLDER_TYPES: readonly FolderType[] = [
  "inbox",
  "sent",
  "drafts",
  "archive",
  "trash",
  "junk",
  "user",
];

/** Maps a folder, falling back to the "user" type for unknown engine labels. */
export function mapFolder(info: FolderInfo): Folder {
  const type =
    FOLDER_TYPES.find((candidate) => candidate === info.type) ?? "user";
  return {
    id: info.id,
    accountId: info.accountId,
    name: info.name,
    imapPath: info.imapPath,
    type,
    unreadCount: info.unreadCount,
    totalCount: info.totalCount,
  };
}

/** Maps a message summary; the engine already merged accounts and colors. */
export function mapMessageSummary(info: MessageSummaryInfo): MessageSummary {
  return {
    id: info.id,
    folderId: info.folderId,
    threadId: info.threadId,
    fromName: info.fromName,
    fromAddress: info.fromAddress,
    subject: info.subject,
    dateIso: info.dateIso,
    flags: {
      seen: info.flags.seen,
      flagged: info.flags.flagged,
      answered: info.flags.answered,
      draft: info.flags.draft,
    },
    hasAttachments: info.hasAttachments,
    accountColor: info.accountColor,
    snippet: info.snippet,
  };
}

/** Maps full message detail; nullable bridge arrays become empty arrays. */
export function mapMessageDetail(info: MessageDetailInfo): MessageDetail {
  return {
    ...mapMessageSummary(info),
    toAddresses: asStringArray(info.toAddresses),
    ccAddresses: asStringArray(info.ccAddresses),
    bodyText: info.bodyText,
    bodyHtml: info.bodyHtml,
    attachments: (info.attachments ?? []).map(mapAttachment),
    inReplyTo: info.inReplyTo,
    references: info.references ? asStringArray(info.references) : undefined,
    hasRemoteContent: asBoolean(info.hasRemoteContent) ?? false,
  };
}

/** Maps a file picked in the native dialog onto the compose-chip shape. */
export function mapPickedFile(info: PickedFileBinding): PickedFile {
  return {
    name: info.name,
    path: info.path,
    sizeBytes: info.sizeBytes,
  };
}

function mapAttachment(
  info: AttachmentInfo,
): MessageDetail["attachments"][number] {
  return {
    id: info.id,
    filename: info.filename,
    mimeType: info.mimeType,
    sizeBytes: info.sizeBytes,
    contentHash: info.contentHash,
    isInline: info.isInline,
    contentId: info.contentId,
    fetchState: info.fetchState === "fetched" ? "fetched" : "not_fetched",
  };
}

/** Maps a conversation summary for the thread views. */
export function mapThread(info: ThreadSummaryInfo): ThreadSummary {
  return {
    id: info.id,
    subject: info.subject,
    messageCount: info.messageCount,
    lastDateIso: info.lastDateIso,
    unreadCount: info.unreadCount,
    hasAttachments: info.hasAttachments,
    participants: asStringArray(info.participants),
  };
}

/** Maps an autocomplete suggestion. */
export function mapContact(info: ContactInfo): Contact {
  return { name: info.name, address: info.address };
}

const SECURITY_MODES: readonly ServerSecurity[] = ["tls", "starttls", "none"];

function mapServerConfig(info: ServerConfigInfo): ServerConfig {
  const security =
    SECURITY_MODES.find((candidate) => candidate === info.security) ?? "tls";
  return {
    host: info.host,
    port: info.port,
    security,
    username: info.username,
  };
}

/** Maps a discovered provider configuration onto the setup-flow shape. */
export function mapDiscoveredConfig(
  info: DiscoveredConfigInfo,
): DiscoveredConfig {
  return {
    email: info.email,
    providerName: info.providerName,
    requiresOAuth: info.requiresOAuth,
    imap: mapServerConfig(info.imap),
    smtp: mapServerConfig(info.smtp),
    appPasswordUrl: info.appPasswordUrl,
  };
}

/** Maps a settings snapshot; an absent keymap becomes an empty override map. */
export function mapSettings(info: AppSettingsInfo): AppSettings {
  const keymap: Record<string, string> = {};
  for (const [action, chord] of Object.entries(info.keymap ?? {})) {
    if (chord != null) keymap[action] = chord;
  }
  return {
    notificationsEnabled: info.notificationsEnabled,
    minimizeToTray: info.minimizeToTray,
    verboseLogging: info.verboseLogging,
    attachmentEagerThresholdBytes: info.attachmentEagerThresholdBytes,
    keymap,
  };
}

// ---------- pure adapters: app type -> binding DTO ----------

/** Builds the manual setup payload from the setup-flow input. */
export function toManualAccountInput(
  input: ManualAccountInput,
): ManualAccountInputBinding {
  return {
    email: input.email,
    password: input.password,
    displayName: input.displayName,
    auth: input.auth,
    imap: toServerConfig(input.imap),
    smtp: toServerConfig(input.smtp),
  };
}

function toServerConfig(config: ServerConfig): ServerConfigInfo {
  return {
    host: config.host,
    port: config.port,
    security: config.security,
    username: config.username,
  };
}

/** Builds a list-messages view request; absent fields mean the unified inbox. */
export function toMessageView(view: MessageView): MessageViewBinding {
  return { folderId: view.folderId, special: view.special };
}

/** Builds the search payload; only genuinely-set filters are carried over. */
export function toSearchFilter(filter: SearchFilter): SearchFilterInput {
  const mapped: SearchFilterInput = { text: filter.text };
  if (filter.from) mapped.from = filter.from;
  if (filter.to) mapped.to = filter.to;
  if (filter.subject) mapped.subject = filter.subject;
  if (filter.hasAttachment != null) mapped.hasAttachment = filter.hasAttachment;
  if (filter.isUnread != null) mapped.isUnread = filter.isUnread;
  if (filter.isStarred != null) mapped.isStarred = filter.isStarred;
  if (filter.afterIso) mapped.afterIso = filter.afterIso;
  if (filter.beforeIso) mapped.beforeIso = filter.beforeIso;
  if (filter.folderName) mapped.folderName = filter.folderName;
  return mapped;
}

/** Builds a draft payload; empty optional fields are dropped, not nulled. */
export function toDraftInput(draft: DraftInput): DraftInputBinding {
  const mapped: DraftInputBinding = {
    toAddresses: [...draft.toAddresses],
    ccAddresses: [...draft.ccAddresses],
    bccAddresses: [...draft.bccAddresses],
    subject: draft.subject,
    bodyText: draft.bodyText,
  };
  if (draft.draftId != null) mapped.draftId = draft.draftId;
  if (draft.accountId != null) mapped.accountId = draft.accountId;
  if (draft.inReplyToMessageId != null)
    mapped.inReplyToMessageId = draft.inReplyToMessageId;
  if (draft.attachments != null && draft.attachments.length > 0) {
    mapped.attachments = [...draft.attachments];
  }
  return mapped;
}

/** A flag patch passes through unchanged; the bridge treats absent as "keep". */
export function toFlagPatch(patch: FlagPatch): FlagPatchBinding {
  return { seen: patch.seen, flagged: patch.flagged };
}

// ---------- event parsing ----------

const SYNC_STATES: readonly SyncStateKind[] = [
  "idle",
  "syncing",
  "offline",
  "error",
  "paused",
];
const SEND_STATES: readonly SendState[] = [
  "draft",
  "queued",
  "sending",
  "sent",
  "failed",
];

/**
 * Parses one bridge event into the app-side union. Returns null for payloads
 * that cannot be understood, so a shape drift degrades to a dropped event
 * instead of a crash.
 */
export function parseBackendEvent(
  name: string,
  data: unknown,
): BackendEvent | null {
  const payload = asRecord(eventPayload(data));
  switch (name) {
    case "sync-state": {
      if (!payload) return null;
      const accountId = asNumber(payload.accountId);
      const state = asString(payload.state);
      if (accountId == null || state == null) return null;
      const kind = SYNC_STATES.find((candidate) => candidate === state);
      if (!kind) return null;
      return {
        type: "sync-state",
        accountId,
        state: kind,
        detail: asString(payload.detail),
      };
    }
    case "messages-changed": {
      if (!payload) return null;
      const accountId = asNumber(payload.accountId);
      if (accountId == null) return null;
      const folderId = asNumber(payload.folderId);
      return {
        type: "messages-changed",
        accountId,
        folderId: folderId === 0 ? undefined : folderId,
      };
    }
    case "folders-changed": {
      if (!payload) return null;
      const accountId = asNumber(payload.accountId);
      if (accountId == null) return null;
      return { type: "folders-changed", accountId };
    }
    case "send-state": {
      if (!payload) return null;
      const accountId = asNumber(payload.accountId);
      const draftId = asString(payload.draftId);
      const state = asString(payload.state);
      if (accountId == null || draftId == null || state == null) return null;
      const sendState = SEND_STATES.find((candidate) => candidate === state);
      if (!sendState) return null;
      return {
        type: "send-state",
        accountId,
        draftId,
        state: sendState,
        error: asString(payload.error),
      };
    }
    case "unread-count": {
      if (!payload) return null;
      const accountId = asNumber(payload.accountId);
      const folderId = asNumber(payload.folderId);
      const unreadCount = asNumber(payload.unreadCount);
      if (accountId == null || folderId == null || unreadCount == null)
        return null;
      return { type: "unread-count", accountId, folderId, unreadCount };
    }
    case "toast": {
      if (!payload) return null;
      const message = asString(payload.message);
      if (message == null) return null;
      return {
        type: "toast",
        level: payload.level === "error" ? "error" : "info",
        message,
      };
    }
    case "accounts-changed":
      return { type: "accounts-changed" };
    case "oauth-complete": {
      if (!payload) return null;
      const stateId = asString(payload.stateId);
      if (stateId == null) return null;
      const ok = asBoolean(payload.ok);
      if (ok == null) return null;
      return {
        type: "oauth-complete",
        stateId,
        ok,
        error: asString(payload.error),
      };
    }
    case "settings-changed": {
      if (!payload || !asRecord(payload.settings)) return null;
      return {
        type: "settings-changed",
        settings: mapSettings(payload.settings as AppSettingsInfo),
      };
    }
    case "ui:compose":
      return { type: "ui:compose" };
    default:
      return null;
  }
}

/**
 * Every event name the engine emits; one subscription each keeps the mapping
 * per event simple and the unsubscribe complete.
 */
const EVENT_NAMES = [
  "sync-state",
  "messages-changed",
  "folders-changed",
  "send-state",
  "unread-count",
  "toast",
  "accounts-changed",
  "settings-changed",
  "oauth-complete",
  "ui:compose",
] as const;

/**
 * How long the frontend waits for an OAuth consent flow to resolve. The wait
 * is event-driven, so holding it costs nothing; the limit only exists so a
 * forgotten browser tab eventually reports failure.
 */
const OAUTH_COMPLETE_TIMEOUT_MS = 10 * 60 * 1000;

/**
 * True when the page runs inside the Wails webview. The Go shell injects
 * `window._wails.flags` before any page script executes; the npm runtime
 * module never populates that object, so its presence is the reliable signal.
 */
export function hasWailsHost(
  scope: { readonly _wails?: { readonly flags?: unknown } } | undefined,
): boolean {
  return scope?._wails?.flags !== undefined;
}

// ---------- the backend ----------

interface WailsEventLike {
  readonly data: unknown;
}

/**
 * Implements Backend over the generated Wails service bindings. Every method
 * is a direct service call plus a mapper; no view logic lives here because
 * the engine already owns unified folders, starred merge, search merge,
 * threads, and contacts.
 */
export class WailsBackend implements Backend {
  // ---------- accounts ----------

  async listAccounts(): Promise<Account[]> {
    const accounts = (await AccountService.ListAccounts()) ?? [];
    return accounts.map(mapAccount);
  }

  async discover(email: string): Promise<DiscoveredConfig | null> {
    const config = await AccountService.Discover(email);
    return config ? mapDiscoveredConfig(config) : null;
  }

  async addAccountManual(input: ManualAccountInput): Promise<Account> {
    const account = await AccountService.AddAccount(
      toManualAccountInput(input),
    );
    return mapAccount(account);
  }

  async verifyAccount(input: ManualAccountInput): Promise<void> {
    await AccountService.VerifyCredentials(toManualAccountInput(input));
  }

  async startOAuth(
    email: string,
  ): Promise<{ url: string; stateId: string; complete: Promise<OAuthResult> }> {
    const start = await AccountService.StartOAuth(email);
    return {
      url: start.url,
      stateId: start.stateId,
      complete: this.awaitOAuthComplete(start.stateId),
    };
  }

  /**
   * Resolves when the engine pushes the flow's outcome as an oauth-complete
   * event, or after the consent timeout. No binding call stays open for the
   * duration of the consent.
   */
  private awaitOAuthComplete(stateId: string): Promise<OAuthResult> {
    return new Promise((resolve) => {
      const off = Events.On("oauth-complete", (event: WailsEventLike) => {
        const parsed = parseBackendEvent("oauth-complete", event.data);
        if (parsed?.type !== "oauth-complete" || parsed.stateId !== stateId)
          return;
        finish({ success: parsed.ok, error: parsed.error });
      });
      const timer = setTimeout(
        () => finish({ success: false, error: "the authorization timed out" }),
        OAUTH_COMPLETE_TIMEOUT_MS,
      );
      function finish(result: OAuthResult): void {
        clearTimeout(timer);
        off();
        resolve(result);
      }
    });
  }

  async cancelOAuth(stateId: string): Promise<void> {
    await AccountService.CancelOAuth(stateId);
  }

  async removeAccount(accountId: number): Promise<void> {
    await AccountService.RemoveAccount(accountId);
  }

  async setAccountPaused(accountId: number, paused: boolean): Promise<void> {
    await AccountService.SetAccountPaused(accountId, paused);
  }

  // ---------- folders and messages ----------

  async listFolders(accountId: number | null): Promise<Folder[]> {
    const folders = (await MailService.ListFolders(accountId ?? 0)) ?? [];
    return folders.map(mapFolder);
  }

  async listMessages(
    accountId: number | null,
    view: MessageView,
    page: number,
  ): Promise<MessageSummary[]> {
    const messages =
      (await MailService.ListMessages(
        accountId ?? 0,
        toMessageView(view),
        page,
      )) ?? [];
    return messages.map(mapMessageSummary);
  }

  async getMessage(
    accountId: number,
    messageId: number,
  ): Promise<MessageDetail> {
    return mapMessageDetail(await MailService.GetMessage(accountId, messageId));
  }

  async getMessageHTML(
    accountId: number,
    messageId: number,
    allowRemote: boolean,
  ): Promise<string> {
    return MailService.GetMessageHTML(accountId, messageId, allowRemote);
  }

  async setFlags(
    accountId: number,
    messageId: number,
    patch: FlagPatch,
  ): Promise<void> {
    await MailService.SetFlags(accountId, messageId, toFlagPatch(patch));
  }

  async archiveMessage(accountId: number, messageId: number): Promise<void> {
    await MailService.ArchiveMessage(accountId, messageId);
  }

  async deleteMessage(accountId: number, messageId: number): Promise<void> {
    await MailService.DeleteMessage(accountId, messageId);
  }

  async moveMessage(
    accountId: number,
    messageId: number,
    targetFolderId: number,
  ): Promise<void> {
    await MailService.MoveMessage(accountId, messageId, targetFolderId);
  }

  async markAllRead(accountId: number, folderId: number): Promise<void> {
    await MailService.MarkAllRead(accountId, folderId);
  }

  async search(
    accountId: number | null,
    filter: SearchFilter,
  ): Promise<MessageSummary[]> {
    const results =
      (await MailService.Search(accountId ?? 0, toSearchFilter(filter))) ?? [];
    return results.map(mapMessageSummary);
  }

  // ---------- compose ----------

  async pickAttachments(): Promise<PickedFile[]> {
    const picked = (await ComposeService.PickAttachments()) ?? [];
    return picked.map(mapPickedFile);
  }

  async saveDraft(draft: DraftInput): Promise<{ id: string }> {
    const result = await ComposeService.SaveDraft(toDraftInput(draft));
    return { id: result.id };
  }

  async sendDraft(draft: DraftInput): Promise<{ queued: boolean }> {
    const result = await ComposeService.SendDraft(toDraftInput(draft));
    return { queued: result.queued };
  }

  // ---------- threads ----------

  async listThreads(
    accountId: number,
    folderId?: number,
  ): Promise<ThreadSummary[]> {
    const threads =
      (await MailService.ListThreads(accountId, folderId ?? 0)) ?? [];
    return threads.map(mapThread);
  }

  async getThread(
    accountId: number,
    threadId: number,
  ): Promise<MessageSummary[]> {
    const messages = (await MailService.GetThread(accountId, threadId)) ?? [];
    return messages.map(mapMessageSummary);
  }

  // ---------- attachments ----------

  async getAttachmentDataURL(
    accountId: number,
    messageId: number,
    attachmentId: number,
  ): Promise<string> {
    return AttachmentService.GetAttachmentDataURL(
      accountId,
      messageId,
      attachmentId,
    );
  }

  async openAttachment(
    accountId: number,
    messageId: number,
    attachmentId: number,
  ): Promise<void> {
    await AttachmentService.OpenAttachment(accountId, messageId, attachmentId);
  }

  async saveAttachment(
    accountId: number,
    messageId: number,
    attachmentId: number,
  ): Promise<void> {
    await AttachmentService.SaveAttachment(accountId, messageId, attachmentId);
  }

  // ---------- contacts, settings, sync ----------

  async listContacts(accountId: number, prefix: string): Promise<Contact[]> {
    const contacts =
      (await ComposeService.ListContacts(accountId, prefix)) ?? [];
    return contacts.map(mapContact);
  }

  async getSettings(): Promise<AppSettings> {
    return mapSettings(await SettingsService.GetSettings());
  }

  async saveSettings(settings: AppSettings): Promise<void> {
    const payload: AppSettingsInfo = {
      notificationsEnabled: settings.notificationsEnabled,
      minimizeToTray: settings.minimizeToTray,
      verboseLogging: settings.verboseLogging,
      attachmentEagerThresholdBytes: settings.attachmentEagerThresholdBytes,
      keymap: settings.keymap,
    };
    await SettingsService.SaveSettings(payload);
  }

  async getSyncActivity(): Promise<SyncActivityEntry[]> {
    const entries = (await AppService.SyncActivity()) ?? [];
    return entries.map((entry) => ({
      at: entry.at ?? "",
      accountId: entry.accountId ?? 0,
      text: entry.text ?? "",
      level: syncActivityLevel(entry.level),
    }));
  }

  async syncNow(accountId?: number): Promise<void> {
    await AppService.SyncNow(accountId ?? 0);
  }

  // ---------- events ----------

  onEvent(handler: (event: BackendEvent) => void): Unsubscribe {
    const unsubscribeFns = EVENT_NAMES.map((name) =>
      Events.On(name, (event: WailsEventLike) => {
        const parsed = parseBackendEvent(name, event.data);
        if (parsed) handler(parsed);
      }),
    );
    return () => {
      for (const off of unsubscribeFns) off();
    };
  }
}

/** Narrows the engine's free-form level string to the log's union. */
function syncActivityLevel(level: string): SyncActivityEntry["level"] {
  switch (level) {
    case "success":
    case "warn":
    case "error":
      return level;
    default:
      return "info";
  }
}
