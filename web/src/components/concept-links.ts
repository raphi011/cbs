import { defaultUrlTransform } from "react-markdown";

import { hintContent, type HintKey } from "./hint-content";

// The syntax of a wiki-link: [[key]] and [[key|custom label]].
const LINK = String.raw`\[\[([^\]|]+)(?:\|([^\]]+))?\]\]`;

// Markdown's verbatim regions: fenced blocks and inline spans. A code region
// renders exactly the characters it contains, so a [[wiki-link]] inside one is
// text the author wants shown — and rewriting it put the literal string
// "[Clearing suspense](concept:clearing-suspense)" in monospace inside a hint's
// ledger diagram. A code block is also where an author is *most* likely to
// write something that only looks like a wiki-link, so the rule belongs here
// rather than in the one body that tripped over it.
//
// Not modelled: fences of four or more backticks, and inline spans containing a
// backtick. Neither appears in the registry, and an unmatched fence falls
// through to the link pattern — i.e. to the old behaviour — rather than
// swallowing the rest of the body.
const CODE = String.raw`\x60{3}[\s\S]*?\x60{3}|~{3}[\s\S]*?~{3}|\x60[^\x60\n]*\x60`;

// One pattern, with the verbatim regions first so each is consumed whole and
// never seen as a link. Group 1 is a code region; groups 2 and 3 are a link's
// key and label.
const CODE_OR_LINK_RE = new RegExp(`(${CODE})|${LINK}`, "g");

// Every wiki-link in a body that markdown will render as a link, in order.
// The rewrite below replaces over the same pattern; the "Related" row and the
// registry guard read through here, so all three agree on which links exist.
function renderedLinks(body: string): Array<{ key: string; label?: string }> {
  const out: Array<{ key: string; label?: string }> = [];
  for (const m of body.matchAll(CODE_OR_LINK_RE)) {
    if (m[1] !== undefined) continue; // a code region, not a link
    out.push({ key: m[2].trim(), label: m[3]?.trim() });
  }
  return out;
}

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
  return body.replace(
    CODE_OR_LINK_RE,
    (_match, code: string | undefined, rawKey: string, label?: string) => {
      // A verbatim region: put it back exactly as written.
      if (code !== undefined) return code;
      const key = rawKey.trim();
      const text = (label ?? hintContent[key as HintKey]?.title ?? key).trim();
      return `[${text}](concept:${key})`;
    },
  );
}

// Distinct, valid concept keys referenced by a body — used for the "Related" row.
export function parseConceptLinks(body: string): HintKey[] {
  const keys = new Set<HintKey>();
  for (const { key } of renderedLinks(body)) {
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
//
// Links inside a code region are not checked, because they are not links: the
// rewrite leaves them alone, so nothing ever resolves the key and a dangling one
// cannot throw. Demanding they resolve would fail an author for quoting
// wiki-link syntax in an example.
export function validateConceptContent(extra: ConceptSource[] = []): void {
  const broken: string[] = [];
  const check = (source: string, body: string) => {
    for (const { key } of renderedLinks(body)) {
      if (!(key in hintContent)) broken.push(`${source} → [[${key}]]`);
    }
  };
  for (const [key, entry] of Object.entries(hintContent)) check(key, entry.body);
  for (const { source, body } of extra) check(source, body);

  if (broken.length > 0) {
    throw new Error(`Unknown concept links:\n${broken.join("\n")}`);
  }
}
