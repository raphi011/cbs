package payment

import "context"

// Re-exports for the external test package.
//
// payment's tests live in package payment_test because they construct a
// store/mem store, and store/mem imports payment — an in-package test file
// importing it back would be an import cycle. The package is dot-imported
// there, so the test bodies read exactly as they did before; this file gives
// them the handful of internals they still need.

// ReasonTable and ReasonFor are translate.go's reason-code table and the
// lookup over it.
//
// The table's tests need no store and were in-package until the translator's
// message tests joined them: those build a whole network, so translate_test.go
// had to move out to package payment_test with the rest, and these two
// re-exports are what that cost. Keeping the two halves in one file is worth
// it — they are one subject, and a reader of the reason table wants the
// message that carries the reason next to it.
var (
	ReasonTable = reasonTable
	ReasonFor   = reasonFor
)

// OpenCycleID returns the ID of the open cycle for a scheme. Tests use it to
// tidy up a cycle they opened, which they used to do by reading the Network's
// openCycle map directly — that map now lives in the store.
func (s *Network) OpenCycleID(ctx context.Context, scheme SchemeID) (CycleID, error) {
	var out CycleID
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		c, err := tx.GetOpenCycle(ctx, scheme)
		out = c.ID
		return err
	})
	return out, err
}
