import type { Backend } from "./backend";
import type { BackendEvent, Unsubscribe } from "./types";
import type {
  Account,
  AppSettings,
  Attachment,
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
  ServerConfig,
  SyncActivityEntry,
  ThreadSummary,
} from "./types";
import { hasRemoteContent } from "./msghtml";

interface MockMessage {
  id: number;
  folderId: number;
  threadId: number;
  fromName: string;
  fromAddress: string;
  toAddresses: string[];
  ccAddresses: string[];
  subject: string;
  date: Date;
  seen: boolean;
  flagged: boolean;
  answered: boolean;
  isDraft: boolean;
  attachments: Attachment[];
  /** Plain-text paragraphs, matching the mockup reading pane. */
  paragraphs: string[];
  snippet: string;
  /** Hand-written sample HTML for messages that demo the sandboxed frame. */
  html?: string;
  inReplyTo?: string;
  references?: string[];
}

const KB = 1024;
const MB = 1024 * 1024;

let attachmentSeq = 1;
function makeAttachment(
  filename: string,
  mimeType: string,
  sizeBytes: number,
): Attachment {
  const attachment: Attachment = {
    id: attachmentSeq++,
    filename,
    mimeType,
    sizeBytes,
    contentHash: `hash-${filename}`,
    isInline: false,
    fetchState: "fetched",
  };
  return attachment;
}

function makeInlineAttachment(contentId: string, filename: string): Attachment {
  const attachment: Attachment = {
    id: attachmentSeq++,
    filename,
    mimeType: "image/svg+xml",
    sizeBytes: 4210,
    contentHash: `hash-${contentId}`,
    isInline: true,
    contentId,
    fetchState: "fetched",
  };
  return attachment;
}

/** Small inline chart used to demo cid: resolution. */
function chartDataUrl(): string {
  const svg =
    '<svg xmlns="http://www.w3.org/2000/svg" width="360" height="140">' +
    '<rect width="360" height="140" fill="#faf6ee"/>' +
    '<rect x="30" y="80" width="40" height="40" fill="#B23A2E"/>' +
    '<rect x="90" y="55" width="40" height="65" fill="#E3A857"/>' +
    '<rect x="150" y="35" width="40" height="85" fill="#B23A2E"/>' +
    '<rect x="210" y="20" width="40" height="100" fill="#E3A857"/>' +
    '<path d="M20 120h320" stroke="#8a8a90" stroke-width="1"/>' +
    '<text x="30" y="132" font-family="Arial" font-size="11" fill="#555">Adoption by week</text>' +
    "</svg>";
  return `data:image/svg+xml;base64,${btoa(svg)}`;
}

const PDF_DATA_URL =
  "data:application/pdf;base64,JVBERi0xLjQKJcTl8uXrp/Og0MTGCjEgMCBvYmoK";

function at(base: Date, hours: number, minutes: number): Date {
  const date = new Date(base);
  date.setHours(hours, minutes, 0, 0);
  return date;
}

function daysAgo(
  base: Date,
  days: number,
  hours: number,
  minutes: number,
): Date {
  const date = new Date(base);
  date.setDate(date.getDate() - days);
  return at(date, hours, minutes);
}

function snippetFrom(paragraphs: string[]): string {
  const text = paragraphs.join(" ").replace(/\s+/g, " ").trim();
  return text.length > 96 ? `${text.slice(0, 95)}…` : text;
}

interface MockFolderSpec {
  id: number;
  name: string;
  type: Folder["type"];
  imapPath: string;
}

const FOLDER_SPECS: MockFolderSpec[] = [
  { id: 1, name: "Inbox", type: "inbox", imapPath: "INBOX" },
  { id: 2, name: "Drafts", type: "drafts", imapPath: "INBOX/Drafts" },
  { id: 3, name: "Sent", type: "sent", imapPath: "INBOX/Sent" },
  { id: 4, name: "Archive", type: "archive", imapPath: "INBOX/Archive" },
  { id: 5, name: "Trash", type: "trash", imapPath: "INBOX/Trash" },
  { id: 6, name: "Work", type: "user", imapPath: "INBOX/Work" },
  { id: 7, name: "Personal", type: "user", imapPath: "INBOX/Personal" },
  { id: 8, name: "Projects", type: "user", imapPath: "INBOX/Projects" },
];

/**
 * In-memory Backend with the mockup's exact sample data: the 17 messages,
 * the virtual Starred/Snoozed views, and user folders. Mutations work over
 * the same in-memory store and push events like the real engine would.
 */
class MockBackend implements Backend {
  private accounts: Account[] = [];
  private folders: Folder[] = [];
  private messages: MockMessage[] = [];
  private settings: AppSettings = {
    notificationsEnabled: true,
    minimizeToTray: false,
    verboseLogging: false,
    attachmentEagerThresholdBytes: 1 * MB,
    keymap: {},
  };
  private listeners = new Set<(event: BackendEvent) => void>();
  private activity: SyncActivityEntry[] = [];
  private nextMessageId = 3000;
  private nextAccountId = 2;
  private nextPickedFileId = 1;
  private nextOAuthFlowId = 1;
  /** Synthetic picked files by path, so draft/sending can resolve the chips. */
  private pickedFiles = new Map<string, { name: string; sizeBytes: number }>();
  /** Pending mock OAuth flows by state id, so cancel can resolve them. */
  private oauthWaiters = new Map<string, (result: OAuthResult) => void>();
  private inlineImages = new Map<string, string>();

