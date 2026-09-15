import { describe, expect, it } from "vitest";
import { buildFrameDocument, hasRemoteContent, resolveCids } from "./msghtml";

describe("hasRemoteContent", () => {
  it("detects remote image sources", () => {
    expect(
      hasRemoteContent('<img src="https://tracker.example.com/pixel.gif">'),
    ).toBe(true);
  });

  it("detects unquoted and background attributes", () => {
    expect(
      hasRemoteContent("<body background=https://cdn.example.com/bg.png>"),
    ).toBe(true);
  });

  it("ignores local references", () => {
    expect(hasRemoteContent('<img src="cid:chart@example.com">')).toBe(false);
    expect(hasRemoteContent('<img src="data:image/png;base64,AAAA">')).toBe(
      false,
    );
  });
});

describe("resolveCids", () => {
  it("replaces cid references with resolved data URLs", async () => {
    const html = '<img src="cid:chart@example.com" alt="chart">';
    const result = await resolveCids(
      html,
      async (cid) => `data:image/svg+xml;fake,${cid}`,
    );
    expect(result).toContain("data:image/svg+xml;fake,chart@example.com");
    expect(result).not.toContain("cid:");
  });

  it("substitutes a transparent placeholder for unknown cids", async () => {
    const result = await resolveCids(
      '<img src="cid:unknown@x">',
      async () => null,
    );
    expect(result).toContain("data:image/gif;base64");
    expect(result).not.toContain("cid:");
  });

  it("handles multiple cids", async () => {
    const html = '<img src="cid:a@x"><img src="cid:b@x">';
    const result = await resolveCids(html, async (cid) => `data:${cid}`);
    expect(result).toContain("data:a@x");
    expect(result).toContain("data:b@x");
  });
});

describe("buildFrameDocument", () => {
  const doc = buildFrameDocument("<p>hello</p>");

  it("embeds a default-src none CSP with script execution denied", () => {
    expect(doc).toContain("default-src 'none'");
    expect(doc).toContain("script-src 'none'");
    expect(doc).toContain("connect-src 'none'");
  });

  it("blocks remote images by default and allows them when requested", () => {
    expect(doc).toContain("img-src data: blob:");
    const permissive = buildFrameDocument("<p>x</p>", { allowRemote: true });
    expect(permissive).toContain("img-src data: blob: https: http:");
  });

  it("rewrites links to open outside the sandboxed frame", () => {
    const linked = buildFrameDocument('<a href="https://example.com">x</a>');
    expect(linked).toContain('target="_blank"');
    expect(linked).toContain('rel="noopener noreferrer"');
    expect(linked).toContain('<base target="_blank">');
  });

  it("adds a no-referrer meta so loads do not leak the reader", () => {
    expect(doc).toContain('name="referrer" content="no-referrer"');
  });

  it("wraps the message html in the theme-adapting container", () => {
    expect(doc).toContain('<div class="mail"><p>hello</p></div>');
    expect(doc).toContain(".mail{filter:invert(1) hue-rotate(180deg);}");
    expect(doc.endsWith("</div></body></html>")).toBe(true);
  });
});
