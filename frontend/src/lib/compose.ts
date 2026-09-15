import type { ComposeMode, MessageDetail } from "./types";
import { formatDateFull, forwardSubject, replySubject } from "./format";

export interface ComposePrefill {
  toAddresses: string[];
  ccAddresses: string[];
  subject: string;
  bodyText: string;
}

function dedupe(addresses: string[]): string[] {
  return [...new Set(addresses.map((address) => address.trim().toLowerCase()))];
}

function withoutSelf(addresses: string[], selfAddress: string): string[] {
  const self = selfAddress.trim().toLowerCase();
  return addresses.filter((address) => address.trim().toLowerCase() !== self);
}

function quote(message: MessageDetail, header: string): string {
  const date = formatDateFull(new Date(message.dateIso));
  const lines = message.bodyText
    .split("\n")
    .map((line) => `> ${line}`)
    .join("\n");
  return `\n\n${header}\n${message.fromName} <${message.fromAddress}> — ${date}\n${lines}\n`;
}

/** Builds the initial compose fields for a mode, mirroring standard mail behavior. */
export function buildComposePrefill(
  mode: ComposeMode,
  message: MessageDetail | null,
  selfAddress: string,
): ComposePrefill {
  if (mode === "new" || !message) {
    return { toAddresses: [], ccAddresses: [], subject: "", bodyText: "" };
  }
  if (mode === "reply") {
    return {
      toAddresses: [message.fromAddress],
      ccAddresses: [],
      subject: replySubject(message.subject),
      bodyText: quote(message, "On reply:"),
    };
  }
  if (mode === "replyAll") {
    return {
      toAddresses: withoutSelf(
        dedupe([message.fromAddress, ...message.toAddresses]),
        selfAddress,
      ),
      ccAddresses: withoutSelf(dedupe(message.ccAddresses), selfAddress),
      subject: replySubject(message.subject),
      bodyText: quote(message, "On reply:"),
    };
  }
  return {
    toAddresses: [],
    ccAddresses: [],
    subject: forwardSubject(message.subject),
    bodyText: quote(message, "---------- Forwarded message ----------"),
  };
}

/** Splits a recipient field into addresses on commas/semicolons/whitespace. */
export function parseRecipients(text: string): string[] {
  return dedupe(
    text
      .split(/[,;\s]+/)
      .map((part) => part.trim())
      .filter(Boolean),
  );
}

export function isAddressLike(value: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim());
}

/** Formats a contact for the DraftInput arrays: "Name <a@b.c>" or the bare address. */
export function formatAddress(name: string, address: string): string {
  const trimmedName = name.trim();
  const trimmedAddress = address.trim();
  if (!trimmedName || !trimmedAddress || trimmedName === trimmedAddress)
    return trimmedAddress;
  return `${trimmedName} <${trimmedAddress}>`;
}

/** Splits "Name <a@b.c>" back into its parts; bare addresses get an empty name. */
export function splitAddress(value: string): { name: string; address: string } {
  const match = value.trim().match(/^"?([^"<]*)"?\s*<([^>]+)>$/);
  if (match) {
    return { name: (match[1] ?? "").trim(), address: (match[2] ?? "").trim() };
  }
  return { name: "", address: value.trim() };
}
