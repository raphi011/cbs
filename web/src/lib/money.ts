// Amounts are integer minor units *of an asset* — what the API always sends
// and receives, never a float or a string. The asset's `scale` is what
// converts an amount to the major-unit string a human reads and types: 2 for
// EUR (cents), 8 for BTC (satoshi), 0 for an asset with no fractional unit at
// all. There is no default scale — every formatter here takes the asset it is
// rendering. Guessing 2 is exactly the bug this file used to have: dividing by
// a hardcoded 100 rendered 1 BTC (100_000_000 satoshi) as "1,000,000.00".

// AssetScale is the minimum an amount formatter needs to know about an asset:
// its code (for display alongside the number) and its scale (for the
// minor-to-major-unit conversion). `Asset` in types.ts satisfies this
// structurally, so callers can pass either.
export interface AssetScale {
  code: string;
  scale: number;
}

// One Intl.NumberFormat per distinct scale seen so far, reused across calls
// (constructing one is not free, and a network deals in a handful of scales
// at most).
const numberFormats = new Map<number, Intl.NumberFormat>();

function numberFormat(scale: number): Intl.NumberFormat {
  let fmt = numberFormats.get(scale);
  if (!fmt) {
    fmt = new Intl.NumberFormat("en-IE", {
      minimumFractionDigits: scale,
      maximumFractionDigits: scale,
    });
    numberFormats.set(scale, fmt);
  }
  return fmt;
}

// formatAmount renders an integer amount at its asset's scale, e.g.
// (12345, EUR) -> "123.45", (100_000_000, BTC) -> "1.00000000". It renders
// the bare number only — no currency symbol or asset code — because an
// arbitrary asset code like "BTC" is not something Intl's currency
// formatting understands. Callers show the code alongside the number (see
// <Money> in components/money.tsx).
export function formatAmount(amount: number, asset: AssetScale): string {
  return numberFormat(asset.scale).format(amount / 10 ** asset.scale);
}

// formatSigned renders with an explicit leading sign, for net positions and
// signed deltas, e.g. (-2500, EUR) -> "-25.00", (2500, EUR) -> "+25.00".
export function formatSigned(amount: number, asset: AssetScale): string {
  const sign = amount > 0 ? "+" : amount < 0 ? "-" : "";
  return `${sign}${formatAmount(Math.abs(amount), asset)}`;
}

// parseAmount converts a major-unit string ("30", "30.5", "30.00") to an
// integer amount at the asset's scale. Returns null on empty or non-numeric
// input so callers can validate.
export function parseAmount(input: string, asset: AssetScale): number | null {
  const trimmed = input.trim();
  if (trimmed === "") return null;
  const value = Number(trimmed);
  if (Number.isNaN(value)) return null;
  return Math.round(value * 10 ** asset.scale);
}

// amountToInput renders an integer amount as an editable major-unit string at
// the asset's scale, e.g. (3000, EUR) -> "30.00", (100_000_000, BTC) ->
// "1.00000000".
export function amountToInput(amount: number, asset: AssetScale): string {
  return (amount / 10 ** asset.scale).toFixed(asset.scale);
}
