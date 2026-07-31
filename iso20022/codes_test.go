package iso20022

import "testing"

// TestCodeValues pins the wire values. These are external code sets: the
// four-character strings ARE the interface, and a typo here is a message a
// counterparty rejects rather than a compile error.
func TestCodeValues(t *testing.T) {
	tests := []struct {
		got  string
		want string
	}{
		{string(SettlementMethodClearing), "CLRG"},
		{string(ChargeBearerFollowingServiceLevel), "SLEV"},
		{string(ServiceLevelSEPA), "SEPA"},

		{string(GroupStatusAccepted), "ACCP"},
		{string(GroupStatusPartiallyAccepted), "PART"},
		{string(GroupStatusRejected), "RJCT"},

		{string(TransactionStatusAccepted), "ACCP"},
		{string(TransactionStatusSettlementInProcess), "ACSP"},
		{string(TransactionStatusSettlementCompleted), "ACSC"},
		{string(TransactionStatusRejected), "RJCT"},

		{string(StatusReasonIncorrectAccountNumber), "AC01"},
		{string(StatusReasonClosedAccountNumber), "AC04"},
		{string(StatusReasonInsufficientFunds), "AM04"},
		{string(StatusReasonDuplication), "AM05"},
		{string(StatusReasonNoMandate), "MD01"},
		{string(StatusReasonNotSpecifiedAgentGenerated), "MS03"},
		{string(StatusReasonBankIdentifierIncorrect), "RC01"},
		{string(StatusReasonMissingDebtorAccountOrIdentification), "RR01"},
		{string(StatusReasonInvalidCutOffTime), "TM01"},
		{string(StatusReasonInvalidFileFormat), "FF01"},

		{string(ReturnReasonClosedAccountNumber), "AC04"},
		{string(ReturnReasonInsufficientFunds), "AM04"},
		{string(ReturnReasonDuplication), "AM05"},
		{string(ReturnReasonNoMandate), "MD01"},
		{string(ReturnReasonNotSpecifiedAgentGenerated), "MS03"},
		{string(ReturnReasonBankIdentifierIncorrect), "RC01"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("code = %q, want %q", tt.got, tt.want)
		}
	}
}
