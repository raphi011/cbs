package ledger

import "fmt"

// Assets are defined in code, not stored per book.
var knownAssets = []AssetDef{
	{Code: "EUR", Name: "Euro", Scale: 2, Class: Fiat},
	{Code: "USD", Name: "US Dollar", Scale: 2, Class: Fiat},
	{Code: "BTC", Name: "Bitcoin", Scale: 8, Class: Crypto},
}

// LookupAsset returns the definition of a known asset.
func LookupAsset(code AssetCode) (AssetDef, error) {
	for _, a := range knownAssets {
		if a.Code == code {
			return a, nil
		}
	}
	return AssetDef{}, fmt.Errorf("%w: %s", ErrAssetNotFound, code)
}

// Assets returns every known asset, in declaration order.
func Assets() []AssetDef {
	out := make([]AssetDef, len(knownAssets))
	copy(out, knownAssets)
	return out
}
