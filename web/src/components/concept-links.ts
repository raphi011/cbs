import { hintContent, type HintKey } from "./hint-content";

// Matches [[key]] and [[key|custom label]].
const LINK_RE = /\[\[([^\]|]+)(?:\|([^\]]+))?\]\]/g;

// Rewrite wiki-links to standard markdown links with a `concept:` scheme so
// react-markdown renders them and our custom <a> can intercept them. The label
// defaults to the target concept's title.
export function preprocessConceptMarkdown(body: string): string {
  return body.replace(LINK_RE, (_match, rawKey: string, label?: string) => {
    const key = rawKey.trim();
    const text = (label ?? hintContent[key as HintKey]?.title ?? key).trim();
    return `[${text}](concept:${key})`;
  });
}

// Distinct, valid concept keys referenced by a body — used for the "Related" row.
export function parseConceptLinks(body: string): HintKey[] {
  const keys = new Set<HintKey>();
  for (const match of body.matchAll(LINK_RE)) {
    const key = match[1].trim();
    if (key in hintContent) keys.add(key as HintKey);
  }
  return [...keys];
}

// Dev-time guard: throws if any body links to a key that isn't in the registry.
export function validateConceptContent(): void {
  const broken: string[] = [];
  for (const [key, entry] of Object.entries(hintContent)) {
    for (const match of entry.body.matchAll(LINK_RE)) {
      const target = match[1].trim();
      if (!(target in hintContent)) broken.push(`${key} → [[${target}]]`);
    }
  }
  if (broken.length > 0) {
    throw new Error(`Unknown concept links:\n${broken.join("\n")}`);
  }
}
