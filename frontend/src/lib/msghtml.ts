/**
 * Helpers for turning an already-sanitized message HTML body into the static
 * document rendered inside the sandboxed iframe. Message HTML is never placed
 * in the app DOM; this module only produces the srcdoc string.
 *
 * The iframe gets sandbox="" (no tokens at all): no scripts, no same-origin,
 * no top navigation. Links are rewritten with target=_blank so the shell can
 * route them to the OS browser; the sandbox blocks any in-frame navigation.
 */

export interface FrameOptions {
  /** When false (default) remote image/media loads are denied by the CSP. */
  allowRemote?: boolean;
}

const CID_PATTERN = /\bcid:([^"'>\s)]+)/gi;

/** True when the HTML references remote resources (tracking pixels, hosted images). */
export function hasRemoteContent(html: string): boolean {
  return /(?:\bsrc|\bbackground|\bposter)\s*=\s*["']?https?:/i.test(html);
}

/**
 * Replaces cid: references with data URLs produced by the attachment store.
 * Unknown cids resolve to null and are rewritten to a 1px transparent
 * placeholder so nothing tries to load them.
 */
export async function resolveCids(
  html: string,
  resolve: (cid: string) => Promise<string | null>,
): Promise<string> {
  const cids = new Set<string>();
  for (const match of html.matchAll(CID_PATTERN)) {
    const cid = match[1];
    if (cid) cids.add(cid);
  }
  let resolved = html;
  for (const cid of cids) {
    const dataUrl = await resolve(cid);
    const replacement = dataUrl ?? TRANSPARENT_PLACEHOLDER;
    resolved = resolved.replaceAll(`cid:${cid}`, replacement);
  }
  return resolved;
}

export function buildFrameDocument(
  html: string,
  options: FrameOptions = {},
): string {
  const allowRemote = options.allowRemote ?? false;
  const mediaPolicy = allowRemote ? "data: blob: https: http:" : "data: blob:";
  // script-src 'none' is explicit defense in depth on top of the sandbox attribute.
  const csp = [
    "default-src 'none'",
    `img-src ${mediaPolicy}`,
    "style-src 'unsafe-inline'",
    "script-src 'none'",
    "frame-src 'none'",
    "connect-src 'none'",
    `media-src ${mediaPolicy}`,
    "font-src data:",
  ].join("; ");
  const linked = html.replace(
    /<a\s+/gi,
    '<a target="_blank" rel="noopener noreferrer" ',
  );
  // The frame is a separate document, so it repeats the app's dark tokens
  // rather than inheriting them. Message HTML is authored for a light
  // background with its own hardcoded colours, so the content is inverted
  // (and hue-rotated back) to land on the dark surface while keeping its
  // colours recognisable; images are inverted a second time to stay correct.
  return (
    `<!doctype html><html><head><meta charset="utf-8">` +
    `<meta name="referrer" content="no-referrer">` +
    `<meta http-equiv="Content-Security-Policy" content="${csp}">` +
    `<base target="_blank">` +
    `<style>` +
    `html,body{margin:0;padding:16px;background:#111113;color:#1c1c1e;color-scheme:dark;}` +
    `body{font:14px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Arial,sans-serif;overflow-wrap:break-word;}` +
    // Styling the scrollbar keeps it inside the frame's layer instead of as a
    // compositor overlay that floats above the app's menus and overlays.
    `::-webkit-scrollbar{width:10px;height:10px;}` +
    `::-webkit-scrollbar-track{background:transparent;}` +
    `::-webkit-scrollbar-thumb{background:#2a2a2e;border:2px solid transparent;background-clip:padding-box;border-radius:6px;}` +
    `.mail{filter:invert(1) hue-rotate(180deg);}` +
    `.mail img{filter:invert(1) hue-rotate(180deg);max-width:100%;height:auto;}` +
    `table{max-width:100%;}` +
    `</style>` +
    `</head><body><div class="mail">${linked}</div></body></html>`
  );
}

const TRANSPARENT_PLACEHOLDER =
  "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7";
