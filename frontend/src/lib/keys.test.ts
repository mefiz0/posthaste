import { describe, expect, it } from "vitest";
import {
  chordEquals,
  createDispatcher,
  eventToChord,
  findChordOwner,
  formatChordParts,
  getKeymap,
  isTypingTarget,
  parseChord,
  resolveKey,
  serializeChord,
  type ActionId,
  type DispatcherContext,
} from "./keys";

function key(
  key: string,
  mods: Partial<{ ctrl: boolean; shift: boolean; alt: boolean }> = {},
): KeyboardEvent {
  return {
    key,
    ctrlKey: mods.ctrl ?? false,
    metaKey: false,
    shiftKey: mods.shift ?? false,
    altKey: mods.alt ?? false,
    preventDefault: () => {},
    stopPropagation: () => {},
  } as unknown as KeyboardEvent;
}

const idleContext = (): DispatcherContext => ({
  overlay: "none",
  typing: false,
  readingOpen: false,
  searchOpen: false,
});

describe("chord parsing and formatting", () => {
  it("parses modifier chains", () => {
    const chord = parseChord("Ctrl+Shift+A");
    expect(chord).toMatchObject({ key: "a", ctrl: true, shift: true });
  });

  it("parses plain keys and normalizes aliases", () => {
    expect(parseChord("#").key).toBe("#");
    expect(parseChord("Esc").key).toBe("escape");
    expect(parseChord("Space").key).toBe(" ");
  });

  it("round-trips through serialize", () => {
    expect(serializeChord(parseChord("Ctrl+K"))).toBe("Ctrl+K");
    expect(serializeChord(parseChord("J"))).toBe("J");
  });

  it("formats display parts per platform", () => {
    expect(formatChordParts({ key: "enter", ctrl: true }, false)).toEqual([
      "Ctrl",
      "↵",
    ]);
    expect(formatChordParts({ key: "k", ctrl: true }, true)).toEqual([
      "⌘",
      "k",
    ]);
    expect(formatChordParts({ key: " " }, false)).toEqual(["Space"]);
  });

  it("ignores shift when matching", () => {
    expect(chordEquals({ key: "s", shift: true }, { key: "s" })).toBe(true);
  });

  it("normalizes keyboard events", () => {
    expect(eventToChord(key("J"))).toMatchObject({ key: "j" });
    expect(eventToChord(key("K", { ctrl: true }))).toMatchObject({
      key: "k",
      ctrl: true,
    });
    expect(eventToChord({ key: "Escape" })).toMatchObject({ key: "escape" });
  });
});

describe("resolveKey", () => {
  const keymap = getKeymap();

  it("matches single chords", () => {
    expect(resolveKey(keymap, [{ key: "j" }]).action).toBe<ActionId>(
      "moveDown",
    );
    expect(resolveKey(keymap, [{ key: "#" }]).action).toBe<ActionId>("delete");
    expect(
      resolveKey(keymap, [{ key: "?", shift: true }]).action,
    ).toBe<ActionId>("shortcuts");
  });

  it("matches Ctrl combos", () => {
    expect(
      resolveKey(keymap, [{ key: "k", ctrl: true }]).action,
    ).toBe<ActionId>("commandPalette");
  });

  it("awaits more input after the G prefix", () => {
    const first = resolveKey(keymap, [{ key: "g" }]);
    expect(first.action).toBeNull();
    expect(first.awaitMore).toBe(true);
    expect(
      resolveKey(keymap, [{ key: "g" }, { key: "i" }]).action,
    ).toBe<ActionId>("gotoInbox");
    expect(
      resolveKey(keymap, [{ key: "g" }, { key: "s" }]).action,
    ).toBe<ActionId>("gotoStarred");
  });

  it("abandons unknown sequences", () => {
    expect(resolveKey(keymap, [{ key: "g" }, { key: "z" }])).toEqual({
      action: null,
      awaitMore: false,
    });
  });
});

describe("getKeymap overrides", () => {
  it("replaces chords from settings overrides", () => {
    const keymap = getKeymap({ archive: "Ctrl+E" });
    expect(resolveKey(keymap, [{ key: "e" }]).action).toBeNull();
    expect(
      resolveKey(keymap, [{ key: "e", ctrl: true }]).action,
    ).toBe<ActionId>("archive");
  });

  it("detects conflicts for the settings UI", () => {
    const keymap = getKeymap();
    expect(findChordOwner(keymap, { key: "s" })).toBe<ActionId>("star");
    expect(findChordOwner(keymap, { key: "q" })).toBeNull();
  });
});

describe("isTypingTarget", () => {
  function target(tagName: string): EventTarget {
    return { tagName } as unknown as EventTarget;
  }

  it("detects input targets", () => {
    expect(isTypingTarget(target("INPUT"))).toBe(true);
    expect(isTypingTarget(target("TEXTAREA"))).toBe(true);
    expect(isTypingTarget(target("BUTTON"))).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
  });
});

describe("createDispatcher", () => {
  function makeHarness(getContext = idleContext) {
    const ran: ActionId[] = [];
    const dispatcher = createDispatcher(
      (action) => ran.push(action),
      getContext,
    );
    return {
      ran,
      handle: (event: KeyboardEvent) => dispatcher.handleKeydown(event),
    };
  }

  it("dispatches navigation and message actions", () => {
    const { ran, handle } = makeHarness();
    handle(key("j"));
    handle(key("ArrowDown"));
    handle(key("e"));
    expect(ran).toEqual<ActionId[]>(["moveDown", "moveDown", "archive"]);
  });

  it("runs two-key sequences after a G prefix", () => {
    const { ran, handle } = makeHarness();
    handle(key("g"));
    handle(key("d"));
    expect(ran).toEqual<ActionId[]>(["gotoDrafts"]);
  });

  it("suppresses single-key actions while typing", () => {
    const { ran, handle } = makeHarness(() => ({
      ...idleContext(),
      typing: true,
      searchOpen: true,
    }));
    handle(key("e"));
    expect(ran).toEqual([]);
    handle(key("Escape"));
    expect(ran).toEqual<ActionId[]>(["back"]);
  });

  it("routes Escape in compose context to close, Ctrl+Enter to send", () => {
    const { ran, handle } = makeHarness(() => ({
      ...idleContext(),
      overlay: "compose",
    }));
    handle(key("e"));
    expect(ran).toEqual([]);
    handle(key("Enter", { ctrl: true }));
    handle(key("Escape"));
    expect(ran).toEqual<ActionId[]>(["sendCompose", "composeClose"]);
  });

  it("only closes overlays like shortcuts via Escape", () => {
    const { ran, handle } = makeHarness(() => ({
      ...idleContext(),
      overlay: "shortcuts",
    }));
    handle(key("e"));
    handle(key("Escape"));
    expect(ran).toEqual<ActionId[]>(["shortcutsClose"]);
  });

  it("closes reading with Escape when open", () => {
    const { ran, handle } = makeHarness(() => ({
      ...idleContext(),
      readingOpen: true,
    }));
    handle(key("Escape"));
    expect(ran).toEqual<ActionId[]>(["back"]);
  });

  it("falls back to single-key matching when a sequence dies", () => {
    const { ran, handle } = makeHarness();
    handle(key("g"));
    handle(key("s"));
    expect(ran).toEqual<ActionId[]>(["gotoStarred"]);
    handle(key("s"));
    expect(ran).toEqual<ActionId[]>(["gotoStarred", "star"]);
  });
});