  constructor() {
    this.accounts.push({
      id: 1,
      email: "me@example.com",
      displayName: "Me",
      isDefault: true,
      paused: false,
      color: "#5f8f5f",
    });
    this.folders = FOLDER_SPECS.map((spec) => ({
      id: spec.id,
      accountId: 1,
      name: spec.name,
      imapPath: spec.imapPath,
      type: spec.type,
      unreadCount: 0,
      totalCount: 0,
    }));
    const chart = makeInlineAttachment(
      "chart@example.com",
      "adoption-chart.svg",
    );
    this.inlineImages.set("chart@example.com", chartDataUrl());
    this.messages = this.seedMessages(chart);
    this.refreshCounts();
  }

  // ---------- sample data ----------

  private seedMessages(chart: Attachment): MockMessage[] {
    const now = new Date();
    // Most recent Monday within the past week, like the mockup's "Mon" rows.
    const mondayOffset = (now.getDay() + 6) % 7 || 7;

    const inbox = (
      id: number,
      threadId: number,
      fromName: string,
      fromAddress: string,
      subject: string,
      paragraphs: string[],
      date: Date,
      options: Partial<MockMessage> = {},
    ): MockMessage => ({
      id,
      folderId: 1,
      threadId,
      fromName,
      fromAddress,
      toAddresses: ["me@example.com"],
      ccAddresses: [],
      subject,
      date,
      seen: true,
      flagged: false,
      answered: false,
      isDraft: false,
      attachments: [],
      paragraphs,
      snippet: snippetFrom(paragraphs),
      ...options,
    });

    const messages: MockMessage[] = [
      inbox(
        1,
        101,
        "Sarah Chen",
        "sarah@example.com",
        "Project proposal",
        [
          "Hey,",
          "Here’s the updated proposal based on our discussion yesterday. I’ve tightened the pricing section and added the implementation timeline you asked for.",
          "The main change is moving the migration work into phase two, which keeps the first milestone smaller and easier to review. Let me know if that works on your end.",
          "Thanks,\nSarah",
        ],
        at(now, 14, 32),
        {
          seen: false,
          attachments: [
            makeAttachment("proposal-v3.pdf", "application/pdf", 2.4 * MB),
            chart,
          ],
          html:
            "<p>Hey,</p><p>Here’s the updated proposal — adoption so far:</p>" +
            '<img src="cid:chart@example.com" alt="Adoption by week" width="360" height="140">' +
            "<p>The main change is moving the migration work into <b>phase two</b>.</p><p>Thanks,<br>Sarah</p>",
        },
      ),
      inbox(
        2,
        102,
        "Marcus Lee",
        "marcus@example.com",
        "Re: deployment",
        [
          "Looks good to me. I’ll merge it tomorrow morning once CI is green again.",
          "One nit: the retry backoff is currently capped at five minutes — worth confirming that’s what we want for the send queue too.",
          "Marcus",
        ],
        at(now, 13, 18),
      ),
      inbox(
        3,
        103,
        "GitHub",
        "noreply@github.com",
        "Security alert",
        [
          "We detected a new sign-in to your account from an unrecognized device.",
          "If this was you, no action is needed. If you don’t recognize this activity, please secure your account.",
          "The GitHub Team",
        ],
        at(now, 11, 2),
        {
          html:
            '<div style="font-family:Arial,sans-serif;color:#24292f">' +
            '<img src="https://tracker.example.com/pixel.gif" width="1" height="1" alt="">' +
            '<h2 style="margin:0 0 8px;color:#cf222e">Security alert</h2>' +
            "<p>We detected a new sign-in to your account from an unrecognized device.</p>" +
            "<p>If this was you, no action is needed.</p>" +
            '<p><a href="https://example.com/secure" style="background:#1f883d;color:#fff;padding:8px 14px;border-radius:6px;text-decoration:none">Secure your account</a></p>' +
            '<p style="color:#57606a">The GitHub Team</p></div>',
        },
      ),
      inbox(
        4,
        104,
        "Priya Nair",
        "priya@example.com",
        "Design review notes",
        [
          "A few thoughts from this morning’s session.",
          "Density is close, but I’d keep the reading width around 700px so long emails don’t turn into full-width lines. Motion should stay almost invisible.",
          "I’ve attached nothing this time — just notes.",
          "Priya",
        ],
        at(now, 10, 14),
        { seen: false, flagged: true },
      ),
      inbox(
        5,
        105,
        "Linear",
        "notifications@linear.app",
        "Your weekly digest",
        [
          "Here’s what moved this week.",
          "12 issues completed, 4 in progress, 2 waiting on review.",
          "Open Linear for the full breakdown.",
        ],
        at(now, 9, 40),
        {
          html:
            '<div style="font-family:Arial,sans-serif">' +
            '<img src="https://cdn.example.com/banner.png" alt="Weekly digest" width="500" height="90">' +
            "<p><strong>Here’s what moved this week.</strong></p>" +
            '<table style="border-collapse:collapse"><tr><td style="padding:4px 12px;border:1px solid #ddd">12 completed</td>' +
            '<td style="padding:4px 12px;border:1px solid #ddd">4 in progress</td>' +
            '<td style="padding:4px 12px;border:1px solid #ddd">2 in review</td></tr></table>' +
            "</div>",
        },
      ),
      inbox(
        6,
        106,
        "Daniel Okafor",
        "daniel@example.com",
        "Contract renewal",
        [
          "Hi,",
          "Attaching the updated contract for the 2027 term. Nothing major changed — just the renewal date and the updated support hours.",
          "Could you sign and send it back by Friday?",
          "Daniel",
        ],
        daysAgo(now, 1, 17, 20),
        {
          seen: false,
          flagged: true,
          attachments: [
            makeAttachment("contract-2027.pdf", "application/pdf", 840 * KB),
          ],
        },
      ),
      inbox(
        7,
        107,
        "Stripe",
        "receipts@stripe.com",
        "Payment received",
        [
          "A payment of $249.00 from Acme was deposited to your account.",
          "No action is required.",
        ],
        daysAgo(now, 1, 15, 2),
      ),
      inbox(
        8,
        108,
        "Anna Kowalski",
        "anna@example.com",
        "Lunch next week?",
        [
          "Are you around Tuesday or Wednesday? There’s a new place near the office I’ve been meaning to try.",
          "Anna",
        ],
        daysAgo(now, 1, 12, 11),
      ),
      inbox(
        9,
        109,
        "CI Bot",
        "ci@example.com",
        "Build #4821 failed",
        [
          "The integration suite failed on linux/amd64 in 4m 12s.",
          "Failing test: TestSyncConflictReplay — expected queued action to be dropped after remote divergence.",
          "See the run for the full log.",
        ],
        daysAgo(now, mondayOffset, 22, 5),
      ),

      {
        ...inbox(
          10,
          101,
          "Me",
          "me@example.com",
          "Re: Project proposal",
          [
            "Sounds good — I’ll review phase two and get back to you tomorrow.",
            "Me",
          ],
          daysAgo(now, mondayOffset, 18, 44),
        ),
        folderId: 3,
        answered: true,
        inReplyTo: "<proposal-original@example.com>",
        references: ["<proposal-original@example.com>"],
      },
      {
        ...inbox(
          11,
          111,
          "Me",
          "me@example.com",
          "Deployment checklist",
          ["Here’s the checklist we agreed on for the release.", "Me"],
          daysAgo(now, Math.max(mondayOffset - 4, 1), 9, 30),
        ),
        folderId: 3,
      },
      {
        ...inbox(
          12,
          112,
          "Draft",
          "me@example.com",
          "Q4 planning",
          ["Rough outline for the Q4 planning doc…"],
          at(now, 10, 2),
        ),
        folderId: 2,
        isDraft: true,
      },
      {
        ...inbox(
          13,
          113,
          "Draft",
          "me@example.com",
          "Invoice follow-up",
          ["Following up on the invoice from last month…"],
          daysAgo(now, mondayOffset, 16, 12),
        ),
        folderId: 2,
        isDraft: true,
      },
      {
        ...inbox(
          14,
          114,
          "Thom Reyes",
          "thom@example.com",
          "Old thread: server migration",
          [
            "Wrapping this up — the migration is done. Thanks for the help.",
            "Thom",
          ],
          daysAgo(now, 17, 11, 8),
        ),
        folderId: 4,
      },
      {
        ...inbox(
          15,
          115,
          "GitHub",
          "noreply@github.com",
          "Release published",
          ["v0.4.0 has been published."],
          daysAgo(now, 31, 19, 45),
        ),
        folderId: 4,
      },
      {
        ...inbox(
          16,
          116,
          "Winner",
          "offers@example.com",
          "You’ve won!",
          ["Click here to claim your prize."],
          daysAgo(now, 43, 6, 30),
        ),
        folderId: 5,
      },
      {
        ...inbox(
          17,
          117,
          "Jamie Fox",
          "jamie@example.com",
          "Standup notes",
          ["Blocked on the API contract, otherwise fine.", "Jamie"],
          at(now, 9, 12),
        ),
        folderId: 6,
      },
    ];
    return messages;
  }

