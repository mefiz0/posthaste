/**
 * Command palette registry: commands belong to sections, declare context
 * availability, and are matched with a scored subsequence fuzzy matcher.
 */

export interface PaletteContext {
  hasSelection: boolean;
  readingOpen: boolean;
  viewTitle: string;
  viewKind: "folder" | "starred" | "snoozed";
  composeOpen: boolean;
}

export interface CommandActions {
  compose(): void;
  openSearch(): void;
  markAllRead(): void;
  refresh(): void;
  selectAll(): void;
  reply(): void;
  replyAll(): void;
  forward(): void;
  archive(): void;
  deleteMessage(): void;
  markUnread(): void;
  toggleStar(): void;
  moveMessage(): void;
  openSettings(): void;
  manageAccounts(): void;
  showShortcuts(): void;
}

export interface Command {
  id: string;
  label: string;
  section: string;
  icon: string;
  keyHint?: string;
  available(context: PaletteContext): boolean;
  run(): void;
}

interface CommandSpec {
  id: string;
  label: string;
  section: string;
  icon: string;
  keyHint?: string;
  when: "message" | "list" | "always";
}

const COMMAND_SPECS: CommandSpec[] = [
  // Section order matches the mockup: Inbox, Message, Application.
  {
    id: "compose",
    label: "Compose",
    section: "Inbox",
    icon: "edit",
    keyHint: "C",
    // Always available: writing a new message should not require leaving the
    // open conversation first.
    when: "always",
  },
  {
    id: "search",
    label: "Search mail",
    section: "Inbox",
    icon: "search",
    keyHint: "/",
    when: "list",
  },
  {
    id: "selectall",
    label: "Select all",
    section: "Inbox",
    icon: "check",
    when: "list",
  },
  {
    id: "markallread",
    label: "Mark all as read",
    section: "Inbox",
    icon: "checkall",
    when: "list",
  },
  {
    id: "refresh",
    label: "Refresh",
    section: "Inbox",
    icon: "refresh",
    when: "list",
  },
  {
    id: "reply",
    label: "Reply",
    section: "Message",
    icon: "reply",
    keyHint: "R",
    when: "message",
  },
  {
    id: "replyall",
    label: "Reply all",
    section: "Message",
    icon: "reply",
    keyHint: "A",
    when: "message",
  },
  {
    id: "forward",
    label: "Forward",
    section: "Message",
    icon: "forward",
    keyHint: "F",
    when: "message",
  },
  {
    id: "archive",
    label: "Archive",
    section: "Message",
    icon: "archive",
    keyHint: "E",
    when: "message",
  },
  {
    id: "delete",
    label: "Delete",
    section: "Message",
    icon: "trash",
    keyHint: "#",
    when: "message",
  },
  {
    id: "unread",
    label: "Mark unread",
    section: "Message",
    icon: "box",
    keyHint: "U",
    when: "message",
  },
  {
    id: "star",
    label: "Star",
    section: "Message",
    icon: "star",
    keyHint: "S",
    when: "message",
  },
  {
    id: "move",
    label: "Move to…",
    section: "Message",
    icon: "move",
    keyHint: "M",
    when: "message",
  },
  {
    id: "settings",
    label: "Open Settings",
    section: "Application",
    icon: "settings",
    when: "always",
  },
  {
    id: "accounts",
    label: "Manage accounts",
    section: "Application",
    icon: "user",
    when: "always",
  },
  {
    id: "shortcuts",
    label: "Keyboard shortcuts",
    section: "Application",
    icon: "keyboard",
    keyHint: "?",
    when: "always",
  },
];

function commandAction(spec: CommandSpec, actions: CommandActions): void {
  switch (spec.id) {
    case "compose":
      actions.compose();
      break;
    case "search":
      actions.openSearch();
      break;
    case "selectall":
      actions.selectAll();
      break;
    case "markallread":
      actions.markAllRead();
      break;
    case "refresh":
      actions.refresh();
      break;
    case "reply":
      actions.reply();
      break;
    case "replyall":
      actions.replyAll();
      break;
    case "forward":
      actions.forward();
      break;
    case "archive":
      actions.archive();
      break;
    case "delete":
      actions.deleteMessage();
      break;
    case "unread":
      actions.markUnread();
      break;
    case "star":
      actions.toggleStar();
      break;
    case "move":
      actions.moveMessage();
      break;
    case "settings":
      actions.openSettings();
      break;
    case "accounts":
      actions.manageAccounts();
      break;
    case "shortcuts":
      actions.showShortcuts();
      break;
  }
}

/** Builds the palette registry; injected actions keep this module store-free. */
export function buildCommands(actions: CommandActions): Command[] {
  return COMMAND_SPECS.map((spec) => ({
    id: spec.id,
    label: spec.label,
    section: spec.section,
    icon: spec.icon,
    keyHint: spec.keyHint,
    available: (context: PaletteContext) =>
      spec.when === "always" ||
      (spec.when === "message" ? context.hasSelection : !context.hasSelection),
    run: () => commandAction(spec, actions),
  }));
}

/**
 * Scored subsequence matcher. Empty query matches everything with score 0.
 * Contiguous runs and prefix hits score higher; non-matches return null.
 * Operates on code points so it behaves with unicode input.
 */
export function fuzzyScore(query: string, text: string): number | null {
  if (!query) return 0;
  const q = Array.from(query.toLowerCase());
  const t = Array.from(text.toLowerCase());
  let score = 0;
  let searchFrom = 0;
  let streak = 0;
  for (const char of q) {
    const index = t.indexOf(char, searchFrom);
    if (index === -1) return null;
    streak = index === searchFrom ? streak + 1 : 1;
    score += 10 + streak * 2 + (index === 0 ? 6 : 0);
    searchFrom = index + 1;
  }
  // Slight preference for shorter labels so "Delete" outranks "Delete and move on…".
  score -= Math.floor((t.length - q.length) / 8);
  return score;
}

/** Filters commands by availability, then fuzzy rank by query. */
export function queryCommands(
  query: string,
  context: PaletteContext,
  commands: Command[],
): Command[] {
  const available = commands.filter((command) => command.available(context));
  const trimmed = query.trim();
  if (!trimmed) return available;
  const scored: { command: Command; score: number }[] = [];
  for (const command of available) {
    const score = fuzzyScore(trimmed, `${command.label} ${command.section}`);
    if (score !== null) scored.push({ command, score });
  }
  scored.sort((a, b) => b.score - a.score);
  return scored.map((entry) => entry.command);
}

/** Groups a flat command list into ordered sections for rendering. */
export function groupBySection(
  commands: Command[],
): { section: string; commands: Command[] }[] {
  const groups: { section: string; commands: Command[] }[] = [];
  for (const command of commands) {
    const last = groups[groups.length - 1];
    if (last && last.section === command.section) {
      last.commands.push(command);
    } else {
      groups.push({ section: command.section, commands: [command] });
    }
  }
  return groups;
}
