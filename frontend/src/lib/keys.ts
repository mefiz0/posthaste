/**
 * Declarative keymap + dispatcher. Bindings reference named actions; rebinding
 * from settings only swaps the chord list, never the call sites.
 */

export type ActionId =
  | "moveDown"
  | "moveUp"
  | "openMessage"
  | "back"
  | "gotoInbox"
  | "gotoStarred"
  | "gotoDrafts"
  | "gotoArchive"
  | "search"
  | "shortcuts"
  | "shortcutsClose"
  | "overlayClose"
  | "compose"
  | "sendCompose"
  | "composeClose"
  | "attachInCompose"
  | "star"
  | "markUnread"
  | "archive"
  | "delete"
  | "viewerClose"
  | "syncClose"
  | "reply"
  | "replyAll"
  | "forward"
  | "move"
  | "scrollPage"
  | "commandPalette";

export type KeyGroup = "Navigation" | "Message" | "Compose" | "Global";

export interface KeyChord {
  /** Canonical key name: single characters are lowercase; named keys are 'escape', 'enter', 'arrowdown', ' ' … */
  key: string;
  ctrl?: boolean;
  shift?: boolean;
  alt?: boolean;
}

export interface KeyBinding {
  id: ActionId;
  label: string;
  group: KeyGroup;
  /** Each entry is one alternative; an entry with several chords is a sequence (G then I). */
  chords: KeyChord[][];
}

const CHORD = (key: string): KeyChord => ({ key });
const ALT = (...chords: KeyChord[]): KeyChord[] => chords;

export const DEFAULT_KEYMAP: KeyBinding[] = [
  {
    id: "moveDown",
    label: "Move between messages",
    group: "Navigation",
    chords: [ALT(CHORD("j")), ALT(CHORD("arrowdown"))],
  },
  {
    id: "moveUp",
    label: "Move between messages",
    group: "Navigation",
    chords: [ALT(CHORD("k")), ALT(CHORD("arrowup"))],
  },
  {
    id: "openMessage",
    label: "Open message",
    group: "Navigation",
    chords: [ALT(CHORD("o")), ALT(CHORD("enter"))],
  },
  {
    id: "back",
    label: "Back",
    group: "Navigation",
    chords: [ALT(CHORD("escape"))],
  },
  {
    id: "gotoInbox",
    label: "Go to Inbox",
    group: "Navigation",
    chords: [ALT(CHORD("g"), CHORD("i"))],
  },
  {
    id: "gotoStarred",
    label: "Go to Starred",
    group: "Navigation",
    chords: [ALT(CHORD("g"), CHORD("s"))],
  },
  {
    id: "gotoDrafts",
    label: "Go to Drafts",
    group: "Navigation",
    chords: [ALT(CHORD("g"), CHORD("d"))],
  },
  {
    id: "gotoArchive",
    label: "Go to Archive",
    group: "Navigation",
    chords: [ALT(CHORD("g"), CHORD("a"))],
  },
  { id: "reply", label: "Reply", group: "Message", chords: [ALT(CHORD("r"))] },
  {
    id: "replyAll",
    label: "Reply all",
    group: "Message",
    chords: [ALT(CHORD("a"))],
  },
  {
    id: "forward",
    label: "Forward",
    group: "Message",
    chords: [ALT(CHORD("f"))],
  },
  {
    id: "archive",
    label: "Archive",
    group: "Message",
    chords: [ALT(CHORD("e"))],
  },
  {
    id: "delete",
    label: "Delete",
    group: "Message",
    chords: [ALT(CHORD("#"))],
  },
  { id: "star", label: "Star", group: "Message", chords: [ALT(CHORD("s"))] },
  {
    id: "markUnread",
    label: "Mark unread",
    group: "Message",
    chords: [ALT(CHORD("u"))],
  },
  {
    id: "move",
    label: "Move to…",
    group: "Message",
    chords: [ALT(CHORD("m"))],
  },
  {
    id: "compose",
    label: "Compose",
    group: "Compose",
    chords: [ALT(CHORD("c"))],
  },
  {
    id: "sendCompose",
    label: "Send",
    group: "Compose",
    chords: [ALT({ key: "enter", ctrl: true })],
  },
  {
    id: "attachInCompose",
    label: "Attach",
    group: "Compose",
    chords: [ALT({ key: "a", ctrl: true, shift: true })],
  },
  { id: "search", label: "Search", group: "Global", chords: [ALT(CHORD("/"))] },
  {
    id: "commandPalette",
    label: "Command palette",
    group: "Global",
    chords: [ALT({ key: "k", ctrl: true })],
  },
  {
    id: "shortcuts",
    label: "Keyboard shortcuts",
    group: "Global",
    chords: [ALT(CHORD("?"))],
  },
];

