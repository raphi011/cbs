// Cents are the source of truth everywhere. These helpers convert between
// integer minor units (what the API speaks) and the major-unit strings humans
// read and type. Default currency is EUR (the backend pre-registers SEPA).

const DEFAULT_CURRENCY = "EUR";

const fmt = new Intl.NumberFormat("en-IE", {
  style: "currency",
  currency: DEFAULT_CURRENCY,
});

// formatCents renders integer cents as a currency string, e.g. 3000 -> "€30.00".
export function formatCents(cents: number): string {
  return fmt.format(cents / 100);
}

// formatSigned renders with an explicit leading sign, for net positions and
// signed balances, e.g. -2500 -> "-€25.00", 2500 -> "+€25.00".
export function formatSigned(cents: number): string {
  const sign = cents > 0 ? "+" : cents < 0 ? "-" : "";
  return `${sign}${fmt.format(Math.abs(cents) / 100)}`;
}

// parseDollars converts a major-unit string ("30", "30.5", "30.00") to integer
// cents. Returns null on empty or non-numeric input so callers can validate.
export function parseDollars(input: string): number | null {
  const trimmed = input.trim();
  if (trimmed === "") return null;
  const value = Number(trimmed);
  if (Number.isNaN(value)) return null;
  return Math.round(value * 100);
}

// centsToInput renders cents as an editable major-unit string ("30.00").
export function centsToInput(cents: number): string {
  return (cents / 100).toFixed(2);
}
