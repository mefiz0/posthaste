import type { IconName } from "./icons";

/**
 * The contextual menu model. Items are built per right-click target by the
 * store and rendered by ContextMenu.svelte; this module holds only the types
 * and the target detection so the wiring stays store-free.
 */

export interface ContextMenuItem {
  id: string;
  label: string;
  icon?: IconName;
  /** Keyboard chord shown on the right, e.g. "R". */
  hint?: string;
  danger?: boolean;
  disabled?: boolean;
  /** Draws a divider above this item, grouping related actions. */
  separatorBefore?: boolean;
  run(): void;
}

export type ContextMenuSubject =
  | { kind: "message"; messageId: number }
  | { kind: "reading" }
  | { kind: "folder"; folderId: number }
  | { kind: "editing"; element: HTMLInputElement | HTMLTextAreaElement }
  | { kind: "app" };

const CONTEXT_ATTR = "data-context";

/**
 * Classifies the element under a contextmenu event. Editable fields win over
 * any surrounding marker so a right-click in the compose body gets text
 * actions rather than the dialog's menu.
 */
export function detectContextMenuSubject(
  target: EventTarget | null,
): ContextMenuSubject {
  if (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement
  ) {
    return { kind: "editing", element: target };
  }
  if (!(target instanceof Element)) return { kind: "app" };
  const marked = target.closest<HTMLElement>(`[${CONTEXT_ATTR}]`);
  if (!marked) return { kind: "app" };
  switch (marked.dataset.context) {
    case "message": {
      const messageId = Number(marked.dataset.messageId);
      return Number.isFinite(messageId)
        ? { kind: "message", messageId }
        : { kind: "app" };
    }
    case "reading":
      return { kind: "reading" };
    case "folder": {
      const folderId = Number(marked.dataset.folderId);
      return Number.isFinite(folderId)
        ? { kind: "folder", folderId }
        : { kind: "app" };
    }
    default:
      return { kind: "app" };
  }
}

const MENU_WIDTH = 220;
const ITEM_HEIGHT = 32;
const MENU_PADDING = 8;
const VIEWPORT_MARGIN = 8;

/**
 * Keeps the menu inside the viewport: it is clamped on both axes so a
 * right-click near an edge still opens a fully visible menu.
 */
export function clampMenuPosition(
  x: number,
  y: number,
  itemCount: number,
  viewport: { width: number; height: number } = {
    width: window.innerWidth,
    height: window.innerHeight,
  },
): { x: number; y: number } {
  const height = itemCount * ITEM_HEIGHT + MENU_PADDING;
  const maxX = Math.max(
    VIEWPORT_MARGIN,
    viewport.width - MENU_WIDTH - VIEWPORT_MARGIN,
  );
  const maxY = Math.max(
    VIEWPORT_MARGIN,
    viewport.height - height - VIEWPORT_MARGIN,
  );
  return {
    x: Math.min(Math.max(x, VIEWPORT_MARGIN), maxX),
    y: Math.min(Math.max(y, VIEWPORT_MARGIN), maxY),
  };
}