  // ---------- plumbing ----------

  private async latency(multiplier = 1): Promise<void> {
    const ms = (10 + Math.random() * 20) * multiplier;
    await new Promise((resolve) => setTimeout(resolve, ms));
  }

  private emit(event: BackendEvent): void {
    for (const listener of this.listeners) listener(event);
  }

  onEvent(handler: (event: BackendEvent) => void): Unsubscribe {
    this.listeners.add(handler);
    return () => {
      this.listeners.delete(handler);
    };
  }

  private refreshCounts(): void {
    for (const folder of this.folders) {
      const messages = this.messages.filter(
        (message) => message.folderId === folder.id,
      );
      folder.totalCount = messages.length;
      folder.unreadCount = messages.filter(
        (message) => !message.seen && !message.isDraft,
      ).length;
    }
  }

  private accountForMessage(message: MockMessage): Account {
    const folder = this.folders.find(
      (candidate) => candidate.id === message.folderId,
    );
    return (
      this.accounts.find((account) => account.id === folder?.accountId) ??
      (this.accounts[0] as Account)
    );
  }

  private accountById(accountId: number): Account {
    const account = this.accounts.find(
      (candidate) => candidate.id === accountId,
    );
    if (!account) throw new Error(`mock: unknown account ${accountId}`);
    return account;
  }

  private findMessage(messageId: number): MockMessage {
    const message = this.messages.find(
      (candidate) => candidate.id === messageId,
    );
    if (!message) throw new Error(`mock: message ${messageId} not found`);
    return message;
  }

