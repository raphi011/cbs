// How a file is named and sized on screen. Two rules, in one place because
// three screens render a file: the mesh's list, a payment's documents and the
// viewer's own header.

// A message definition identifier is a family, a variant and a version —
// "pacs.008.001.10". The family and the variant are what names the document;
// the version is what an implementer needs and a reader does not.
export function shortDefinition(msgDefIdr: string): string {
  if (!msgDefIdr) return "file";
  const parts = msgDefIdr.split(".");
  return parts.length >= 2 ? `${parts[0]}.${parts[1]}` : msgDefIdr;
}

// A file's size, which is what a listing carries in place of the file. Bytes up
// to a kilobyte, because a document small enough to count in bytes is one worth
// knowing is nearly empty.
export function formatSize(bytes: number): string {
  return bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} kB`;
}
