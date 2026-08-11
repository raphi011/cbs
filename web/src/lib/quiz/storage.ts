import type { Response } from "./session";

export interface ChapterProgress {
  bestPct: number;
  lastPct: number;
  /** ISO timestamp of the most recent attempt. */
  lastAttempt: string;
}

/**
 * A session in flight, so leaving mid-way costs nothing.
 *
 * `ids` is what makes a resume safe: the responses are positional, and the
 * session they index into is rebuilt from `seed` against whatever questions the
 * chapter holds now. A length alone would not do — a mixed session is also
 * weighted by which concepts the reader has missed, so finishing a different
 * chapter in between can change which questions this seed selects without
 * changing how many. Storing the ids and comparing them in order is the only
 * check that catches that; a mismatch discards rather than resumes.
 */
export interface SavedSession {
  seed: number;
  index: number;
  ids: string[];
  responses: (Response | null)[];
  streak: number;
}

function keyFor(slug: string): string {
  return `quiz:${slug}`;
}

function sessionKeyFor(slug: string): string {
  return `quiz:session:${slug}`;
}

const MISSED_KEY = "quiz:missed";

function resolveStore(store?: Storage): Storage | null {
  if (store) return store;
  return typeof localStorage !== "undefined" ? localStorage : null;
}

/**
 * Read and parse one key, dropping it if it does not survive `check`.
 *
 * Dropping is the point. A value that cannot be read is a value that can never
 * be read, and leaving it in place means every future visit pays the same parse
 * and returns the same null — with the slot still occupied, so the next write
 * is the only thing that can clear it.
 */
function readJson<T>(store: Storage, key: string, check: (v: unknown) => v is T): T | null {
  const raw = store.getItem(key);
  if (raw === null) return null;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (check(parsed)) return parsed;
  } catch {
    // fall through to the drop below
  }
  store.removeItem(key);
  return null;
}

function isProgress(v: unknown): v is ChapterProgress {
  if (typeof v !== "object" || v === null) return false;
  const p = v as Partial<ChapterProgress>;
  return typeof p.bestPct === "number" && typeof p.lastPct === "number";
}

export function readProgress(slug: string, store?: Storage): ChapterProgress | null {
  const s = resolveStore(store);
  if (!s) return null;
  const parsed = readJson(s, keyFor(slug), isProgress);
  if (!parsed) return null;
  return {
    bestPct: parsed.bestPct,
    lastPct: parsed.lastPct,
    lastAttempt: typeof parsed.lastAttempt === "string" ? parsed.lastAttempt : "",
  };
}

export function recordResult(
  slug: string,
  pct: number,
  now: string,
  store?: Storage,
): ChapterProgress {
  const s = resolveStore(store);
  const prev = s ? readProgress(slug, s) : null;
  const next: ChapterProgress = {
    bestPct: Math.max(pct, prev?.bestPct ?? 0),
    lastPct: pct,
    lastAttempt: now,
  };
  if (s) s.setItem(keyFor(slug), JSON.stringify(next));
  return next;
}

function isSavedSession(v: unknown): v is SavedSession {
  if (typeof v !== "object" || v === null) return false;
  const p = v as Partial<SavedSession>;
  return (
    typeof p.seed === "number" &&
    typeof p.index === "number" &&
    typeof p.streak === "number" &&
    Array.isArray(p.ids) &&
    p.ids.every((id) => typeof id === "string") &&
    Array.isArray(p.responses)
  );
}

/** The session in flight for `slug`, or null if there is none to resume. */
export function readSession(slug: string, store?: Storage): SavedSession | null {
  const s = resolveStore(store);
  if (!s) return null;
  return readJson(s, sessionKeyFor(slug), isSavedSession);
}

export function writeSession(slug: string, session: SavedSession, store?: Storage): void {
  const s = resolveStore(store);
  if (s) s.setItem(sessionKeyFor(slug), JSON.stringify(session));
}

export function clearSession(slug: string, store?: Storage): void {
  const s = resolveStore(store);
  if (s) s.removeItem(sessionKeyFor(slug));
}

function isMissCounts(v: unknown): v is Record<string, number> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/**
 * How often each concept has been missed, across every chapter.
 *
 * It is keyed by concept rather than by question because that is what a reader
 * gets wrong: missing two different questions on `value-date` is one gap, not
 * two, and the next session should draw from the concept rather than repeat the
 * question that exposed it.
 */
export function readMissCounts(store?: Storage): Record<string, number> {
  const s = resolveStore(store);
  if (!s) return {};
  return readJson(s, MISSED_KEY, isMissCounts) ?? {};
}

/** Concepts missed at least once — the `favour` set a session is weighted by. */
export function readMissedConcepts(store?: Storage): Set<string> {
  return new Set(
    Object.entries(readMissCounts(store))
      .filter(([, n]) => typeof n === "number" && n > 0)
      .map(([concept]) => concept),
  );
}

/**
 * Add one miss per concept, and forgive one per concept answered correctly.
 *
 * Forgiving is what keeps the weighting from being a permanent record: a
 * concept missed once and then answered right twice stops being drawn, so the
 * set tracks what a reader currently gets wrong rather than everything they
 * ever did.
 */
export function recordConceptOutcomes(
  missed: readonly string[],
  correct: readonly string[],
  store?: Storage,
): void {
  const s = resolveStore(store);
  if (!s) return;
  const counts = readMissCounts(s);
  for (const c of missed) counts[c] = (counts[c] ?? 0) + 1;
  for (const c of correct) {
    const next = (counts[c] ?? 0) - 1;
    if (next > 0) counts[c] = next;
    else delete counts[c];
  }
  s.setItem(MISSED_KEY, JSON.stringify(counts));
}
