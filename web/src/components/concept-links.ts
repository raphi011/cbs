import { defaultUrlTransform } from "react-markdown";

import { hintContent, type HintKey } from "./hint-content";

// Matches [[key]] and [[key|custom label]].
const LINK_RE = /\[\[([^\]|]+)(?:\|([^\]]+))?\]\]/g;

// react-markdown sanitizes every href through `defaultUrlTransform`, whose
// allow-list (http(s)/irc(s)/mailto/xmpp) rewrites our custom `concept:` scheme
// to "" — leaving the panel's wiki-links pointing at the current page. Let
// `concept:` through untouched; defer everything else so XSS protection (e.g.
// stripping `javascript:`) stays intact. Used as ReactMarkdown's `urlTransform`.
export function conceptUrlTransform(url: string): string {
  return url.startsWith("concept:") ? url : defaultUrlTransform(url);
}

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

// A body of markdown that may contain wiki-links, labelled by where it came
// from so a failure names the file to open.
export interface ConceptSource {
  source: string;
  body: string;
}

// Throws if any body links to a key that isn't in the registry.
//
// `extra` widens the scan beyond the hint bodies. It exists because quiz
// explanations carry wiki-links too, and for a long time nothing checked them:
// this guard runs from RootLayout over `hintContent` alone, so a dangling link
// in a chapter passed `npm run test` and then threw when that explanation was
// rendered. The runtime caller deliberately still passes nothing — pulling all
// sixteen chapters into the client bundle to validate them would cost every
// visitor for a developer's benefit — so the quiz side is covered by the test
// suite instead (see concept-links.test.ts).
export function validateConceptContent(extra: ConceptSource[] = []): void {
  const broken: string[] = [];
  const check = (source: string, body: string) => {
    for (const match of body.matchAll(LINK_RE)) {
      const target = match[1].trim();
      if (!(target in hintContent)) broken.push(`${source} → [[${target}]]`);
    }
  };
  for (const [key, entry] of Object.entries(hintContent)) check(key, entry.body);
  for (const { source, body } of extra) check(source, body);

  if (broken.length > 0) {
    throw new Error(`Unknown concept links:\n${broken.join("\n")}`);
  }
}
