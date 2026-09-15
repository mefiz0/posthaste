import { describe, expect, it } from "vitest";
import { unzip } from "./zip";
import { isOfficePreviewable } from "./office";

/** Builds a ZIP with stored (uncompressed) entries; enough for the reader. */
function makeZip(files: Record<string, string>): Uint8Array {
  const encoder = new TextEncoder();
  const locals: Uint8Array[] = [];
  const centrals: Uint8Array[] = [];
  let offset = 0;

  for (const [name, content] of Object.entries(files)) {
    const nameBytes = encoder.encode(name);
    const data = encoder.encode(content);

    const local = new Uint8Array(30 + nameBytes.length + data.length);
    const localView = new DataView(local.buffer);
    localView.setUint32(0, 0x04034b50, true);
    localView.setUint16(4, 20, true);
    localView.setUint16(8, 0, true);
    localView.setUint32(18, data.length, true);
    localView.setUint32(22, data.length, true);
    localView.setUint16(26, nameBytes.length, true);
    local.set(nameBytes, 30);
    local.set(data, 30 + nameBytes.length);
    locals.push(local);

    const central = new Uint8Array(46 + nameBytes.length);
    const centralView = new DataView(central.buffer);
    centralView.setUint32(0, 0x02014b50, true);
    centralView.setUint16(4, 20, true);
    centralView.setUint16(6, 20, true);
    centralView.setUint32(20, data.length, true);
    centralView.setUint32(24, data.length, true);
    centralView.setUint16(28, nameBytes.length, true);
    centralView.setUint32(42, offset, true);
    central.set(nameBytes, 46);
    centrals.push(central);

    offset += local.length;
  }

  const centralSize = centrals.reduce(
    (total, entry) => total + entry.length,
    0,
  );
  const eocd = new Uint8Array(22);
  const eocdView = new DataView(eocd.buffer);
  eocdView.setUint32(0, 0x06054b50, true);
  eocdView.setUint16(8, centrals.length, true);
  eocdView.setUint16(10, centrals.length, true);
  eocdView.setUint32(12, centralSize, true);
  eocdView.setUint32(16, offset, true);

  const out = new Uint8Array(offset + centralSize + eocd.length);
  let cursor = 0;
  for (const local of locals) {
    out.set(local, cursor);
    cursor += local.length;
  }
  for (const central of centrals) {
    out.set(central, cursor);
    cursor += central.length;
  }
  out.set(eocd, cursor);
  return out;
}

describe("unzip", () => {
  it("reads every stored entry by name", async () => {
    const archive = makeZip({
      "word/document.xml": "<doc>hello</doc>",
      "xl/worksheets/sheet1.xml": "<sheet/>",
    });
    const entries = await unzip(archive);
    expect([...entries.keys()].sort()).toEqual([
      "word/document.xml",
      "xl/worksheets/sheet1.xml",
    ]);
    expect(new TextDecoder().decode(entries.get("word/document.xml"))).toBe(
      "<doc>hello</doc>",
    );
  });

  it("rejects data that is not a zip archive", async () => {
    await expect(unzip(new Uint8Array([1, 2, 3, 4]))).rejects.toThrow();
  });
});

describe("isOfficePreviewable", () => {
  it("recognises the XML office formats and ignores others", () => {
    expect(
      isOfficePreviewable(
        "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
      ),
    ).toBe(true);
    expect(isOfficePreviewable("application/pdf")).toBe(false);
    expect(isOfficePreviewable("text/plain")).toBe(false);
  });
});