  private toSummary(message: MockMessage): MessageSummary {
    const account = this.accountForMessage(message);
    return {
      id: message.id,
      folderId: message.folderId,
      threadId: message.threadId,
      fromName: message.fromName,
      fromAddress: message.fromAddress,
      subject: message.subject,
      dateIso: message.date.toISOString(),
      flags: {
        seen: message.seen,
        flagged: message.flagged,
        answered: message.answered,
        draft: message.isDraft,
      },
      hasAttachments: message.attachments.some(
        (attachment) => !attachment.isInline,
      ),
      accountColor: account?.color,
      snippet: message.snippet,
    };
  }

  private toDetail(message: MockMessage): MessageDetail {
    return {
      ...this.toSummary(message),
      toAddresses: message.toAddresses,
      ccAddresses: message.ccAddresses,
      bodyText: message.paragraphs.join("\n\n"),
      bodyHtml: message.html,
      attachments: message.attachments.map((attachment) => ({ ...attachment })),
      inReplyTo: message.inReplyTo,
      references: message.references ? [...message.references] : undefined,
      hasRemoteContent: message.html ? hasRemoteContent(message.html) : false,
    };
  }

  private defaultAccount(): Account {
    return (
      this.accounts.find((account) => account.isDefault) ??
      (this.accounts[0] as Account)
    );
  }

  // ---------- accounts ----------

  async listAccounts(): Promise<Account[]> {
    await this.latency();
    return this.accounts.map((account) => ({ ...account }));
  }

  async discover(email: string): Promise<DiscoveredConfig | null> {
    await this.latency(6);
    const domain = email.split("@")[1]?.toLowerCase().trim();
    if (!domain || !email.includes("@")) return null;
    const imap = (
      host: string,
      port: number,
      username = email,
    ): ServerConfig => ({ host, port, security: "tls", username });
    const smtp = (
      host: string,
      port: number,
      username = email,
    ): ServerConfig => ({
      host,
      port,
      security: port === 465 ? "tls" : "starttls",
      username,
    });
    const presets: Record<string, Omit<DiscoveredConfig, "email">> = {
      "gmail.com": {
        providerName: "Gmail",
        requiresOAuth: true,
        imap: imap("imap.gmail.com", 993),
        smtp: smtp("smtp.gmail.com", 587),
        appPasswordUrl: "https://support.google.com/mail/answer/185833",
      },
      "googlemail.com": {
        providerName: "Gmail",
        requiresOAuth: true,
        imap: imap("imap.gmail.com", 993),
        smtp: smtp("smtp.gmail.com", 587),
        appPasswordUrl: "https://support.google.com/mail/answer/185833",
      },
      "outlook.com": {
        providerName: "Outlook / Microsoft 365",
        requiresOAuth: true,
        imap: imap("outlook.office365.com", 993),
        smtp: smtp("smtp.office365.com", 587),
      },
      "hotmail.com": {
        providerName: "Outlook / Microsoft 365",
        requiresOAuth: true,
        imap: imap("outlook.office365.com", 993),
        smtp: smtp("smtp.office365.com", 587),
      },
      "yahoo.com": {
        providerName: "Yahoo Mail",
        requiresOAuth: false,
        imap: imap("imap.mail.yahoo.com", 993),
        smtp: smtp("smtp.mail.yahoo.com", 465),
        appPasswordUrl:
          "https://help.yahoo.com/kb/generate-app-password-slm15221.html",
      },
      "icloud.com": {
        providerName: "iCloud Mail",
        requiresOAuth: false,
        imap: imap("imap.mail.me.com", 993),
        smtp: smtp("smtp.mail.me.com", 587),
        appPasswordUrl: "https://support.apple.com/102654",
      },
    };
    const preset = presets[domain];
    if (preset) return { email, ...preset };
    return {
      email,
      providerName: domain,
      requiresOAuth: false,
      imap: imap(`imap.${domain}`, 993),
      smtp: smtp(`smtp.${domain}`, 587),
    };
  }

  async addAccountManual(input: ManualAccountInput): Promise<Account> {
    await this.latency(3);
    const accountId = this.nextAccountId++;
    const palette = ["#5f8f5f", "#a4703c", "#5b7ea4", "#8a5ba4"];
    const account: Account = {
      id: accountId,
      email: input.email,
      displayName:
        input.displayName ?? input.email.split("@")[0] ?? input.email,
      isDefault: this.accounts.length === 0,
      paused: false,
      color: palette[(accountId - 1) % palette.length] as string,
    };
    this.accounts.push(account);
    const base = 100 * accountId;
    this.folders.push(
      ...FOLDER_SPECS.filter((spec) => spec.type !== "user").map(
        (spec, index) => ({
          id: base + index,
          accountId,
          name: spec.name,
          imapPath: spec.imapPath,
          type: spec.type,
          unreadCount: 0,
          totalCount: 0,
        }),
      ),
    );
    this.emit({ type: "folders-changed", accountId });
    return { ...account };
  }

  async verifyAccount(input: ManualAccountInput): Promise<void> {
    await this.latency(15);
    if (input.auth !== "oauth") {
      if (!input.password)
        throw new Error("Enter your password to verify the account.");
      if (input.password === "fail") {
        throw new Error(
          "Authentication failed: [AUTH] Invalid credentials. If 2FA is on, use an app password.",
        );
      }
    }
  }

