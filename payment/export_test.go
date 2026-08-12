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

// The four mis-wired handles: one institution's TYPE over another's core.
//
// An institution's acts are methods on that institution's type, so reaching
// another's through a handle Networks minted does not compile and there is
// nothing to measure. What is still reachable is a handle whose methods and
// whose identity disagree — and because the embedded field is unexported (see
// the note on core in institutions.go), only this package can assemble one.
//
// They are here rather than beside the types because assembling one is not an
// act the system has. The suites that measure Network.self and
// Network.centralBankBook need it and nothing else does.
func BankHandleOverClearingHouse(c *ClearingHouseNetwork) *BankNetwork {
	return &BankNetwork{core: c.core}
}

func BankHandleOverCentralBank(c *CentralBankNetwork) *BankNetwork {
	return &BankNetwork{core: c.core}
}

func CentralBankHandleOverClearingHouse(c *ClearingHouseNetwork) *CentralBankNetwork {
	return &CentralBankNetwork{core: c.core}
}

func CentralBankHandleOverBank(b *BankNetwork) *CentralBankNetwork {
	return &CentralBankNetwork{core: b.core}
}
