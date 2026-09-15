/**
 * Minimal ZIP reader for Office Open XML and OpenDocument attachments.
 *
 * These formats are ZIP containers; the browser's built-in
 * `DecompressionStream("deflate-raw")` inflates the entries, so no archive
 * dependency is bundled. Only stored (0) and deflate (8) entries are handled,
 * which covers every real-world `.docx`/`.xlsx`/`.pptx`/`.odt`/`.ods`/`.odp`.
 */

const EOCD_SIGNATURE = 0x06054b50;
const CENTRAL_SIGNATURE = 0x02014b50;
const LOCAL_SIGNATURE = 0x04034b50;

/** Inflates every entry of a ZIP archive into a name → bytes map. */
export async function unzip(
  data: Uint8Array,
): Promise<Map<string, Uint8Array>> {
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  const eocd = findEndOfCentralDirectory(view);
  if (eocd < 0) throw new Error("zip: end of central directory not found");

  const entryCount = view.getUint16(eocd + 10, true);
  let offset = view.getUint32(eocd + 16, true);
  const entries = new Map<string, Uint8Array>();

  for (let i = 0; i < entryCount; i += 1) {
    if (
      offset + 46 > view.byteLength ||
      view.getUint32(offset, true) !== CENTRAL_SIGNATURE
    ) {
      break;
    }
    const method = view.getUint16(offset + 10, true);
    const compressedSize = view.getUint32(offset + 20, true);
    const nameLength = view.getUint16(offset + 28, true);
    const extraLength = view.getUint16(offset + 30, true);
    const commentLength = view.getUint16(offset + 32, true);
    const localOffset = view.getUint32(offset + 42, true);
    const nameStart = offset + 46;
    const name = new TextDecoder().decode(
      data.subarray(nameStart, nameStart + nameLength),
    );
    const bytes = await readEntry(
      data,
      view,
      localOffset,
      method,
      compressedSize,
    );
    if (bytes) entries.set(name, bytes);
    offset = nameStart + nameLength + extraLength + commentLength;
  }
  return entries;
}

function findEndOfCentralDirectory(view: DataView): number {
  const min = Math.max(0, view.byteLength - 0xffff - 22);
  for (let i = view.byteLength - 22; i >= min; i -= 1) {
    if (view.getUint32(i, true) === EOCD_SIGNATURE) return i;
  }
  return -1;
}

async function readEntry(
  data: Uint8Array,
  view: DataView,
  localOffset: number,
  method: number,
  compressedSize: number,
): Promise<Uint8Array | null> {
  if (
    localOffset + 30 > view.byteLength ||
    view.getUint32(localOffset, true) !== LOCAL_SIGNATURE
  ) {
    return null;
  }
  const nameLength = view.getUint16(localOffset + 26, true);
  const extraLength = view.getUint16(localOffset + 28, true);
  const start = localOffset + 30 + nameLength + extraLength;
  const raw = data.subarray(start, start + compressedSize);
  if (method === 0) return raw;
  if (method !== 8) return null;
  return inflateRaw(raw);
}

async function inflateRaw(raw: Uint8Array): Promise<Uint8Array> {
  // slice() gives a fresh ArrayBuffer-backed view, which BlobPart requires.
  const stream = new Blob([raw.slice()])
    .stream()
    .pipeThrough(new DecompressionStream("deflate-raw"));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}
