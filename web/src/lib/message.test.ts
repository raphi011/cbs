import { describe, expect, it } from "vitest";

import { formatSize, shortDefinition } from "./message";

describe("shortDefinition", () => {
  it("keeps the family and the variant and drops the version", () => {
    expect(shortDefinition("pacs.008.001.10")).toBe("pacs.008");
    expect(shortDefinition("camt.053.001.08")).toBe("camt.053");
  });

  it("answers whole what it cannot split, and names a file with no header", () => {
    expect(shortDefinition("HRD")).toBe("HRD");
    expect(shortDefinition("")).toBe("file");
  });
});

describe("formatSize", () => {
  it("counts bytes below a kilobyte and kilobytes above one", () => {
    expect(formatSize(0)).toBe("0 B");
    expect(formatSize(1023)).toBe("1023 B");
    expect(formatSize(1024)).toBe("1.0 kB");
    expect(formatSize(4300)).toBe("4.2 kB");
  });
});
