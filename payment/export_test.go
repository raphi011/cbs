package payment

import "context"

// Re-exports for the external test package.
//
// payment's tests live in package payment_test because they construct a store,
// which reaches store/sqlite, which imports payment — an in-package test file
// importing it back would be an import cycle. The package is dot-imported
// there, so the test bodies read exactly as they did before; this file gives
// them the handful of internals they still need.

// OpenCycleID returns the ID of the open cycle for a scheme. Tests use it to
// tidy up a cycle they opened; the cycle lives in the store, so there is nothing
// on the Network to read it off.
func (s *Network) OpenCycleID(ctx context.Context, scheme SchemeID) (CycleID, error) {
	var out CycleID
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		c, err := tx.GetOpenCycle(ctx, scheme)
		out = c.ID
		return err
	})
	return out, err
}