export type OverlayContext =
  | "none"
  | "palette"
  | "compose"
  | "shortcuts"
  | "settings"
  | "setup"
  | "viewer"
  | "sync"
  | "capture";

export interface DispatcherContext {
  overlay: OverlayContext;
  /** The event target is an input/textarea/select — suppress single-key actions. */
  typing: boolean;
  readingOpen: boolean;
  searchOpen: boolean;
}

export interface ChordSource {
  key: string;
  ctrlKey?: boolean;
  metaKey?: boolean;
  shiftKey?: boolean;
  altKey?: boolean;
}

/** Normalizes a keyboard event into a canonical chord. */
export function eventToChord(event: ChordSource): KeyChord {
  let key = event.key.toLowerCase();
  if (key === "esc") key = "escape";
  return {
    key,
    ctrl: event.ctrlKey === true || event.metaKey === true,
    shift: event.shiftKey === true,
    alt: event.altKey === true,
  };
}

/** Parses a persisted chord string such as "Ctrl+Shift+A" or "#" or "G+I". */
export function parseChord(serialized: string): KeyChord {
  const parts = serialized
    .split("+")
    .map((part) => part.trim())
    .filter(Boolean);
  const keyPart = parts.pop() ?? "";
  const modifiers = new Set(parts.map((part) => part.toLowerCase()));
  let key = keyPart.toLowerCase();
  if (key === "esc") key = "escape";
  if (key === "space") key = " ";
  return {
    key,
    ctrl: modifiers.has("ctrl") || modifiers.has("cmd") || modifiers.has("⌘"),
    shift: modifiers.has("shift"),
    alt: modifiers.has("alt"),
  };
}

/** Serializes a chord back to the canonical "Ctrl+K" form used in settings. */
export function serializeChord(chord: KeyChord): string {
  const parts: string[] = [];
  if (chord.ctrl) parts.push("Ctrl");
  if (chord.alt) parts.push("Alt");
  if (chord.shift) parts.push("Shift");
  const key =
    chord.key === " "
      ? "Space"
      : chord.key.charAt(0).toUpperCase() + chord.key.slice(1);
  parts.push(key);
  return parts.join("+");
}

export function isApplePlatform(): boolean {
  return (
    typeof navigator !== "undefined" &&
    /mac|iphone|ipad|ipod/i.test(navigator.platform)
  );
}

/** Key-cap labels for display, one entry per kbd element. */
export function formatChordParts(
  chord: KeyChord,
  apple: boolean = isApplePlatform(),
): string[] {
  const parts: string[] = [];
  if (chord.ctrl) parts.push(apple ? "⌘" : "Ctrl");
  if (chord.alt) parts.push(apple ? "⌥" : "Alt");
  if (chord.shift) parts.push(apple ? "⇧" : "Shift");
  parts.push(prettyKey(chord.key, apple));
  return parts;
}

function prettyKey(key: string, apple: boolean): string {
  switch (key) {
    case " ":
      return "Space";
    case "escape":
      return "Esc";
    case "enter":
      return "↵";
    case "backspace":
      return "⌫";
    case "arrowup":
      return "↑";
    case "arrowdown":
      return "↓";
    case "arrowleft":
      return "←";
    case "arrowright":
      return "→";
    default:
      return apple
        ? key
        : key.length === 1
          ? key.toUpperCase()
          : key.charAt(0).toUpperCase() + key.slice(1);
  }
}

/** Shift is intentionally ignored when matching: keycaps already reflect it ('?' = Shift+/). */
export function chordEquals(a: KeyChord, b: KeyChord): boolean {
  return (
    a.key === b.key &&
    (a.ctrl === true) === (b.ctrl === true) &&
    (a.alt === true) === (b.alt === true)
  );
}

function alternativeMatches(
  alternative: KeyChord[],
  buffer: KeyChord[],
): boolean {
  if (alternative.length !== buffer.length) return false;
  return alternative.every((chord, index) =>
    chordEquals(chord, buffer[index] as KeyChord),
  );
}

function alternativeExtends(
  alternative: KeyChord[],
  buffer: KeyChord[],
): boolean {
  if (alternative.length <= buffer.length) return false;
  return buffer.every((chord, index) =>
    chordEquals(alternative[index] as KeyChord, chord),
  );
}

function bindingMatches(binding: KeyBinding, buffer: KeyChord[]): boolean {
  return binding.chords.some((alternative) =>
    alternativeMatches(alternative, buffer),
  );
}

function hasLongerBinding(binding: KeyBinding, buffer: KeyChord[]): boolean {
  return binding.chords.some((alternative) =>
    alternativeExtends(alternative, buffer),
  );
}

