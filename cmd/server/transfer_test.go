package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/payment"
)

// The book transfer over HTTP, and the route it is the other side of.
//
// These tests take the seeded harness rather than a bare Server, because the
// pairing is the subject: one address, two routes, and which one carries it is
// decided by whether the payee banks here. Aurora's first two customers are the
// on-us pair — Alice at serial 1 and Aaron at serial 2, under one bank code.

// auroraIBAN is the address of Aurora's account at that serial. Minted the way
// the register mints it, so these tests name the seed's accounts by the only
// thing a payer ever holds.
func auroraIBAN(serial uint64) string { return mustMint(iban.DE, "99999999", serial) }

// aurora is the bank router of whichever bank holds the seed's Alice, and the
// port every transfer below is made on. It is payerRoutes under the name this
// file's tests read by.
func aurora(t *testing.T, s *server) http.Handler {
	t.Helper()
	bic, _ := seededParty(t, s, aliceIBAN)
	h, err := s.BankRoutes(context.Background(), payment.ParticipantID(bic))
	if err != nil {
		t.Fatalf("binding %s's surface: %v", bic, err)
	}
	return h
}

// TestOneAddressIsRefusedAsAPaymentAndCarriedAsATransfer is the pair, and it is
// the whole point of the route: the same instruction a payer would submit is
// refused by the clearing door and performed by the bank's own.
func TestOneAddressIsRefusedAsAPaymentAndCarriedAsATransfer(t *testing.T) {
	srv, _ := newAPIHarness(t)
	h := aurora(t, srv)
	_, alice := seededParty(t, srv, aliceIBAN)
	aaronIBAN := auroraIBAN(2)
	_, aaron := seededParty(t, srv, aaronIBAN)

	before := doJSON(t, h, "GET", "/deposit-accounts/"+string(alice.Account)+"/balance", "", http.StatusOK)

	// The clearing route first. Nothing about the instruction is wrong — two real
	// accounts, a real scheme, an address whose check digits pass — and it is
	// refused because there is no interbank obligation in it to clear.
	rec := postJSON(t, h, "/payments", fmt.Sprintf(
		`{"scheme":"sepa.ct","debtor":{"account":%q},`+
			`"creditor":{"account":"","identifier":{"scheme":"IBAN","value":%q}},`+
			`"amount":1000,"description":"rent","creditorName":"Aaron Apstorp"}`,
		alice.Account, aaronIBAN))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("submitting an on-us instruction = %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
	if msg := rec.Body.String(); !strings.Contains(msg, "book transfer") {
		t.Errorf("the refusal does not name the remedy: %s", msg)
	}

	// And the remedy it names, on the same port, with the same address.
	out := doJSON(t, h, "POST", "/transfers", fmt.Sprintf(
		`{"from":%q,"to":%q,"amount":1000,"description":"rent"}`, alice.Account, aaronIBAN),
		http.StatusOK)
	if out["transactionId"] == "" {
		t.Error("the transfer answers no transaction id, so nothing names the posting")
	}
	assertEqual(t, "the resolved payee", out["to"].(string), string(aaron.Account))

	// 200 means finished, so the balance in the answer is the balance a second
	// request reads. Nobody else has to agree to it.
	bal := out["balance"].(map[string]any)
	assertEqual(t, "the payer's book balance", int64(bal["book"].(float64)),
		int64(before["book"].(float64))-1000)
	again := doJSON(t, h, "GET", "/deposit-accounts/"+string(alice.Account)+"/balance", "", http.StatusOK)
	assertEqual(t, "the balance read back", int64(again["book"].(float64)), int64(bal["book"].(float64)))
}

// TestATransferNamesNoPaymentAndOpensNoCycle is the negative half of the pairing
// above: a transfer that succeeded left nothing on the routes a payment would
// have appeared on.
func TestATransferNamesNoPaymentAndOpensNoCycle(t *testing.T) {
	srv, msh := newAPIHarness(t)
	h := aurora(t, srv)
	_, alice := seededParty(t, srv, aliceIBAN)

	before := len(doJSONArray(t, h, "GET", "/payments", "", http.StatusOK))
	doJSON(t, h, "POST", "/transfers", fmt.Sprintf(
		`{"from":%q,"to":%q,"amount":1000,"description":"rent"}`, alice.Account, auroraIBAN(2)),
		http.StatusOK)

	// Drained, because the assertion is about messages that were never sent: a
	// clearing payment would have a pacs.008 in flight at this point, and
	// counting the bank's legs before the mesh had settled would pass either way.
	drain(t, msh)
	if after := len(doJSONArray(t, h, "GET", "/payments", "", http.StatusOK)); after != before {
		t.Errorf("the bank holds %d payment legs after a book transfer, want %d", after, before)
	}
}

// TestATransferToAnAddressThisBankDoesNotHoldIsNotFound is the boundary as a
// status code. Bella banks at Banca Verde, so there is nothing here to transfer
// to and the instruction was a payment all along.
func TestATransferToAnAddressThisBankDoesNotHoldIsNotFound(t *testing.T) {
	srv, _ := newAPIHarness(t)
	h := aurora(t, srv)
	_, alice := seededParty(t, srv, aliceIBAN)

	assertStatus(t, h, "POST", "/transfers", fmt.Sprintf(
		`{"from":%q,"to":%q,"amount":1000,"description":"rent"}`, alice.Account, bellaIBAN),
		http.StatusNotFound)
}

// TestATransferRefusesItsOwnPayeeAndItsOwnAmount pins the two refusals that are
// about the request rather than about either account, and the statuses that
// separate them from the ones that are.
func TestATransferRefusesItsOwnPayeeAndItsOwnAmount(t *testing.T) {
	srv, _ := newAPIHarness(t)
	h := aurora(t, srv)
	_, alice := seededParty(t, srv, aliceIBAN)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"to the account it came from", fmt.Sprintf(
			`{"from":%q,"to":%q,"amount":1000,"description":"rent"}`, alice.Account, aliceIBAN),
			http.StatusBadRequest},
		{"of nothing", fmt.Sprintf(
			`{"from":%q,"to":%q,"amount":0,"description":"rent"}`, alice.Account, auroraIBAN(2)),
			http.StatusBadRequest},
		// More than the payer has and no overdraft behind it: the account's
		// state refusing a well-formed request, which is the other category.
		{"beyond the available balance", fmt.Sprintf(
			`{"from":%q,"to":%q,"amount":100000000,"description":"rent"}`, alice.Account, auroraIBAN(2)),
			http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertStatus(t, h, "POST", "/transfers", tc.body, tc.want)
		})
	}
}
