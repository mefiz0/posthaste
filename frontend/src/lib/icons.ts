import type { FolderType } from "./types";

/**
 * Icon sprite, kept verbatim from mockup/index.html. App.svelte renders
 * SPRITE_SVG once; components reference symbols through <Icon name="…" />
 * (a plain <use href="#i-…" />).
 */
export const ICON_NAMES = [
  "inbox",
  "star",
  "clock",
  "draft",
  "sent",
  "archive",
  "trash",
  "folder",
  "search",
  "reply",
  "forward",
  "more",
  "clip",
  "check",
  "box",
  "chev",
  "palette",
  "settings",
  "keyboard",
  "edit",
  "move",
  "refresh",
  "checkall",
  "moon",
  "user",
  "x",
  "download",
] as const;

export type IconName = (typeof ICON_NAMES)[number];

const SYMBOL_PATHS: Record<IconName, string> = {
  inbox:
    '<path d="M2 9l2-5.5h8L14 9v3.5H2z"/><path d="M2 9h3.2l.9 2h3.8l.9-2H14"/>',
  star: '<path d="M8 1.8l1.9 3.9 4.3.6-3.1 3 .7 4.3L8 11.6l-3.8 2 .7-4.3-3.1-3 4.3-.6z"/>',
  clock: '<circle cx="8" cy="8" r="6"/><path d="M8 4.6V8l2.4 1.4"/>',
  draft: '<path d="M4 1.5h5l3 3v10H4z"/><path d="M9 1.5V5h3"/>',
  sent: '<path d="M3 13L13 3"/><path d="M6.5 3H13v6.5"/>',
  archive:
    '<rect x="2" y="5" width="12" height="9" rx="1"/><path d="M1.5 2.5h13v2.5h-13z"/><path d="M6.4 8.6h3.2"/>',
  trash:
    '<path d="M3 4h10"/><path d="M6 4V2.5h4V4"/><path d="M4.6 4l.7 10h5.4l.7-10"/>',
  folder: '<path d="M2 4h4l1.5 2H14v7H2z"/>',
  search: '<circle cx="7" cy="7" r="4.5"/><path d="M10.4 10.4L14 14"/>',
  reply: '<path d="M6 4L2.5 7.5 6 11"/><path d="M2.5 7.5H9a4 4 0 014 4v1"/>',
  forward:
    '<path d="M10 4l3.5 3.5L10 11"/><path d="M13.5 7.5H7a4 4 0 00-4 4v1"/>',
  more: '<circle cx="3.2" cy="8" r="1"/><circle cx="8" cy="8" r="1"/><circle cx="12.8" cy="8" r="1"/>',
  clip: '<path d="M11.5 6.2l-4.7 4.7a2.1 2.1 0 01-3-3l5.2-5.2a3.2 3.2 0 014.5 4.5l-5.2 5.2a4.3 4.3 0 01-6-6l4.6-4.6"/>',
  check: '<path d="M3 8.5l3.2 3.2L13 4.7"/>',
  box: '<rect x="3" y="3" width="10" height="10" rx="1.5"/>',
  chev: '<path d="M6 4.5l3.5 3.5L6 11.5"/>',
  palette:
    '<path d="M8 1.5l1.8 3.7 4 .6-2.9 2.8.7 4L8 10.7l-3.6 1.9.7-4L2.2 5.8l4-.6z"/>',
  settings:
    '<circle cx="8" cy="8" r="2.2"/><path d="M8 1.6v1.6M8 12.8v1.6M2.4 8H4M12 8h1.6M4 4l1.1 1.1M10.9 10.9L12 12M12 4l-1.1 1.1M5.1 10.9L4 12"/>',
  keyboard:
    '<rect x="1.5" y="4" width="13" height="8" rx="1.5"/><path d="M4 6.5h.01M6.5 6.5h.01M9 6.5h.01M11.5 6.5h.01M4.5 9.5h7"/>',
  edit: '<path d="M11.5 2.5l2 2L6 12l-2.6.6L4 10z"/>',
  move: '<path d="M8 2v12"/><path d="M5 5l3-3 3 3"/><path d="M5 11l3 3 3-3"/>',
  refresh: '<path d="M13 8a5 5 0 11-1.5-3.5"/><path d="M13 2.5V5h-2.5"/>',
  checkall: '<path d="M1.5 8.5l3 3 6-6.5"/><path d="M7.5 11.5l6-6.5"/>',
  moon: '<path d="M13 9.5A5.5 5.5 0 016.5 3a5.5 5.5 0 106.5 6.5z"/>',
  user: '<circle cx="8" cy="5.5" r="2.5"/><path d="M3.5 13a4.5 4.5 0 019 0"/>',
  x: '<path d="M4 4l8 8M12 4l-8 8"/>',
  download:
    '<path d="M8 2.5v7.5"/><path d="M5 7.5l3 3 3-3"/><path d="M3 13h10"/>',
};

export const SPRITE_SVG = `<svg width="0" height="0" style="position:absolute" aria-hidden="true">${ICON_NAMES.map(
  (name) =>
    `<symbol id="i-${name}" viewBox="0 0 16 16">${SYMBOL_PATHS[name]}</symbol>`,
).join("")}</svg>`;

/** Maps a folder type to its sidebar icon. */
export function folderIcon(type: FolderType): IconName {
  switch (type) {
    case "inbox":
      return "inbox";
    case "sent":
      return "sent";
    case "drafts":
      return "draft";
    case "archive":
      return "archive";
    case "trash":
      return "trash";
    case "junk":
      return "box";
    case "user":
      return "folder";
  }
}
