package payment

import (
	"context"
	"time"
)

// Re-exports for the external test package.

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
func NetworkWithoutAnIdentity(clock func() time.Time) {
	newNetwork(nil, nil, clock, Identity{}, newSchemeRegistry())
}

// The four mis-wired handles: one institution's TYPE over another's IDENTITY.
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