  async startOAuth(
    email: string,
  ): Promise<{ url: string; stateId: string; complete: Promise<OAuthResult> }> {
    await this.latency();
    const stateId = `mock-oauth-${this.nextOAuthFlowId++}`;
    const complete = new Promise<OAuthResult>((resolve) => {
      this.oauthWaiters.set(stateId, resolve);
    });
    // Simulates the loopback redirect landing after the user consents.
    setTimeout(() => this.resolveOAuth(stateId, { success: true }), 1200);
    return {
      url: `https://accounts.example.com/oauth?user=${encodeURIComponent(email)}`,
      stateId,
      complete,
    };
  }

  async cancelOAuth(stateId: string): Promise<void> {
    await this.latency();
    this.resolveOAuth(stateId, { success: false, error: "Sign-in canceled." });
  }

  /** Resolves one pending mock OAuth flow; a repeat or unknown id is a no-op. */
  private resolveOAuth(stateId: string, result: OAuthResult): void {
    const resolve = this.oauthWaiters.get(stateId);
    if (!resolve) return;
    this.oauthWaiters.delete(stateId);
    resolve(result);
  }

  async removeAccount(accountId: number): Promise<void> {
    await this.latency();
    this.accounts = this.accounts.filter((account) => account.id !== accountId);
    const removedFolderIds = new Set(
      this.folders
        .filter((folder) => folder.accountId === accountId)
        .map((folder) => folder.id),
    );
    this.folders = this.folders.filter(
      (folder) => folder.accountId !== accountId,
    );
    this.messages = this.messages.filter(
      (message) => !removedFolderIds.has(message.folderId),
    );
    this.emit({ type: "folders-changed", accountId });
  }

  async setAccountPaused(accountId: number, paused: boolean): Promise<void> {
    await this.latency();
    const account = this.accountById(accountId);
    account.paused = paused;
    this.emit({
      type: "sync-state",
      accountId,
      state: paused ? "paused" : "syncing",
    });
    if (!paused)
      setTimeout(
        () => this.emit({ type: "sync-state", accountId, state: "idle" }),
        300,
      );
  }

  // ---------- folders and messages ----------

  async listFolders(accountId: number | null): Promise<Folder[]> {
    await this.latency();
    this.refreshCounts();
    const folders =
      accountId == null
        ? this.folders
        : this.folders.filter((folder) => folder.accountId === accountId);
    return folders.map((folder) => ({ ...folder }));
  }

  async listMessages(
    accountId: number | null,
    view: MessageView,
    page: number,
  ): Promise<MessageSummary[]> {
    await this.latency();
    this.refreshCounts();
    let list = this.messages.slice();
    if (view.special === "starred") {
      list = list.filter((message) => message.flagged);
    } else if (view.folderId != null) {
      list = list.filter((message) => message.folderId === view.folderId);
    }
    if (accountId != null) {
      const folderIds = new Set(
        this.folders
          .filter((folder) => folder.accountId === accountId)
          .map((folder) => folder.id),
      );
      list = list.filter((message) => folderIds.has(message.folderId));
    }
    const inboxFolder = this.folders.find((folder) => folder.type === "inbox");
    const unreadFirst =
      view.special === "starred" ||
      (view.folderId != null && view.folderId === inboxFolder?.id);
    list.sort((a, b) => {
      if (unreadFirst && a.seen !== b.seen) return a.seen ? 1 : -1;
      return b.date.getTime() - a.date.getTime();
    });
    const pageSize = 200;
    return list
      .slice(page * pageSize, (page + 1) * pageSize)
      .map((message) => this.toSummary(message));
  }

  async getMessage(
    accountId: number,
    messageId: number,
  ): Promise<MessageDetail> {
    await this.latency();
    void accountId;
    return this.toDetail(this.findMessage(messageId));
  }

  async getMessageHTML(
    accountId: number,
    messageId: number,
    allowRemote: boolean,
  ): Promise<string> {
    await this.latency(2);
    void accountId;
    void allowRemote;
    // The mock stores one sample HTML per message; the frame's CSP already
    // decides whether remote loads actually happen.
    return this.findMessage(messageId).html ?? "";
  }

  async setFlags(
    accountId: number,
    messageId: number,
    patch: { seen?: boolean; flagged?: boolean },
  ): Promise<void> {
    await this.latency();
    void accountId;
    const message = this.findMessage(messageId);
    if (patch.seen !== undefined) message.seen = patch.seen;
    if (patch.flagged !== undefined) message.flagged = patch.flagged;
    this.refreshCounts();
    this.emitMessageChanged(message);
  }

  async archiveMessage(accountId: number, messageId: number): Promise<void> {
    await this.latency();
    void accountId;
    await this.moveToFolder(this.findMessage(messageId), "archive");
  }

  async deleteMessage(accountId: number, messageId: number): Promise<void> {
    await this.latency();
    void accountId;
    const message = this.findMessage(messageId);
    const folder = this.folders.find(
      (candidate) => candidate.id === message.folderId,
    );
    if (folder?.type === "trash") {
      this.messages = this.messages.filter(
        (candidate) => candidate.id !== messageId,
      );
      this.refreshCounts();
      this.emitMessageChanged(message);
      return;
    }
    await this.moveToFolder(message, "trash");
  }

  async moveMessage(
    accountId: number,
    messageId: number,
    targetFolderId: number,
  ): Promise<void> {
    await this.latency();
    void accountId;
    const message = this.findMessage(messageId);
    message.folderId = targetFolderId;
    this.refreshCounts();
    this.emitMessageChanged(message);
  }

