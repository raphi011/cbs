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

// todayDateString returns today's calendar day as "YYYY-MM-DD" for snapshot
// date inputs and <input type="date"> defaults.
export function todayDateString(): string {
  return new Date().toISOString().slice(0, 10);
}