/** Pure matcher used by the dispatcher (and tests): exact action or keep waiting for sequence input. */
export function resolveKey(
  keymap: KeyBinding[],
  buffer: KeyChord[],
): { action: ActionId | null; awaitMore: boolean } {
  for (const binding of keymap) {
    if (bindingMatches(binding, buffer))
      return { action: binding.id, awaitMore: false };
  }
  for (const binding of keymap) {
    if (hasLongerBinding(binding, buffer))
      return { action: null, awaitMore: true };
  }
  return { action: null, awaitMore: false };
}

/** Merges user overrides (action id -> serialized chord) over the defaults. */
export function getKeymap(overrides?: Record<string, string>): KeyBinding[] {
  if (!overrides) return DEFAULT_KEYMAP;
  return DEFAULT_KEYMAP.map((binding) => {
    const replacement = overrides[binding.id];
    if (!replacement) return binding;
    return { ...binding, chords: [[parseChord(replacement)]] };
  });
}

/** Finds which action already owns a chord, for conflict detection in settings.
 *  Only the first chord of each alternative counts: later chords of a sequence
 *  are only reachable once the prefix has primed it. */
export function findChordOwner(
  keymap: KeyBinding[],
  chord: KeyChord,
): ActionId | null {
  for (const binding of keymap) {
    for (const alternative of binding.chords) {
      const first = alternative[0];
      if (first && chordEquals(first, chord)) return binding.id;
    }
  }
  return null;
}

export function isTypingTarget(target: EventTarget | null): boolean {
  const element = target as {
    tagName?: string;
    isContentEditable?: boolean;
  } | null;
  if (!element || typeof element.tagName !== "string") return false;
  const tag = element.tagName.toLowerCase();
  return (
    tag === "input" ||
    tag === "textarea" ||
    tag === "select" ||
    element.isContentEditable === true
  );
}

export type ActionRunner = (action: ActionId) => void;

const SEQUENCE_TIMEOUT_MS = 900;

/**
 * Creates the single global key handler. Context-aware: overlays capture
 * Escape/compose chords, typing suppresses single-key actions, and G-prefixed
 * sequences time out like the mockup. The optional overrides getter supplies
 * the user's saved keymap (settings-changed re-feeds it at runtime).
 */
export function createDispatcher(
  run: ActionRunner,
  getContext: () => DispatcherContext,
  getOverrides: () => Record<string, string> | undefined = () => undefined,
) {
  let buffer: KeyChord[] = [];
  let sequenceTimer: ReturnType<typeof setTimeout> | null = null;

  function clearSequence(): void {
    buffer = [];
    if (sequenceTimer !== null) {
      clearTimeout(sequenceTimer);
      sequenceTimer = null;
    }
  }

  function handleKeydown(event: KeyboardEvent): void {
    const context = getContext();
    const chord = eventToChord(event);

    if (context.overlay === "palette" || context.overlay === "capture") return;

    if (context.overlay === "compose") {
      if (chord.ctrl && chord.key === "enter") {
        event.preventDefault();
        run("sendCompose");
      } else if (chord.ctrl && chord.shift && chord.key === "a") {
        event.preventDefault();
        run("attachInCompose");
      } else if (chord.key === "escape") {
        event.preventDefault();
        run("composeClose");
      }
      return;
    }

    if (
      context.overlay === "shortcuts" ||
      context.overlay === "settings" ||
      context.overlay === "setup" ||
      context.overlay === "viewer" ||
      context.overlay === "sync"
    ) {
      if (chord.key === "escape") {
        event.preventDefault();
        if (context.overlay === "shortcuts") run("shortcutsClose");
        else if (context.overlay === "viewer") run("viewerClose");
        else if (context.overlay === "sync") run("syncClose");
        else run("overlayClose");
      }
      return;
    }

    if (context.typing) {
      if (chord.key === "escape" && context.searchOpen) {
        event.preventDefault();
        run("back");
      }
      return;
    }

    if (chord.key === "escape") {
      clearSequence();
      if (context.readingOpen) {
        event.preventDefault();
        run("back");
      }
      return;
    }

    const nextBuffer = [...buffer, chord];
    const { action, awaitMore } = resolveKey(
      getKeymap(getOverrides()),
      nextBuffer,
    );
    if (action) {
      event.preventDefault();
      clearSequence();
      run(action);
      return;
    }
    if (awaitMore) {
      event.preventDefault();
      buffer = nextBuffer;
      if (sequenceTimer !== null) clearTimeout(sequenceTimer);
      sequenceTimer = setTimeout(clearSequence, SEQUENCE_TIMEOUT_MS);
      return;
    }
    clearSequence();
  }

  return { handleKeydown };
}
