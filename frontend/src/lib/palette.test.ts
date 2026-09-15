import { describe, expect, it } from "vitest";
import {
  buildCommands,
  fuzzyScore,
  groupBySection,
  queryCommands,
  type CommandActions,
  type PaletteContext,
} from "./palette";

const baseContext: PaletteContext = {
  hasSelection: false,
  readingOpen: false,
  viewTitle: "Inbox",
  viewKind: "folder",
  composeOpen: false,
  hasThread: false,
};

const messageContext: PaletteContext = {
  ...baseContext,
  hasSelection: true,
  readingOpen: true,
};

const threadContext: PaletteContext = {
  ...messageContext,
  hasThread: true,
};

const noopActions: CommandActions = {
  compose: () => {},
  openSearch: () => {},
  markAllRead: () => {},
  refresh: () => {},
  selectAll: () => {},
  reply: () => {},
  replyAll: () => {},
  forward: () => {},
  archive: () => {},
  deleteMessage: () => {},
  markUnread: () => {},
  toggleStar: () => {},
  moveMessage: () => {},
  toggleThread: () => {},
  openSettings: () => {},
  manageAccounts: () => {},
  showShortcuts: () => {},
};

const commands = buildCommands(noopActions);

describe("fuzzyScore", () => {
  it("matches everything with score 0 on an empty query", () => {
    expect(fuzzyScore("", "Delete")).toBe(0);
  });

  it("returns null when the query is not a subsequence", () => {
    expect(fuzzyScore("xz", "Compose")).toBeNull();
  });

  it("scores prefix matches higher than scattered matches", () => {
    const prefix = fuzzyScore("arc", "Archive");
    const scattered = fuzzyScore("arc", "Mark all as read — archive");
    expect(prefix).not.toBeNull();
    expect(scattered).not.toBeNull();
    expect(prefix as number).toBeGreaterThan(scattered as number);
  });

  it("rewards contiguous runs over gaps", () => {
    const contiguous = fuzzyScore("com", "Compose");
    const gapped = fuzzyScore("com", "Command — open compose");
    expect(contiguous).not.toBeNull();
    expect(gapped).not.toBeNull();
    expect(contiguous as number).toBeGreaterThan(gapped as number);
  });

  it("is case-insensitive", () => {
    expect(fuzzyScore("DEL", "Delete")).not.toBeNull();
  });

  it("handles unicode by code point", () => {
    expect(fuzzyScore("Ém", "Émile Zola")).not.toBeNull();
    expect(fuzzyScore(" Zo", "Émile Zola")).not.toBeNull();
    expect(fuzzyScore("zz", "Émile Zola")).toBeNull();
  });
});

describe("queryCommands", () => {
  it("returns all available commands in registry order on an empty query", () => {
    const result = queryCommands("", baseContext, commands);
    expect(result.map((command) => command.id)).toEqual([
      "compose",
      "search",
      "selectall",
      "markallread",
      "refresh",
      "settings",
      "accounts",
      "shortcuts",
    ]);
  });

  it("keeps Compose available while a message is open and hides list-only commands", () => {
    const result = queryCommands("", messageContext, commands);
    expect(result.map((command) => command.id)).toEqual([
      "compose",
      "reply",
      "replyall",
      "forward",
      "archive",
      "delete",
      "unread",
      "star",
      "move",
      "settings",
      "accounts",
      "shortcuts",
    ]);
  });

  it("offers the conversation toggle only for multi-message threads", () => {
    const ids = queryCommands("", threadContext, commands).map(
      (command) => command.id,
    );
    expect(ids).toContain("thread");
    expect(
      queryCommands("", messageContext, commands).map((c) => c.id),
    ).not.toContain("thread");
  });

  it("filters and ranks by query", () => {
    const result = queryCommands("repl", messageContext, commands);
    expect(result[0]?.id).toBe("reply");
    expect(result.some((command) => command.id === "settings")).toBe(false);
  });

  it("runs the wired action for a command", () => {
    let called = 0;
    const wired = buildCommands({
      ...noopActions,
      compose: () => (called += 1),
    });
    const compose = queryCommands("compose", baseContext, wired)[0];
    compose?.run();
    expect(called).toBe(1);
  });
});

describe("groupBySection", () => {
  it("keeps section order and groups consecutive commands", () => {
    const result = groupBySection(queryCommands("", baseContext, commands));
    expect(result.map((group) => group.section)).toEqual([
      "Inbox",
      "Application",
    ]);
  });

  it("groups the open-message palette into Inbox then Message", () => {
    const result = groupBySection(queryCommands("", messageContext, commands));
    expect(result[0]?.section).toBe("Inbox");
    expect(result[0]?.commands[0]?.id).toBe("compose");
    expect(result[1]?.section).toBe("Message");
    expect(result[1]?.commands[0]?.keyHint).toBe("R");
  });
});
