package ledger

import "fmt"

// Assets are defined in code, not stored per book.
//
// An asset definition is a fact about the world rather than per-bank state:
// "BTC has 8 decimal places" is true in every book that ever mentions BTC, and
// two books disagreeing about it would be a bug, not a feature. Storing the
// definition would make that disagreement representable.
//
// The rest of the system already draws the line here. A payment scheme is a Go
// type with an Asset() method, registered in code (see payment/scheme.go); the
// per-scheme *settlements* are rows. Assets work the same way: the definition
// is code, and everything denominated in one — accounts, deposit accounts, a
// bank's per-asset plumbing — is a row that names it.
//
// The earlier design had a writable per-book registry. It could not be honoured,
// because a bank's per-asset plumbing is provisioned once and never extended: a
// bank's own suspense, reserve, unclaimed-balances and returns-receivable
// accounts are created in its own book when it is FOUNDED, and its settlement
// account is opened for it, in another institution's book, when the scheme
// answers its application. Neither moment comes round again, so an asset
// registered after both produced accounts that could never hold money. A list
// the system can actually deliver on is smaller and true.
//
// Adding an asset is a code change, which is the point: the reserve plumbing,
// the schemes that quote it and the chart of accounts all have to move with it.
var knownAssets = []AssetDef{
	{Code: "EUR", Name: "Euro", Scale: 2, Class: Fiat},
	{Code: "USD", Name: "US Dollar", Scale: 2, Class: Fiat},
	{Code: "BTC", Name: "Bitcoin", Scale: 8, Class: Crypto},
}

// LookupAsset returns the definition of a known asset.
//
// Returns ErrAssetNotFound, wrapped with the code, if it is not one. There is
// no default: silently falling back to a base currency is precisely the bug the
// asset dimension exists to prevent.
//
// Named LookupAsset rather than Asset because Asset is already taken — it is
// the AccountType constant for the asset side of the balance sheet.
func LookupAsset(code AssetCode) (AssetDef, error) {
	for _, a := range knownAssets {
		if a.Code == code {
			return a, nil
		}
	}
	return AssetDef{}, fmt.Errorf("%w: %s", ErrAssetNotFound, code)
}

// Assets returns every known asset, in declaration order.
//
// The slice is a copy: callers rendering a list must not be able to edit the
// definitions everything else validates against.
func Assets() []AssetDef {
	out := make([]AssetDef, len(knownAssets))
	copy(out, knownAssets)
	return out
}
