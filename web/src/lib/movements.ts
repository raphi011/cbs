// What a run of the business day moved, in one line. A whole day and a single
// phase report the same three fields, so they are described by the same rule.

import type { DayProblem, FileMoved, TransactionOutcome } from "./types";

export interface Movements {
  files: FileMoved[];
  outcomes: TransactionOutcome[];
  problems: DayProblem[];
}

// countFiles counts crossings and not movements. A file is put where its
// recipient can reach it and taken by that recipient, so a delivered file is two
// entries in a report and one file. Order ids are minted per host, which is why
// the ends are part of the key.
export function countFiles(files: FileMoved[]): number {
  return new Set(files.map((f) => `${f.from}→${f.to}:${f.orderId}`)).size;
}

// describeMovements is the one line a toast carries: what ran, in the terms the
// report is made of. A run that moved nothing says so rather than counting
// three zeros — most phases of most days move nothing, and that is ordinary.
export function describeMovements(m: Movements): string {
  const files = countFiles(m.files);
  const parts: string[] = [];
  if (files > 0) parts.push(`${files} ${files === 1 ? "file" : "files"}`);
  if (m.outcomes.length > 0) {
    parts.push(`${m.outcomes.length} ${m.outcomes.length === 1 ? "decision" : "decisions"}`);
  }
  if (m.problems.length > 0) parts.push(`${m.problems.length} unprocessed`);
  return parts.length > 0 ? parts.join(" · ") : "nothing moved";
}
