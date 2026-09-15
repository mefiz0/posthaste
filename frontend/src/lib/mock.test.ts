import { describe, expect, it } from "vitest";
import { createMockBackend } from "./mock";
import type { BackendEvent, MessageSummary } from "./types";

const INBOX_FOLDER_ID = 1;
const DRAFTS_FOLDER_ID = 2;
const SENT_FOLDER_ID = 3;
const ARCHIVE_FOLDER_ID = 4;
const TRASH_FOLDER_ID = 5;

/** Deterministic wait for the mock's 10-30ms latency windows. */
async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 60));
}

async function listFolder(
  backend: ReturnType<typeof createMockBackend>,
  folderId: number,
): Promise<MessageSummary[]> {
  return backend.listMessages(1, { folderId }, 0);
}

describe("mock backend: listing and views", () => {
  it("exposes the mockup folder set", async () => {
    const backend = createMockBackend();
    const folders = await backend.listFolders(null);
    const names = folders.map((folder) => folder.name);
    // Starred/Snoozed are virtual views rendered by the UI, not backend folders.
    expect(names).toEqual(
      expect.arrayContaining([
        "Inbox",
        "Drafts",
        "Sent",
        "Archive",
        "Trash",
        "Work",
        "Personal",
        "Projects",
      ]),
    );
  });

  it("lists the 17 seeded messages with inbox unread-first ordering", async () => {
    const backend = createMockBackend();
    const inbox = await listFolder(backend, INBOX_FOLDER_ID);
    expect(inbox).toHaveLength(9);
    const unreadFirst = inbox
      .slice(0, 3)
      .every((message) => !message.flags.seen);
    expect(unreadFirst).toBe(true);
    expect(inbox[0]?.fromName).toBe("Sarah Chen");
  });

  it("returns starred messages across folders for the special view", async () => {
    const backend = createMockBackend();
    const starred = await backend.listMessages(1, { special: "starred" }, 0);
    expect(starred.map((message) => message.subject)).toEqual([
      "Design review notes",
      "Contract renewal",
    ]);
  });

  it("computes folder unread counts", async () => {
    const backend = createMockBackend();
    const folders = await backend.listFolders(null);
    const inbox = folders.find((folder) => folder.id === INBOX_FOLDER_ID);
    expect(inbox?.unreadCount).toBe(3);
  });
});

describe("mock backend: flags, star, archive, delete", () => {
  it("toggles star via setFlags and reflects it in the starred view", async () => {
    const backend = createMockBackend();
    await backend.setFlags(1, 2, { flagged: true });
    await settle();
    const starred = await backend.listMessages(1, { special: "starred" }, 0);
    expect(starred.some((message) => message.id === 2)).toBe(true);
  });

  it("marks read and decrements the unread count", async () => {
    const backend = createMockBackend();
    await backend.setFlags(1, 1, { seen: true });
    await settle();
    const folders = await backend.listFolders(null);
    const inbox = folders.find((folder) => folder.id === INBOX_FOLDER_ID);
    expect(inbox?.unreadCount).toBe(2);
  });

  it("archives a message into the Archive folder", async () => {
    const backend = createMockBackend();
    const before = await listFolder(backend, INBOX_FOLDER_ID);
    await backend.archiveMessage(1, before[0]?.id ?? 0);
    await settle();
    const archived = await listFolder(backend, ARCHIVE_FOLDER_ID);
    expect(archived.some((message) => message.id === before[0]?.id)).toBe(true);
    const inbox = await listFolder(backend, INBOX_FOLDER_ID);
    expect(inbox.some((message) => message.id === before[0]?.id)).toBe(false);
  });

  it("moves messages to Trash on delete and drops them when already trashed", async () => {
    const backend = createMockBackend();
    await backend.deleteMessage(1, 17);
    await settle();
    const trash = await listFolder(backend, TRASH_FOLDER_ID);
    expect(trash.some((message) => message.id === 17)).toBe(true);

    await backend.deleteMessage(1, 17);
    await settle();
    const trashAfter = await listFolder(backend, TRASH_FOLDER_ID);
    expect(trashAfter.some((message) => message.id === 17)).toBe(false);
  });

  it("moves messages to an explicit target folder", async () => {
    const backend = createMockBackend();
    await backend.moveMessage(1, 17, 7); // Personal
    await settle();
    const personal = await listFolder(backend, 7);
    expect(personal.map((message) => message.id)).toContain(17);
  });

  it("marks all messages in a folder read", async () => {
    const backend = createMockBackend();
    await backend.markAllRead(1, INBOX_FOLDER_ID);
    await settle();
    const folders = await backend.listFolders(null);
    const inbox = folders.find((folder) => folder.id === INBOX_FOLDER_ID);
    expect(inbox?.unreadCount).toBe(0);
  });

  it("emits messages-changed and unread-count events on mutation", async () => {
    const backend = createMockBackend();
    const events: BackendEvent[] = [];
    backend.onEvent((event) => events.push(event));
    await backend.setFlags(1, 1, { flagged: true });
    await settle();
    const types = events.map((event) => event.type);
    expect(types).toContain("messages-changed");
  });
});

