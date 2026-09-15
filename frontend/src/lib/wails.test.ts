import { describe, expect, it } from "vitest";
import type {
  AccountInfo,
  AppSettingsInfo,
  DiscoveredConfigInfo,
  DraftInput as DraftInputBinding,
  FolderInfo,
  MessageDetailInfo,
  PickedFile as PickedFileBinding,
} from "../bindings/github.com/mefiz0/posthaste/internal/app/models.js";
import {
  hasWailsHost,
  mapAccount,
  mapDiscoveredConfig,
  mapFolder,
  mapMessageDetail,
  mapPickedFile,
  mapSettings,
  parseBackendEvent,
  toDraftInput,
  toMessageView,
  toSearchFilter,
} from "./wails";
import type { DraftInput, MessageView, SearchFilter } from "./types";

const summaryFields = {
  id: 7,
  folderId: 42,
  threadId: 9,
  fromName: "Sarah Chen",
  fromAddress: "sarah@example.com",
  subject: "Project proposal",
  dateIso: "2026-09-14T14:32:00Z",
  flags: { seen: false, flagged: true, answered: false, draft: false },
  hasAttachments: true,
  snippet: "Here is the proposal",
};

describe("binding -> app mappers", () => {
  it("maps accounts field for field", () => {
    const info: AccountInfo = {
      id: 3,
      email: "me@example.com",
      displayName: "Me",
      isDefault: true,
      paused: false,
      color: "#5f8f5f",
    };
    expect(mapAccount(info)).toEqual({
      id: 3,
      email: "me@example.com",
      displayName: "Me",
      isDefault: true,
      paused: false,
      color: "#5f8f5f",
    });
  });

  it("maps folder types and falls back to user for unknown labels", () => {
    const base: FolderInfo = {
      id: 1,
      accountId: 2,
      name: "Inbox",
      imapPath: "INBOX",
      type: "inbox",
      unreadCount: 3,
      totalCount: 10,
    };
    expect(mapFolder(base).type).toBe("inbox");
    expect(mapFolder({ ...base, type: "mystery" }).type).toBe("user");
  });

  it("maps message detail, replacing nullable arrays with empty ones", () => {
    const info: MessageDetailInfo = {
      ...summaryFields,
      toAddresses: ["me@example.com"],
      ccAddresses: null,
      bodyText: "Hello",
      attachments: null,
      references: null,
    };
    const detail = mapMessageDetail(info);
    expect(detail.toAddresses).toEqual(["me@example.com"]);
    expect(detail.ccAddresses).toEqual([]);
    expect(detail.attachments).toEqual([]);
    expect(detail.references).toBeUndefined();
    expect(detail.flags.flagged).toBe(true);
    expect(detail.accountColor).toBeUndefined();
  });

  it("maps the remote-content flag, defaulting an absent engine value to false", () => {
    const base: MessageDetailInfo = {
      ...summaryFields,
      toAddresses: [],
      ccAddresses: [],
      bodyText: "Hello",
      attachments: [],
    };
    expect(mapMessageDetail(base).hasRemoteContent).toBe(false);
    expect(
      mapMessageDetail({ ...base, hasRemoteContent: true }).hasRemoteContent,
    ).toBe(true);
  });

  it("maps a picked file onto the compose-chip shape", () => {
    const info: PickedFileBinding = {
      name: "notes.txt",
      path: "/home/me/notes.txt",
      sizeBytes: 4352,
    };
    expect(mapPickedFile(info)).toEqual({
      name: "notes.txt",
      path: "/home/me/notes.txt",
      sizeBytes: 4352,
    });
  });

  it("maps settings and substitutes an empty keymap for null", () => {
    const info: AppSettingsInfo = {
      notificationsEnabled: true,
      minimizeToTray: false,
      verboseLogging: false,
      crashReportingEnabled: false,
      attachmentEagerThresholdBytes: 1048576,
      keymap: null,
    };
    const settings = mapSettings(info);
    expect(settings.keymap).toEqual({});
    expect(settings.attachmentEagerThresholdBytes).toBe(1048576);
  });

  it("maps discovered server configs and falls back to tls security", () => {
    const info: DiscoveredConfigInfo = {
      email: "user@yahoo.com",
      providerName: "Yahoo",
      requiresOAuth: false,
      imap: { host: "imap.mail.yahoo.com", port: 993, security: "tls", username: "user" },
      smtp: { host: "smtp.mail.yahoo.com", port: 465, security: "odd", username: "user" },
      appPasswordUrl: "https://help.yahoo.com/kb/generate-app-password-slm15221.html",
    };
    const config = mapDiscoveredConfig(info);
    expect(config.imap.security).toBe("tls");
    expect(config.smtp.security).toBe("tls");
    expect(config.appPasswordUrl).toContain("yahoo");
  });
});

describe("app -> binding adapters", () => {
  it("copies draft fields and drops absent optionals", () => {
    const draft: DraftInput = {
      accountId: 5,
      toAddresses: ["a@example.com"],
      ccAddresses: [],
      bccAddresses: [],
      subject: "Hi",
      bodyText: "Body",
    };
    const binding: DraftInputBinding = toDraftInput(draft);
    expect(binding.accountId).toBe(5);
    expect(binding.toAddresses).toEqual(["a@example.com"]);
    expect("draftId" in binding).toBe(false);
    expect("inReplyToMessageId" in binding).toBe(false);
    expect("attachments" in binding).toBe(false);

    const withDraft = toDraftInput({
      ...draft,
      draftId: "outbox-1",
      inReplyToMessageId: "12",
      attachments: ["/tmp/report.pdf"],
    });
    expect(withDraft.draftId).toBe("outbox-1");
    expect(withDraft.inReplyToMessageId).toBe("12");
    expect(withDraft.attachments).toEqual(["/tmp/report.pdf"]);
  });

  it("carries only set search filter fields", () => {
    const filter: SearchFilter = { text: "proposal", isUnread: true };
    const binding = toSearchFilter(filter);
    expect(binding.text).toBe("proposal");
    expect(binding.isUnread).toBe(true);
    expect("from" in binding).toBe(false);
    expect("hasAttachment" in binding).toBe(false);
  });

  it("passes starred and folder views through", () => {
    const starred: MessageView = { special: "starred" };
    expect(toMessageView(starred)).toEqual({ folderId: undefined, special: "starred" });
    const folder: MessageView = { folderId: 41 };
    expect(toMessageView(folder)).toEqual({ folderId: 41, special: undefined });
  });
});

