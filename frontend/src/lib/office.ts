/**
 * Inline preview for Office documents. `.docx`/`.xlsx`/`.pptx` (Office Open
 * XML) and `.odt`/`.ods`/`.odp` (OpenDocument) are ZIP+XML, so the text and
 * tables are extracted with the built-in DOMParser after unzipping. Legacy
 * binary formats (`.doc`, `.xls`, `.ppt`) are intentionally not handled.
 */

import { unzip } from "./zip";

export interface OfficeTable {
  name: string;
  rows: string[][];
}

export type OfficePreview =
  | { kind: "text"; paragraphs: string[] }
  | { kind: "table"; tables: OfficeTable[] };

type OfficeKind = "docx" | "xlsx" | "pptx" | "odt" | "ods" | "odp";

const OFFICE_MIME: Record<string, OfficeKind> = {
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
    "docx",
  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "xlsx",
  "application/vnd.openxmlformats-officedocument.presentationml.presentation":
    "pptx",
  "application/vnd.oasis.opendocument.text": "odt",
  "application/vnd.oasis.opendocument.spreadsheet": "ods",
  "application/vnd.oasis.opendocument.presentation": "odp",
};

/** True when the MIME type has an inline preview implemented here. */
export function isOfficePreviewable(mimeType: string): boolean {
  return mimeType.toLowerCase() in OFFICE_MIME;
}

/** Parses an Office attachment into text paragraphs or spreadsheet tables. */
export async function renderOffice(
  bytes: Uint8Array,
  mimeType: string,
): Promise<OfficePreview> {
  const kind = OFFICE_MIME[mimeType.toLowerCase()];
  if (!kind) throw new Error("office: unsupported document type");
  const entries = await unzip(bytes);

  switch (kind) {
    case "docx":
      return {
        kind: "text",
        paragraphs: docxParagraphs(entries.get("word/document.xml")),
      };
    case "pptx":
      return { kind: "text", paragraphs: pptxParagraphs(entries) };
    case "odt":
    case "odp":
      return {
        kind: "text",
        paragraphs: odfParagraphs(entries.get("content.xml")),
      };
    case "xlsx":
      return { kind: "table", tables: xlsxTables(entries) };
    case "ods":
      return { kind: "table", tables: odfTables(entries.get("content.xml")) };
  }
}

function parseXml(bytes: Uint8Array | undefined): Document | null {
  if (!bytes) return null;
  try {
    return new DOMParser().parseFromString(
      new TextDecoder().decode(bytes),
      "application/xml",
    );
  } catch {
    return null;
  }
}

function elements(root: Document | Element, tag: string): Element[] {
  return Array.from(root.getElementsByTagName(tag));
}

// ---------- Word (.docx) ----------

function docxParagraphs(documentXml: Uint8Array | undefined): string[] {
  const doc = parseXml(documentXml);
  if (!doc) return [];
  return elements(doc, "w:p")
    .map((paragraph) =>
      elements(paragraph, "w:t")
        .map((run) => run.textContent ?? "")
        .join(""),
    )
    .filter(
      (line, index, all) =>
        line.length > 0 || (index > 0 && index < all.length - 1),
    );
}

// ---------- PowerPoint (.pptx) ----------

function pptxParagraphs(entries: Map<string, Uint8Array>): string[] {
  const slides = [...entries.keys()]
    .filter((name) => /^ppt\/slides\/slide\d+\.xml$/.test(name))
    .sort((a, b) => slideNumber(a) - slideNumber(b));
  const out: string[] = [];
  slides.forEach((name, index) => {
    const doc = parseXml(entries.get(name));
    if (!doc) return;
    if (index > 0) out.push("");
    for (const run of elements(doc, "a:t")) {
      const text = (run.textContent ?? "").trim();
      if (text) out.push(text);
    }
  });
  return out;
}

function slideNumber(name: string): number {
  return Number(name.replace(/\D/g, "")) || 0;
}

// ---------- OpenDocument text/presentation (.odt/.odp) ----------

function odfParagraphs(contentXml: Uint8Array | undefined): string[] {
  const doc = parseXml(contentXml);
  if (!doc) return [];
  return elements(doc, "text:p")
    .map((paragraph) => paragraph.textContent ?? "")
    .filter(
      (line, index, all) =>
        line.length > 0 || (index > 0 && index < all.length - 1),
    );
}

// ---------- Excel (.xlsx) ----------

function xlsxTables(entries: Map<string, Uint8Array>): OfficeTable[] {
  const shared = sharedStrings(entries.get("xl/sharedStrings.xml"));
  const sheets = [...entries.keys()]
    .filter((name) => /^xl\/worksheets\/sheet\d+\.xml$/.test(name))
    .sort((a, b) => slideNumber(a) - slideNumber(b));
  return sheets.map((name, index) => ({
    name: `Sheet ${index + 1}`,
    rows: xlsxRows(entries.get(name), shared),
  }));
}

function sharedStrings(bytes: Uint8Array | undefined): string[] {
  const doc = parseXml(bytes);
  if (!doc) return [];
  return elements(doc, "si").map((item) =>
    elements(item, "t")
      .map((text) => text.textContent ?? "")
      .join(""),
  );
}

function xlsxRows(
  documentXml: Uint8Array | undefined,
  shared: string[],
): string[][] {
  const doc = parseXml(documentXml);
  if (!doc) return [];
  const rows: string[][] = [];
  for (const row of elements(doc, "row")) {
    const cells: string[] = [];
    for (const cell of elements(row, "c")) {
      const reference = cell.getAttribute("r") ?? "";
      const column = columnIndex(reference.replace(/[0-9]/g, ""));
      const type = cell.getAttribute("t");
      let value = "";
      if (type === "s") {
        const index = Number(elements(cell, "v")[0]?.textContent ?? "-1");
        value = shared[index] ?? "";
      } else if (type === "inlineStr") {
        value = elements(cell, "t")
          .map((text) => text.textContent ?? "")
          .join("");
      } else {
        value = elements(cell, "v")[0]?.textContent ?? "";
      }
      if (column >= 0) cells[column] = value;
    }
    rows.push(cells);
  }
  return rows;
}

function columnIndex(letters: string): number {
  let index = 0;
  for (const character of letters.toUpperCase()) {
    index = index * 26 + (character.charCodeAt(0) - 64);
  }
  return index - 1;
}

// ---------- OpenDocument spreadsheet (.ods) ----------

function odfTables(contentXml: Uint8Array | undefined): OfficeTable[] {
  const doc = parseXml(contentXml);
  if (!doc) return [];
  return elements(doc, "table:table").map((table, index) => ({
    name: table.getAttribute("table:name") ?? `Sheet ${index + 1}`,
    rows: elements(table, "table:table-row").map((row) =>
      elements(row, "table:table-cell").map((cell) => cell.textContent ?? ""),
    ),
  }));
}
