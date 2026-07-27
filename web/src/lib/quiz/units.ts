// What a numeric question's answer is denominated in.
//
// The rest of the app resolves an asset from `GET /assets` (`useAssetLookup`,
// `lib/money.ts`) and never assumes a scale. The quiz cannot use that route: it
// is static content that has to render with no backend behind it, and the
// endpoint carries a scale and a display name but no word for the minor unit —
// "satoshi" appears nowhere on the wire. So the codes are repeated here, which
// is the same deliberate duplication the domain content already lives with, and
// the union makes an unrecognised one a type error rather than a wrong label.
//
// Keep in step with `knownAssets` in ledger/asset.go.
export type AssetCode = "EUR" | "USD" | "BTC";

/**
 * The asset an answer is counted in, and whether it counts that asset's major
 * units (dollars, euros, bitcoin) or its minor units (cents, satoshi).
 *
 * Optional on a question: an answer that is a count of entries, or a scale, has
 * no asset and renders as a bare number.
 */
export interface NumericUnit {
  asset: AssetCode;
  in: "major" | "minor";
}

interface UnitNames {
  major: string;
  minor: string;
  /** Prefix symbol for a major-unit amount, where the asset has a familiar one. */
  symbol?: string;
}

const NAMES: Record<AssetCode, UnitNames> = {
  EUR: { major: "euros", minor: "cents", symbol: "€" },
  USD: { major: "dollars", minor: "cents", symbol: "$" },
  BTC: { major: "bitcoin", minor: "satoshi" },
};

/** The word shown beside the answer input: "dollars", "cents", "satoshi". */
export function unitLabel(unit: NumericUnit): string {
  const names = NAMES[unit.asset];
  return unit.in === "major" ? names.major : names.minor;
}

/**
 * The correct answer as a reader should see it — "$100", "€25", "2500 cents",
 * "200000 satoshi". A question with no unit renders the bare number.
 *
 * The number is printed as written, not rescaled: a question states its own
 * denomination, so an answer of 2500 cents is 2500 here and not "25.00".
 */
export function formatNumericAnswer(answer: number, unit?: NumericUnit): string {
  if (!unit) return String(answer);
  const names = NAMES[unit.asset];
  if (unit.in === "major" && names.symbol !== undefined) return `${names.symbol}${answer}`;
  return `${answer} ${unitLabel(unit)}`;
}