describe("parseBackendEvent", () => {
  it("parses a sync-state payload with detail", () => {
    const event = parseBackendEvent("sync-state", {
      type: "sync-state",
      accountId: 2,
      state: "error",
      detail: "connection refused",
    });
    expect(event).toEqual({
      type: "sync-state",
      accountId: 2,
      state: "error",
      detail: "connection refused",
    });
  });

  it("unwraps a single-element array payload", () => {
    const event = parseBackendEvent("unread-count", [
      { type: "unread-count", accountId: 1, folderId: 99, unreadCount: 4 },
    ]);
    expect(event).toEqual({
      type: "unread-count",
      accountId: 1,
      folderId: 99,
      unreadCount: 4,
    });
  });

  it("rejects unknown sync and send states", () => {
    expect(
      parseBackendEvent("sync-state", { accountId: 1, state: "warp" }),
    ).toBeNull();
    expect(
      parseBackendEvent("send-state", {
        accountId: 1,
        draftId: "x",
        state: "exploded",
      }),
    ).toBeNull();
  });

  it("normalizes a zero folder id to absent", () => {
    const event = parseBackendEvent("messages-changed", {
      type: "messages-changed",
      accountId: 1,
      folderId: 0,
    });
    expect(event).toEqual({ type: "messages-changed", accountId: 1 });
  });

  it("parses send failures with their error text", () => {
    const event = parseBackendEvent("send-state", {
      type: "send-state",
      accountId: 1,
      draftId: "abc",
      state: "failed",
      error: "smtp: 535 bad credentials",
    });
    expect(event).toEqual({
      type: "send-state",
      accountId: 1,
      draftId: "abc",
      state: "failed",
      error: "smtp: 535 bad credentials",
    });
  });

  it("parses toasts, defaulting the level to info", () => {
    expect(parseBackendEvent("toast", { level: "error", message: "boom" })).toEqual({
      type: "toast",
      level: "error",
      message: "boom",
    });
    expect(parseBackendEvent("toast", { message: "quiet" })).toEqual({
      type: "toast",
      level: "info",
      message: "quiet",
    });
  });

  it("parses payload-less events even when data is null", () => {
    expect(parseBackendEvent("accounts-changed", null)).toEqual({
      type: "accounts-changed",
    });
    expect(parseBackendEvent("ui:compose", null)).toEqual({ type: "ui:compose" });
  });

  it("parses oauth-complete outcomes and rejects malformed ones", () => {
    expect(
      parseBackendEvent("oauth-complete", {
        type: "oauth-complete",
        stateId: "flow-1",
        ok: true,
      }),
    ).toEqual({ type: "oauth-complete", stateId: "flow-1", ok: true, error: undefined });
    expect(
      parseBackendEvent("oauth-complete", {
        type: "oauth-complete",
        stateId: "flow-2",
        ok: false,
        error: "the sign-in was canceled",
      }),
    ).toEqual({
      type: "oauth-complete",
      stateId: "flow-2",
      ok: false,
      error: "the sign-in was canceled",
    });
    expect(
      parseBackendEvent("oauth-complete", { type: "oauth-complete", ok: true }),
    ).toBeNull();
    expect(
      parseBackendEvent("oauth-complete", {
        type: "oauth-complete",
        stateId: "flow-3",
      }),
    ).toBeNull();
  });

  it("maps settings-changed payloads through the settings mapper", () => {
    const event = parseBackendEvent("settings-changed", {
      type: "settings-changed",
      settings: {
        notificationsEnabled: false,
        minimizeToTray: true,
        verboseLogging: false,
        crashReportingEnabled: false,
        attachmentEagerThresholdBytes: 1,
        keymap: { archive: "Ctrl+E" },
      },
    });
    expect(event).toEqual({
      type: "settings-changed",
      settings: {
        notificationsEnabled: false,
        minimizeToTray: true,
        verboseLogging: false,
        crashReportingEnabled: false,
        attachmentEagerThresholdBytes: 1,
        keymap: { archive: "Ctrl+E" },
      },
    });
  });

  it("returns null for unknown names and malformed payloads", () => {
    expect(parseBackendEvent("unknown-event", { x: 1 })).toBeNull();
    expect(parseBackendEvent("sync-state", null)).toBeNull();
    expect(parseBackendEvent("unread-count", { accountId: 1 })).toBeNull();
    expect(parseBackendEvent("folders-changed", [1, 2])).toBeNull();
  });
});

describe("hasWailsHost", () => {
  it("is true only when the shell-injected flags object exists", () => {
    expect(hasWailsHost({ _wails: { flags: {} } })).toBe(true);
    expect(hasWailsHost({ _wails: {} })).toBe(false);
    expect(hasWailsHost({})).toBe(false);
    expect(hasWailsHost(undefined)).toBe(false);
  });
});
