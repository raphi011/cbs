package ledger

import "context"

// The ledger package's own tests live in package ledger_test, because they
// build a Book over a store from store/testenv, which reaches store/sqlite,
// which imports ledger — an in-package test file importing it would be an
// import cycle. The two helpers below re-export the unexported API those tests
// still need to reach. Being in a _test.go file, they exist only during
// `go test` and never widen the public API.

// CodeBlock exposes codeBlock for TestAccountTypeCodeBlock.
func (t AccountType) CodeBlock() int { return t.codeBlock() }

// AppendAuditTx exposes appendAuditTx for TestAuditPayloadIsSnapshotNotReference,
// which appends an event for an entity it still holds a pointer to and then
// mutates it.
func (s *Book) AppendAuditTx(ctx context.Context, tx Tx, scope Scope, eventType, entityID string, payload any) error {
	return s.appendAuditTx(ctx, tx, scope, eventType, entityID, payload)
}
