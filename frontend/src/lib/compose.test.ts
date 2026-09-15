import { describe, expect, it } from "vitest";
import {
  buildComposePrefill,
  formatAddress,
  isAddressLike,
  parseRecipients,
  splitAddress,
} from "./compose";
import type { MessageDetail } from "./types";

const message: MessageDetail = {
  id: 1,
  folderId: 1,
  threadId: 101,
  fromName: "Sarah Chen",
  fromAddress: "sarah@example.com",
  toAddresses: ["me@example.com", "marcus@example.com"],
  ccAddresses: ["priya@example.com"],
  subject: "Re: Project proposal",
  dateIso: new Date(2026, 8, 14, 14, 32).toISOString(),
  flags: { seen: true, flagged: false, answered: false, draft: false },
  hasAttachments: false,
  snippet: "Hey,",
  attachments: [],
  bodyText: "Hey,\nSounds good.",
};

describe("buildComposePrefill", () => {
  it("returns empty fields for a new message", () => {
    expect(buildComposePrefill("new", message, "me@example.com")).toEqual({
      toAddresses: [],
      ccAddresses: [],
      subject: "",
      bodyText: "",
    });
  });

  it("replies to the sender with a quoted body", () => {
    const prefill = buildComposePrefill("reply", message, "me@example.com");
    expect(prefill.toAddresses).toEqual(["sarah@example.com"]);
    expect(prefill.subject).toBe("Re: Project proposal");
    expect(prefill.bodyText).toContain(">");
  });

  it("replies all to sender plus recipients minus self", () => {
    const prefill = buildComposePrefill("replyAll", message, "me@example.com");
    expect(prefill.toAddresses).toEqual([
      "sarah@example.com",
      "marcus@example.com",
    ]);
    expect(prefill.ccAddresses).toEqual(["priya@example.com"]);
  });

  it("forwards with a Fwd subject and nobody addressed", () => {
    const prefill = buildComposePrefill("forward", message, "me@example.com");
    expect(prefill.toAddresses).toEqual([]);
    expect(prefill.subject).toBe("Fwd: Project proposal");
    expect(prefill.bodyText).toContain("Forwarded message");
  });
});

describe("recipient parsing", () => {
  it("splits on commas, semicolons, and whitespace", () => {
    expect(parseRecipients("a@b.com; c@d.com,e@f.com g@h.com")).toEqual([
      "a@b.com",
      "c@d.com",
      "e@f.com",
      "g@h.com",
    ]);
  });

  it("deduplicates case-insensitively", () => {
    expect(parseRecipients("A@B.com a@b.com")).toEqual(["a@b.com"]);
  });

  it("rejects values without a TLD", () => {
    expect(isAddressLike("sarah")).toBe(false);
    expect(isAddressLike("sarah@example.com")).toBe(true);
  });
});

describe("address formatting", () => {
  it("formats named contacts as Name <address>", () => {
    expect(formatAddress("Sarah Chen", "sarah@example.com")).toBe(
      "Sarah Chen <sarah@example.com>",
    );
  });

  it("keeps bare addresses bare", () => {
    expect(formatAddress("", "sarah@example.com")).toBe("sarah@example.com");
  });

  it("splits formatted addresses back apart", () => {
    expect(splitAddress("Sarah Chen <sarah@example.com>")).toEqual({
      name: "Sarah Chen",
      address: "sarah@example.com",
    });
    expect(splitAddress("sarah@example.com")).toEqual({
      name: "",
      address: "sarah@example.com",
    });
  });
});