  private async moveToFolder(
    message: MockMessage,
    type: Folder["type"],
  ): Promise<void> {
    const target = this.folders.find(
      (folder) =>
        folder.type === type &&
        folder.accountId === this.accountForMessage(message).id,
    );
    if (!target) throw new Error(`mock: no ${type} folder`);
    message.folderId = target.id;
    this.refreshCounts();
    this.emitMessageChanged(message);
  }

  private emitMessageChanged(message: MockMessage): void {
    const account = this.accountForMessage(message);
    this.emit({
      type: "messages-changed",
      accountId: account.id,
      folderId: message.folderId,
    });
  }

  async markAllRead(accountId: number, folderId: number): Promise<void> {
    await this.latency();
    let changed = false;
    for (const message of this.messages) {
      if (message.folderId === folderId && !message.seen) {
        message.seen = true;
        changed = true;
      }
    }
    if (changed) {
      this.refreshCounts();
      const folder = this.folders.find(
        (candidate) => candidate.id === folderId,
      );
      if (folder) {
        this.emit({
          type: "unread-count",
          accountId,
          folderId,
          unreadCount: folder.unreadCount,
        });
      }
      this.emit({ type: "messages-changed", accountId, folderId });
    }
  }

  // ---------- search ----------

  async search(
    accountId: number | null,
    filter: SearchFilter,
  ): Promise<MessageSummary[]> {
    await this.latency();
    let list = this.messages.slice();
    if (accountId != null) {
      const folderIds = new Set(
        this.folders
          .filter((folder) => folder.accountId === accountId)
          .map((folder) => folder.id),
      );
      list = list.filter((message) => folderIds.has(message.folderId));
    }
    if (filter.isStarred) list = list.filter((message) => message.flagged);
    if (filter.isUnread) list = list.filter((message) => !message.seen);
    if (filter.hasAttachment)
      list = list.filter((message) => message.attachments.length > 0);
    if (filter.from)
      list = list.filter((message) =>
        this.matchesText(
          `${message.fromName} ${message.fromAddress}`,
          filter.from ?? "",
        ),
      );
    if (filter.to)
      list = list.filter((message) =>
        this.matchesText(message.toAddresses.join(" "), filter.to ?? ""),
      );
    if (filter.subject)
      list = list.filter((message) =>
        this.matchesText(message.subject, filter.subject ?? ""),
      );
    if (filter.folderName) {
      list = list.filter((message) => {
        const folder = this.folders.find(
          (candidate) => candidate.id === message.folderId,
        );
        return folder
          ? this.matchesText(folder.name, filter.folderName ?? "")
          : false;
      });
    }
    if (filter.afterIso) {
      const after = new Date(filter.afterIso).getTime();
      list = list.filter((message) => message.date.getTime() >= after);
    }
    if (filter.beforeIso) {
      const before = new Date(filter.beforeIso).getTime();
      list = list.filter((message) => message.date.getTime() <= before);
    }
    const { text, from, hasAttachment, isUnread } = this.parseOperators(
      filter.text,
    );
    if (text) {
      list = list.filter((message) => {
        const haystack = [
          message.fromName,
          message.fromAddress,
          message.subject,
          message.snippet,
          ...message.toAddresses,
          ...message.paragraphs,
        ].join(" ");
        return this.matchesText(haystack, text);
      });
    }
    if (from)
      list = list.filter((message) =>
        this.matchesText(`${message.fromName} ${message.fromAddress}`, from),
      );
    if (hasAttachment)
      list = list.filter((message) => message.attachments.length > 0);
    if (isUnread) list = list.filter((message) => !message.seen);
    list.sort((a, b) => b.date.getTime() - a.date.getTime());
    return list.map((message) => this.toSummary(message));
  }

  private parseOperators(raw: string): {
    text: string;
    from: string;
    hasAttachment: boolean;
    isUnread: boolean;
  } {
    let text = raw;
    let from = "";
    let hasAttachment = false;
    let isUnread = false;
    const fromMatch = text.match(/\bfrom:(\S+)/i);
    if (fromMatch) {
      from = fromMatch[1] ?? "";
      text = text.replace(fromMatch[0], " ");
    }
    const attachMatch = text.match(/\bhas:attachment\b/i);
    if (attachMatch) {
      hasAttachment = true;
      text = text.replace(attachMatch[0], " ");
    }
    const unreadMatch = text.match(/\bis:unread\b/i);
    if (unreadMatch) {
      isUnread = true;
      text = text.replace(unreadMatch[0], " ");
    }
    return {
      text: text.replace(/\s+/g, " ").trim(),
      from,
      hasAttachment,
      isUnread,
    };
  }

  private matchesText(haystack: string, needle: string): boolean {
    return haystack.toLowerCase().includes(needle.toLowerCase().trim());
  }

  // ---------- compose ----------

  async pickAttachments(): Promise<PickedFile[]> {
    await this.latency();
    // No real file system in the browser: hand out one synthetic text file
    // per pick, registered so saveDraft/sendDraft can resolve the chips.
    const id = this.nextPickedFileId++;
    const name = `meeting-notes-${id}.txt`;
    const sizeBytes = 4096 + id * 256;
    const path = `mock://attachments/${id}/${name}`;
    this.pickedFiles.set(path, { name, sizeBytes });
    return [{ name, path, sizeBytes }];
  }