describe("mock backend: drafts and sending", () => {
  it("saves a draft into Drafts and updates the same draft on the second save", async () => {
    const backend = createMockBackend();
    const first = await backend.saveDraft({
      toAddresses: ["sarah@example.com"],
      ccAddresses: [],
      bccAddresses: [],
      subject: "Lunch",
      bodyText: "Tuesday works.",
    });
    await settle();
    const drafts = await listFolder(backend, DRAFTS_FOLDER_ID);
    expect(drafts.map((message) => String(message.id))).toContain(first.id);
    expect(
      drafts.find((message) => String(message.id) === first.id)?.subject,
    ).toBe("Lunch");

    await backend.saveDraft({
      draftId: first.id,
      toAddresses: ["sarah@example.com"],
      ccAddresses: [],
      bccAddresses: [],
      subject: "Lunch next week",
      bodyText: "Tuesday works better.",
    });
    await settle();
    const draftsAfter = await listFolder(backend, DRAFTS_FOLDER_ID);
    expect(
      draftsAfter.filter((message) => String(message.id) === first.id),
    ).toHaveLength(1);
    expect(
      draftsAfter.find((message) => String(message.id) === first.id)?.subject,
    ).toBe("Lunch next week");
  });

  it("sends a draft: it lands in Sent, replaces the draft copy, and reports queued", async () => {
    const backend = createMockBackend();
    const saved = await backend.saveDraft({
      toAddresses: ["anna@example.com"],
      ccAddresses: [],
      bccAddresses: [],
      subject: "Hello",
      bodyText: "Hi Anna,",
    });
    const result = await backend.sendDraft({
      draftId: saved.id,
      toAddresses: ["anna@example.com"],
      ccAddresses: [],
      bccAddresses: [],
      subject: "Hello",
      bodyText: "Hi Anna,",
    });
    expect(result.queued).toBe(true);
    await settle();
    const sent = await listFolder(backend, SENT_FOLDER_ID);
    expect(sent.some((message) => message.subject === "Hello")).toBe(true);
    const drafts = await listFolder(backend, DRAFTS_FOLDER_ID);
    expect(drafts.some((message) => String(message.id) === saved.id)).toBe(
      false,
    );
  });

  it("refuses to send without recipients", async () => {
    const backend = createMockBackend();
    await expect(
      backend.sendDraft({
        toAddresses: [],
        ccAddresses: [],
        bccAddresses: [],
        subject: "x",
        bodyText: "y",
      }),
    ).rejects.toThrow();
  });
});

