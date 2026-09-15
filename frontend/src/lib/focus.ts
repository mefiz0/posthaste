import type { Action } from "svelte/action";

export interface FocusTrapOptions {
  /** Element to focus on mount; defaults to [data-autofocus] or the first focusable. */
  autofocusSelector?: string;
}

function focusableElements(container: HTMLElement): HTMLElement[] {
  const selector = [
    "button:not([disabled])",
    "[href]",
    "input:not([disabled])",
    "select:not([disabled])",
    "textarea:not([disabled])",
    '[tabindex]:not([tabindex="-1"])',
  ].join(",");
  return Array.from(container.querySelectorAll<HTMLElement>(selector)).filter(
    (element) =>
      element.offsetParent !== null || element === document.activeElement,
  );
}

/**
 * Svelte action that traps Tab focus inside an overlay dialog and restores
 * focus to the previously-focused element on teardown.
 */
export const focusTrap: Action<HTMLElement, FocusTrapOptions | undefined> = (
  node,
  options,
) => {
  const previous =
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;

  function focusInitial(): void {
    const selector = options?.autofocusSelector;
    const initial =
      (selector ? node.querySelector<HTMLElement>(selector) : null) ??
      node.querySelector<HTMLElement>("[data-autofocus]") ??
      focusableElements(node)[0] ??
      node;
    initial.focus();
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key !== "Tab") return;
    const items = focusableElements(node);
    if (items.length === 0) return;
    const first = items[0] as HTMLElement;
    const last = items[items.length - 1] as HTMLElement;
    const active = document.activeElement;
    if (event.shiftKey && active === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && active === last) {
      event.preventDefault();
      first.focus();
    }
  }

  focusInitial();
  node.addEventListener("keydown", onKeydown);
  return {
    destroy() {
      node.removeEventListener("keydown", onKeydown);
      previous?.focus();
    },
  };
};
