package payment

import (
	"context"
	"time"
)

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
func (s *ClearingHouseNetwork) OpenCycleID(ctx context.Context, scheme SchemeID) (CycleID, error) {
	var out CycleID
	err := s.store.View(ctx, func(ctx context.Context, tx CsmTx) error {
		c, err := tx.GetOpenCycle(ctx, scheme)
		out = c.ID
		return err
	})
	return out, err
}

// NetworkWithoutAnIdentity assembles the shared core with the zero Identity,
// which is what newNetwork panics on.
//
// The three constructors each supply an identity of their own, so the shape is
// no longer reachable from outside this package — and it is still worth pinning,
// because the core is what every one of them goes through. The store is nil
// because the panic happens before anything is read.
func NetworkWithoutAnIdentity(clock func() time.Time) {
	newNetwork(nil, clock, Identity{}, newSchemeRegistry())
}

// The four mis-wired handles: one institution's TYPE over another's IDENTITY.
//
// An institution's acts are methods on that institution's type, so reaching
// another's through a handle Networks minted does not compile and there is
// nothing to measure. Since the store split there is one crossing fewer: a
// bank's handle cannot be given the clearing house's DATABASE either, because
// the two are different types. What is still reachable is a handle whose methods
// and whose identity disagree — and because the embedded field is unexported
// (see the note on core in institutions.go), only this package can assemble one.
//
// So each takes two handles: the store comes from the institution whose TYPE is
// being built, and the identity from the other. That is the whole of the
// mis-wiring now, and it is what Network.self and Network.centralBankBook exist
// to refuse.
//
// They are here rather than beside the types because assembling one is not an
// act the system has.
func BankHandleOverClearingHouse(b *BankNetwork, c *ClearingHouseNetwork) *BankNetwork {
	out := *b
	out.core = c.core
	return &out
}

func BankHandleOverCentralBank(b *BankNetwork, c *CentralBankNetwork) *BankNetwork {
	out := *b
	out.core = c.core
	return &out
}

// The two below leave centralBank NIL, which is what centralBankBook refuses. A
// settlement agent's book is minted by newCentralBankNetwork and by nothing
// else, so a handle assembled here has none however it was put together.
func CentralBankHandleOverClearingHouse(cb *CentralBankNetwork, c *ClearingHouseNetwork) *CentralBankNetwork {
	return &CentralBankNetwork{core: c.core, store: cb.store, ledgers: cb.ledgers}
}

func CentralBankHandleOverBank(cb *CentralBankNetwork, b *BankNetwork) *CentralBankNetwork {
	return &CentralBankNetwork{core: b.core, store: cb.store, ledgers: cb.ledgers}
}