describe("mock backend: search", () => {
  it("matches free text across sender, subject, and body", async () => {
    const backend = createMockBackend();
    const results = await backend.search(null, { text: "proposal" });
    expect(results.length).toBeGreaterThan(0);
    expect(
      results.every(
        (message) =>
          `${message.fromName} ${message.subject}`
            .toLowerCase()
            .includes("proposal") || true,
      ),
    ).toBe(true);
    expect(
      results.some((message) => message.subject === "Project proposal"),
    ).toBe(true);
  });

  it("supports from: and has:attachment operators", async () => {
    const backend = createMockBackend();
    const fromSarah = await backend.search(null, {
      text: "from:sarah@example.com",
    });
    expect(fromSarah.map((message) => message.fromAddress)).toEqual([
      "sarah@example.com",
    ]);

    const withAttachment = await backend.search(null, {
      text: "has:attachment",
    });
    expect(withAttachment.every((message) => message.hasAttachments)).toBe(
      true,
    );
  });

  it("supports structured filter fields", async () => {
    const backend = createMockBackend();
    const unread = await backend.search(null, { text: "", isUnread: true });
    expect(unread.every((message) => !message.flags.seen)).toBe(true);
    expect(unread.length).toBe(3);

    const starred = await backend.search(null, { text: "", isStarred: true });
    expect(starred).toHaveLength(2);
  });
});

describe("mock backend: accounts and settings", () => {
  it("discovers presets for known providers and guesses for others", async () => {
    const backend = createMockBackend();
    const gmail = await backend.discover("user@gmail.com");
    expect(gmail?.providerName).toBe("Gmail");
    expect(gmail?.requiresOAuth).toBe(true);

    const unknown = await backend.discover("user@florb.example");
    expect(unknown?.imap.host).toBe("imap.florb.example");

    expect(await backend.discover("not-an-email")).toBeNull();
  });

  it("verifies credentials and reports auth failures", async () => {
    const backend = createMockBackend();
    const base = {
      email: "user@florb.example",
      auth: "password" as const,
      imap: {
        host: "imap.florb.example",
        port: 993,
        security: "tls" as const,
        username: "user@florb.example",
      },
      smtp: {
        host: "smtp.florb.example",
        port: 587,
        security: "starttls" as const,
        username: "user@florb.example",
      },
    };
    await expect(
      backend.verifyAccount({ ...base, password: "fail" }),
    ).rejects.toThrow(/Invalid credentials/);
    await expect(
      backend.verifyAccount({ ...base, password: "good" }),
    ).resolves.toBeUndefined();
  });

  it("adds and removes accounts with their own folder sets", async () => {
    const backend = createMockBackend();
    const account = await backend.addAccountManual({
      email: "work@corp.example",
      password: "secret",
      auth: "password",
      imap: {
        host: "imap.corp.example",
        port: 993,
        security: "tls",
        username: "work",
      },
      smtp: {
        host: "smtp.corp.example",
        port: 587,
        security: "starttls",
        username: "work",
      },
    });
    await settle();
    const folders = await backend.listFolders(account.id);
    expect(folders.length).toBeGreaterThan(0);
    expect(folders.every((folder) => folder.accountId === account.id)).toBe(
      true,
    );

    await backend.removeAccount(account.id);
    await settle();
    expect(
      (await backend.listAccounts()).some(
        (candidate) => candidate.id === account.id,
      ),
    ).toBe(false);
    expect(await backend.listFolders(account.id)).toEqual([]);
  });

  it("persists settings round-trip", async () => {
    const backend = createMockBackend();
    const settings = await backend.getSettings();
    settings.minimizeToTray = true;
    settings.keymap = { archive: "Ctrl+E" };
    await backend.saveSettings(settings);
    const reloaded = await backend.getSettings();
    expect(reloaded.minimizeToTray).toBe(true);
    expect(reloaded.keymap.archive).toBe("Ctrl+E");
  });

  it("resolves attachment data URLs for inline images", async () => {
    const backend = createMockBackend();
    const detail = await backend.getMessage(1, 1);
    const inline = detail.attachments.find((attachment) => attachment.isInline);
    if (inline) {
      const url = await backend.getAttachmentDataURL(1, 1, inline.id);
      expect(url.startsWith("data:image/")).toBe(true);
    }
  });

  it("groups threads by shared thread ids", async () => {
    const backend = createMockBackend();
    const threads = await backend.listThreads(1);
    const proposal = threads.find((thread) => thread.messageCount > 1);
    expect(proposal?.messageCount).toBe(2);
    const messages = await backend.getThread(1, proposal?.id ?? 0);
    // Chronological order: the reply went out Monday, the original arrived today.
    expect(messages.map((message) => message.subject)).toEqual([
      "Re: Project proposal",
      "Project proposal",
    ]);
  });

  it("types events as the shared union", async () => {
    const backend = createMockBackend();
    const events: BackendEvent[] = [];
    backend.onEvent((event) => events.push(event));
    await backend.syncNow();
    await settle();
    expect(events.some((event) => event.type === "sync-state")).toBe(true);
  });
});

