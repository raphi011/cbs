// Date helpers. The API speaks RFC3339 timestamps (createdAt, valueDate, …) and
// one calendar-day field: snapshot `date` as "YYYY-MM-DD".

// formatDateTime renders an RFC3339 timestamp for display, e.g. "23 Jun 2026,
// 14:37". Returns "—" for empty/zero values.
export function formatDateTime(iso?: string): string {
  if (!iso || iso.startsWith("0001-01-01")) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("en-GB", {
    day: "2-digit",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// formatDate renders just the calendar day, e.g. "23 Jun 2026".
export function formatDate(iso?: string): string {
  if (!iso || iso.startsWith("0001-01-01")) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleDateString("en-GB", {
    day: "2-digit",
    month: "short",
    year: "numeric",
  });
}

// formatBusinessDate renders a plain calendar day, e.g. "Mon 15 Sep 2025".
//
// It parses the parts by hand rather than handing the string to Date, and that
// is the whole reason it exists beside formatDate: `new Date("2025-09-15")` is
// UTC midnight, and rendering that in the reader's own zone shows the 14th for
// anybody west of Greenwich. A business date has no time of day to be shifted
// by, so it is built as a LOCAL date and never converted.
//
// The weekday is part of it, because half of what the readout says is why the
// scheme is shut and "Sat" is the answer on two days in seven.
export function formatBusinessDate(date?: string): string {
  const parts = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date ?? "");
  if (!parts) return "—";
  const [, y, m, d] = parts;
  return new Date(Number(y), Number(m) - 1, Number(d)).toLocaleDateString("en-GB", {
    weekday: "short",
    day: "2-digit",
    month: "short",
    year: "numeric",
  });
}

// todayDateString returns today's calendar day as "YYYY-MM-DD" for snapshot
// date inputs and <input type="date"> defaults.
export function todayDateString(): string {
  return new Date().toISOString().slice(0, 10);
}
