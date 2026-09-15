import { cubicOut } from 'svelte/easing';
import type { TransitionConfig } from 'svelte/transition';

/**
 * Transitions honour the OS reduced-motion preference by reading the media
 * query at start time. The global stylesheet clears CSS animations under the
 * same preference, but Svelte transitions run in JavaScript and are unaffected
 * by that rule, so they gate themselves here.
 */
function reduceMotion(): boolean {
  return (
    typeof window !== 'undefined' &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches
  );
}

const DURATION = { overlay: 120, reveal: 150 } as const;

/** Soft fade shared by every modal overlay and its backdrop. */
export function overlayFade(_node: Element): TransitionConfig {
  return {
    duration: reduceMotion() ? 0 : DURATION.overlay,
    easing: cubicOut,
    css: (t) => `opacity: ${t}`,
  };
}

/** Height-and-fade reveal for inline bars such as the list search field. */
export function revealDown(node: Element): TransitionConfig {
  const height = node.getBoundingClientRect().height;
  return {
    duration: reduceMotion() ? 0 : DURATION.reveal,
    easing: cubicOut,
    css: (t) => `height: ${t * height}px; opacity: ${t}; overflow: hidden;`,
  };
}