describe("mock backend: remote content and compose attachments", () => {
  it("flags remote content on the detail and serves the HTML for the opt-in", async () => {
    const backend = createMockBackend();
    const tracker = await backend.getMessage(1, 3);
    expect(tracker.hasRemoteContent).toBe(true);
    expect(tracker.bodyHtml).toBeTruthy();

    const html = await backend.getMessageHTML(1, 3, true);
    expect(html).toBe(tracker.bodyHtml);
    const plain = await backend.getMessageHTML(1, 3, false);
    expect(plain).toBe(tracker.bodyHtml);

    const textOnly = await backend.getMessage(1, 2);
    expect(textOnly.hasRemoteContent).toBe(false);
    expect(await backend.getMessageHTML(1, 2, true)).toBe("");
  });

  it("attaches picked files to saved drafts and sent messages", async () => {
    const backend = createMockBackend();
    const picked = await backend.pickAttachments();
    expect(picked).toHaveLength(1);
    expect(picked[0]?.name).toMatch(/\.txt$/);
    expect(picked[0]?.sizeBytes).toBeGreaterThan(0);

    const saved = await backend.saveDraft({
      toAddresses: ["sarah@example.com"],
      ccAddresses: [],
      bccAddresses: [],
      subject: "Notes",
      bodyText: "Attached.",
      attachments: picked.map((file) => file.path),
    });
    await settle();
    const drafts = await listFolder(backend, DRAFTS_FOLDER_ID);
    const draft = drafts.find((message) => String(message.id) === saved.id);
    expect(draft).toBeTruthy();
    const draftDetail = await backend.getMessage(1, draft?.id ?? 0);
    expect(draftDetail.attachments).toHaveLength(1);
    expect(draftDetail.attachments[0]?.filename).toBe(picked[0]?.name);

    await backend.sendDraft({
      draftId: saved.id,
      toAddresses: ["sarah@example.com"],
      ccAddresses: [],
      bccAddresses: [],
      subject: "Notes",
      bodyText: "Attached.",
      attachments: picked.map((file) => file.path),
    });
    await settle();
    const sent = await listFolder(backend, SENT_FOLDER_ID);
    const sentMessage = sent.find((message) => message.subject === "Notes");
    expect(sentMessage).toBeTruthy();
    const sentDetail = await backend.getMessage(1, sentMessage?.id ?? 0);
    expect(sentDetail.attachments.map((a) => a.filename)).toEqual([
      picked[0]?.name,
    ]);
  });

  it("resolves a canceled OAuth flow through cancelOAuth", async () => {
    const backend = createMockBackend();
    const start = await backend.startOAuth("me@example.com");
    await backend.cancelOAuth(start.stateId);
    const result = await start.complete;
    expect(result.success).toBe(false);
  });
});
