// Rates and fractions cross the wire as an integer in millionths, alongside
// the scale that converts it — the same discipline money.ts documents for an
// amount and its asset's scale. `interest.Rate` (an annual rate) and
// `interest.Fraction` (a dimensionless proportion, e.g. a revolving line's
// minimum-payment fraction) share one scale in the Go backend
// (`interest.RateScale == interest.FractionScale == 1_000_000`), and every DTO
// that carries a rate — Facility, DepositAccount — carries that scale beside
// it as `rateScale`. Rendering one must always divide by the response's own
// `rateScale`, never a hardcoded 1_000_000: that field exists precisely so a
// client never has to guess.
//
// facilityDTO.minPayment (a Fraction) has no separate `fractionScale` field on
// the wire and is documented, on the Go side, as sharing `rateScale` — true
// only because both scales are 1e6 today (see api/dto_lending.go's
// facilityDTO comment). Render it through rateScale for the same reason.

// formatRate renders a millionths rate/fraction as a percentage string, e.g.
// (150_000, 1_000_000) -> "15.00%", (20_000, 1_000_000) -> "2.00%".
export function formatRate(value: number, scale: number): string {
  return `${((value / scale) * 100).toFixed(2)}%`;
}

// RATE_SCALE mirrors interest.RateScale / interest.FractionScale for
// COMPOSING a new request. Unlike an asset's scale, a rate's scale is not a
// per-entity fact resolved from a lookup (there is no "rate definition"
// endpoint the way GET /assets is one) — it is a fixed constant the whole
// domain shares, so there is nothing to read before a facility or deposit
// account exists to carry a `rateScale` of its own. Reading an EXISTING rate
// must still go through formatRate above with the response's own rateScale,
// never this constant, in case the two ever diverge.
export const RATE_SCALE = 1_000_000;

// parseRatePercent converts a percentage string ("15", "2.5") typed by a user
// into the millionths integer the API expects. Returns null on empty or
// non-numeric input.
export function parseRatePercent(input: string): number | null {
  const trimmed = input.trim();
  if (trimmed === "") return null;
  const value = Number(trimmed);
  if (Number.isNaN(value)) return null;
  return Math.round((value / 100) * RATE_SCALE);
}