  /** Resolves the draft's attachment paths onto attachment records. */
  private attachmentsFor(draft: DraftInput): Attachment[] {
    return (draft.attachments ?? []).map((path) => {
      const picked = this.pickedFiles.get(path);
      const name = picked?.name ?? path.split("/").pop() ?? path;
      return makeAttachment(name, "text/plain", picked?.sizeBytes ?? 0);
    });
  }

  async saveDraft(draft: DraftInput): Promise<{ id: string }> {
    await this.latency();
    const account =
      draft.accountId != null
        ? this.accountById(draft.accountId)
        : this.defaultAccount();
    const draftsFolder = this.folders.find(
      (folder) => folder.type === "drafts" && folder.accountId === account.id,
    );
    if (!draftsFolder) throw new Error("mock: no drafts folder for account");
    const paragraphs = draft.bodyText
      .split(/\n{2,}/)
      .filter((paragraph) => paragraph.trim().length > 0);
    const subject = draft.subject.trim() || "(no subject)";

    if (draft.draftId) {
      const existing = this.messages.find(
        (message) => String(message.id) === draft.draftId,
      );
      if (existing) {
        existing.subject = subject;
        existing.paragraphs = paragraphs.length ? paragraphs : [""];
        existing.snippet = snippetFrom(paragraphs);
        existing.toAddresses = draft.toAddresses;
        existing.ccAddresses = draft.ccAddresses;
        existing.attachments = this.attachmentsFor(draft);
        existing.date = new Date();
        this.refreshCounts();
        this.emitMessageChanged(existing);
        return { id: String(existing.id) };
      }
    }

    const id = this.nextMessageId++;
    const message: MockMessage = {
      id,
      folderId: draftsFolder.id,
      threadId: 900000 + id,
      fromName: "Draft",
      fromAddress: account.email,
      toAddresses: draft.toAddresses,
      ccAddresses: draft.ccAddresses,
      subject,
      date: new Date(),
      seen: true,
      flagged: false,
      answered: false,
      isDraft: true,
      attachments: this.attachmentsFor(draft),
      paragraphs: paragraphs.length ? paragraphs : [""],
      snippet: snippetFrom(paragraphs),
    };
    this.messages.push(message);
    this.refreshCounts();
    this.emitMessageChanged(message);
    return { id: String(id) };
  }

  async sendDraft(draft: DraftInput): Promise<{ queued: boolean }> {
    await this.latency(8);
    const account =
      draft.accountId != null
        ? this.accountById(draft.accountId)
        : this.defaultAccount();
    if (
      !draft.toAddresses.length &&
      !draft.ccAddresses.length &&
      !draft.bccAddresses.length
    ) {
      throw new Error("mock: no recipients");
    }
    const sentFolder = this.folders.find(
      (folder) => folder.type === "sent" && folder.accountId === account.id,
    );
    if (!sentFolder) throw new Error("mock: no sent folder for account");
    const id = this.nextMessageId++;
    const message: MockMessage = {
      id,
      folderId: sentFolder.id,
      threadId: 900000 + id,
      fromName: account.displayName,
      fromAddress: account.email,
      toAddresses: draft.toAddresses,
      ccAddresses: draft.ccAddresses,
      subject: draft.subject.trim() || "(no subject)",
      date: new Date(),
      seen: true,
      flagged: false,
      answered: false,
      isDraft: false,
      attachments: this.attachmentsFor(draft),
      paragraphs: draft.bodyText
        .split(/\n{2,}/)
        .filter((paragraph) => paragraph.trim().length > 0),
      snippet: snippetFrom(draft.bodyText.split(/\n{2,}/)),
    };
    this.messages.push(message);
    if (draft.draftId) {
      this.messages = this.messages.filter(
        (candidate) => String(candidate.id) !== draft.draftId,
      );
    }
    this.refreshCounts();
    this.emitMessageChanged(message);
    this.emit({
      type: "send-state",
      accountId: account.id,
      draftId: String(id),
      state: "sent",
    });
    return { queued: true };
  }

  // ---------- threads ----------

  async listThreads(
    accountId: number,
    folderId?: number,
  ): Promise<ThreadSummary[]> {
    await this.latency();
    const folderIds = new Set(
      this.folders
        .filter(
          (folder) =>
            folder.accountId === accountId &&
            (folderId == null || folder.id === folderId),
        )
        .map((folder) => folder.id),
    );
    const byThread = new Map<number, MockMessage[]>();
    for (const message of this.messages) {
      if (!folderIds.has(message.folderId)) continue;
      const bucket = byThread.get(message.threadId) ?? [];
      bucket.push(message);
      byThread.set(message.threadId, bucket);
    }
    const threads: ThreadSummary[] = [];
    for (const [id, messages] of byThread) {
      messages.sort((a, b) => a.date.getTime() - b.date.getTime());
      const first = messages[0] as MockMessage;
      const last = messages[messages.length - 1] as MockMessage;
      threads.push({
        id,
        subject: first.subject,
        messageCount: messages.length,
        lastDateIso: last.date.toISOString(),
        unreadCount: messages.filter((message) => !message.seen).length,
        hasAttachments: messages.some(
          (message) => message.attachments.length > 0,
        ),
        participants: [...new Set(messages.map((message) => message.fromName))],
      });
    }
    return threads;
  }

  async getThread(
    accountId: number,
    threadId: number,
  ): Promise<MessageSummary[]> {
    await this.latency();
    void accountId;
    return this.messages
      .filter((message) => message.threadId === threadId)
      .sort((a, b) => a.date.getTime() - b.date.getTime())
      .map((message) => this.toSummary(message));
  }

  // ---------- attachments ----------

  async getAttachmentDataURL(
    accountId: number,
    messageId: number,
    attachmentId: number,
  ): Promise<string> {
    await this.latency(2);
    void accountId;
    const message = this.findMessage(messageId);
    const attachment = message.attachments.find(
      (candidate) => candidate.id === attachmentId,
    );
    if (!attachment)
      throw new Error(`mock: attachment ${attachmentId} not found`);
    if (attachment.contentId) {
      const inline = this.inlineImages.get(attachment.contentId);
      if (inline) return inline;
    }
    if (attachment.mimeType === "image/svg+xml") return chartDataUrl();
    return PDF_DATA_URL;
  }

  async openAttachment(
    accountId: number,
    messageId: number,
    attachmentId: number,
  ): Promise<void> {
    await this.latency(3);
    const name = this.findAttachment(messageId, attachmentId).filename;
    this.emit({
      type: "toast",
      level: "info",
      message: `Opening ${name} (mock)`,
    });
  }

  async saveAttachment(
    accountId: number,
    messageId: number,
    attachmentId: number,
  ): Promise<void> {
    await this.latency(3);
    const name = this.findAttachment(messageId, attachmentId).filename;
    this.emit({
      type: "toast",
      level: "info",
      message: `Saved ${name} (mock)`,
    });
  }

  private findAttachment(messageId: number, attachmentId: number): Attachment {
    const attachment = this.findMessage(messageId).attachments.find(
      (candidate) => candidate.id === attachmentId,
    );
    if (!attachment)
      throw new Error(`mock: attachment ${attachmentId} not found`);
    return attachment;
  }

  // ---------- contacts, settings, sync ----------

  async listContacts(accountId: number, prefix: string): Promise<Contact[]> {
    await this.latency(0.5);
    void accountId;
    const seen = new Set<string>();
    const contacts: Contact[] = [];
    for (const message of this.messages) {
      for (const source of [message.fromAddress, ...message.toAddresses]) {
        if (seen.has(source)) continue;
        seen.add(source);
        const named = this.messages.find(
          (candidate) =>
            candidate.fromAddress === source &&
            candidate.fromName !== "Me" &&
            candidate.fromName !== "Draft",
        );
        contacts.push({ name: named?.fromName ?? "", address: source });
      }
    }
    const needle = prefix.toLowerCase().trim();
    return contacts
      .filter(
        (contact) =>
          !needle ||
          `${contact.name} ${contact.address}`.toLowerCase().includes(needle),
      )
      .filter((contact) => contact.address.includes("@"))
      .sort((a, b) => a.address.localeCompare(b.address))
      .slice(0, 8);
  }

  async getSettings(): Promise<AppSettings> {
    await this.latency();
    return structuredClone(this.settings);
  }

  async saveSettings(settings: AppSettings): Promise<void> {
    await this.latency();
    this.settings = structuredClone(settings);
  }

  async getSyncActivity(): Promise<SyncActivityEntry[]> {
    return this.activity.map((entry) => ({ ...entry }));
  }

  private record(
    accountId: number,
    text: string,
    level: SyncActivityEntry["level"],
  ): void {
    this.activity.push({
      at: new Date().toISOString(),
      accountId,
      text,
      level,
    });
    if (this.activity.length > 300)
      this.activity.splice(0, this.activity.length - 300);
  }

  private progress(
    accountId: number,
    phase: "pass-start" | "folder-start" | "folder-done" | "pass-done",
    folder: string,
    newCount: number,
    total: number,
  ): void {
    this.emit({
      type: "sync-progress",
      accountId,
      folder,
      phase,
      new: newCount,
      total,
      at: new Date().toISOString(),
    });
  }

  async syncNow(accountId?: number): Promise<void> {
    const targets =
      accountId == null
        ? this.accounts
        : this.accounts.filter((account) => account.id === accountId);
    for (const account of targets) {
      if (account.paused) continue;
      const folders = this.folders.filter(
        (folder) => folder.accountId === account.id,
      );
      this.emit({
        type: "sync-state",
        accountId: account.id,
        state: "syncing",
      });
      this.record(account.id, "sync started", "info");
      this.progress(account.id, "pass-start", "", 0, folders.length);
      folders.forEach((folder, index) => {
        setTimeout(
          () => {
            this.progress(
              account.id,
              "folder-start",
              folder.name,
              0,
              folder.totalCount,
            );
            this.progress(
              account.id,
              "folder-done",
              folder.name,
              0,
              folder.totalCount,
            );
            this.progress(account.id, "pass-done", "", 0, folders.length);
            this.emit({
              type: "sync-state",
              accountId: account.id,
              state: "idle",
            });
          },
          150 + index * 120,
        );
      });
      if (!folders.length) {
        setTimeout(
          () =>
            this.emit({
              type: "sync-state",
              accountId: account.id,
              state: "idle",
            }),
          400,
        );
      }
    }
    await this.latency();
  }
}

/** Builds a fresh MockBackend; every instance starts from the mockup sample data. */
export function createMockBackend(): Backend {
  return new MockBackend();
}
