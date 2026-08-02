# Task 14 — The Message Carries the Parties: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop a submitting bank reading the counterparty's deposit register for a
name, and stop a receiving bank sweeping every member's register to resolve an
address — by putting what a message says about each party onto the request and
onto the payment.

**Architecture:** A new value type `payment.PartyDetails` (agent BIC + account
holder's name) travels on `InitiatePaymentRequest` and is stored on `Payment`. The
submitting bank fills its **own** side from its own register and is **told** the
counterparty's. Message building then reads the payment instead of the store, so
`partyTx` — the one call the book recorder sees crossing a bank boundary —
disappears. On the receiving side, resolution narrows to the bank's own customer:
the creditor on a push, the debtor on a pull.

**Tech Stack:** Go 1.x, `store/mem` + `store/pg` (Postgres), `store/storetest`
conformance suite, Next.js/TypeScript web app, vitest.

## Global Constraints

- **`store/pg` must never accept or refuse a write differently from `store/mem`.**
  `store/storetest` is the conformance suite that enforces it. Both runs must stay
  green.
- **One migration.** There is no deployed database; `store/pg/schema/0001_init.sql`
  is edited in place, never layered on. Its comments are domain content.
- **Documentation is duplicated by design.** A domain fact corrected in one of
  `README.md` (authoritative), `web/src/components/hint-content.ts`,
  `web/src/lib/quiz/chapters/*.ts`, `store/pg/schema/0001_init.sql` must be
  checked in the others.
- **A `[[wiki-link]]` to a key missing from `hint-content.ts`** throws at runtime
  under `RootLayout` and takes every route down while `next build` stays green.
  `npm run test` catches it in hint bodies *and* quiz explanations.
- **`rm -rf web/.next` before `npm run typecheck`.**
- **Never hand-write a backend path** in the web app; use `cb()` / `csm()` /
  `bank(pid, …)`.
- **`web/src/lib/quiz/diversity.test.ts`** holds each chapter to 18–22 questions,
  ≥8 distinct `concept` tags, no tag more than 3×, and all three difficulty tiers.
- **Every behavioural claim is watched failing.** For each measurement this plan
  moves: break the code, watch that exact test fail, restore. A test whose failure
  nobody has observed is not evidence.

## Verification (run before every commit that touches Go)

```bash
gofmt -l . && go vet ./... && go build ./...
go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
```

Web changes additionally:

```bash
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

`make test-pg` uses Docker and does not work on this machine. Postgres is local
Homebrew; use `TEST_DATABASE_URL` directly.

---

## File Structure

| file | responsibility in this task |
|---|---|
| `payment/types.go` | new `PartyDetails`; two fields on `Payment` |
| `payment/system.go` | `InitiatePaymentRequest` gains two fields; `SubmitPaymentTx` fills the local side and requires the counterparty's; new sentinel error |
| `payment/errors.go` | `ErrCounterpartyNotNamed` |
| `payment/translate.go` | `partyTx` deleted; `partiesOfTx` becomes pure; `partiesIn` narrows to the local side |
| `store/mem/tx_payment.go` | round-trip the two new fields |
| `store/pg/tx_payment.go` | round-trip the two new fields |
| `store/pg/schema/0001_init.sql` | four columns + a `COMMENT ON COLUMN` explaining why the name is stored rather than looked up |
| `store/storetest/payment.go` | conformance: the new fields survive a round trip |
| `mesh/books_test.go` | the two measurements this task moves |
| `api/dto_payment.go` | request/response DTOs |
| `seed/seed.go` | seeded payments name their counterparties |
| `web/src/lib/types.ts`, the send page | the two new form fields |
| `README.md`, `hint-content.ts`, quiz chapters | the domain fact |

---

## Task 14.1: `PartyDetails` on the payment, stored and round-tripped

**Files:**
- Modify: `payment/types.go` (add type + two fields on `Payment`)
- Modify: `store/mem/tx_payment.go`, `store/pg/tx_payment.go`
- Modify: `store/pg/schema/0001_init.sql:604-630`
- Test: `store/storetest/payment.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `payment.PartyDetails{Agent iso20022.BIC; Name string}`, and
  `Payment.DebtorDetails` / `Payment.CreditorDetails` of that type. Every later
  sub-task reads these names.

- [ ] **Step 1: Write the failing conformance test**

In `store/storetest/payment.go`, inside the existing payment suite, add a subtest.
Follow the file's existing registration style — find how
`PutIsAnUpsertAndDeepCopies` is registered and add alongside it.

```go
// PaymentRoundTripsPartyDetails pins that what a MESSAGE says about each side —
// the agent's BIC and the account holder's name — survives a round trip through
// both stores.
//
// It is stored rather than looked up because looking it up is a read of another
// bank's deposit register, which is the crossing sub-project 8 exists to remove.
// A store that dropped these fields would send the name-reading code back.
func paymentRoundTripsPartyDetails(t *testing.T, st payment.Store) {
	ctx := context.Background()
	p := samplePayment("pay_details")
	p.DebtorDetails = payment.PartyDetails{Agent: "AURODEFFXXX", Name: "Ada Lovelace"}
	p.CreditorDetails = payment.PartyDetails{Agent: "BRVODEFFXXX", Name: "Grace Hopper"}

	if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		return tx.PutPayment(ctx, p)
	}); err != nil {
		t.Fatalf("PutPayment: %v", err)
	}

	var got payment.Payment
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		var err error
		got, err = tx.GetPayment(ctx, p.ID)
		return err
	}); err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if got.DebtorDetails != p.DebtorDetails {
		t.Errorf("debtor details round-tripped as %+v, want %+v", got.DebtorDetails, p.DebtorDetails)
	}
	if got.CreditorDetails != p.CreditorDetails {
		t.Errorf("creditor details round-tripped as %+v, want %+v", got.CreditorDetails, p.CreditorDetails)
	}
}
```

If `samplePayment` does not exist under that name in the file, use whatever
constructor the neighbouring subtests use and set `ID` to `"pay_details"`.

- [ ] **Step 2: Run it and watch it fail to compile**

```bash
cd ~/Git/cbs-db-per-entity && go test ./store/... 2>&1 | head -20
```

Expected: FAIL — `p.DebtorDetails undefined (type payment.Payment has no field or method DebtorDetails)`.

- [ ] **Step 3: Add the type and the fields**

In `payment/types.go`, immediately after the `PartyRef` block (which ends at the
`SameParty` method, around line 196):

```go
// PartyDetails is what a MESSAGE says about one side of a payment: which bank
// holds the account, and the name on it. It is the whole of what a counterparty
// bank is told, and the whole of what building an outbound message needs.
//
// It is SEPARATE from PartyRef because the two answer different questions and
// only one of them is answerable locally. A PartyRef names an account this
// system can look up; PartyDetails is a statement made in a message, which the
// receiving bank has no way to verify and no business verifying.
//
// # Why it is stored on the payment rather than resolved
//
// It used to be resolved: payment.partyTx read the account out of the party's
// own bank's deposit register to get the name on it. That is a read of ANOTHER
// BANK'S BOOK on the happy path of every submission, measured by the recorder in
// mesh/books_test.go and recorded there at length. A real payer's bank knows the
// payee's name because the payer typed it in — it has no access to the payee's
// bank's records at all — so the name travels on the instruction.
//
// Storing it is therefore not a cache. There is nothing to fall back to.
type PartyDetails struct {
	// Agent is the BIC of the bank holding this party's account.
	Agent iso20022.BIC
	// Name is the account holder's name as quoted on the instruction. It is not
	// checked against the register even for a local party, because the name on a
	// message is what the payer asserted and a bank's own record of its customer
	// may legitimately differ.
	Name string
}
```

Then in the `Payment` struct, after `Creditor PartyRef`:

```go
	// DebtorDetails and CreditorDetails are what a message says about each side.
	// The submitting bank fills its OWN side from its own register and is TOLD
	// the counterparty's; see SubmitPaymentTx.
	DebtorDetails   PartyDetails
	CreditorDetails PartyDetails
```

Confirm `payment/types.go` already imports `github.com/raphi011/cbs/iso20022`. It
does — `Payment` and `Participant` both use it. If the import is not in
`types.go` specifically, add it.

- [ ] **Step 4: Round-trip them in `store/mem`**

In `store/mem/tx_payment.go`, find the deep-copy helper the file uses for
`Payment` (the `Put*` contract requires a deep copy). `PartyDetails` holds two
strings and is copied by assignment, so the existing struct copy already carries
it — **verify** by reading the copy function. If it copies field-by-field rather
than by struct assignment, add the two fields.

- [ ] **Step 5: Round-trip them in `store/pg`**

Add to `store/pg/schema/0001_init.sql`, inside `CREATE TABLE payments` after
`creditor_identifier_value`:

```sql
    debtor_agent               TEXT NOT NULL DEFAULT '',
    debtor_name                TEXT NOT NULL DEFAULT '',
    creditor_agent             TEXT NOT NULL DEFAULT '',
    creditor_name              TEXT NOT NULL DEFAULT '',
```

And a comment after the existing `COMMENT ON COLUMN payments.reject_code` block:

```sql
COMMENT ON COLUMN payments.debtor_name IS
    'The account holder name as QUOTED ON THE INSTRUCTION, not as held in the '
    'register. It is stored rather than looked up because looking it up means '
    'reading the counterparty bank''s deposit register, which no bank may do. '
    'A real payer''s bank knows the payee''s name because the payer typed it. '
    'The four agent/name columns are therefore not a cache: there is nothing '
    'to fall back to, and a NULL here would be an unsendable payment.';
```

Then update the `INSERT`/`SELECT` in `store/pg/tx_payment.go` to carry the four
columns. Match the existing column ordering exactly — the file lists columns
explicitly rather than using `SELECT *`.

- [ ] **Step 6: Run the conformance suite against both stores**

```bash
cd ~/Git/cbs-db-per-entity && go test ./store/...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./store/...
```

Expected: PASS on both.

- [ ] **Step 7: Watch it fail for real**

Comment out the `creditor_name` column in the `SELECT` in `store/pg/tx_payment.go`
(return `""` instead). Re-run the Postgres suite. Expected: FAIL on
`creditor details round-tripped as`. Restore.

This is the "watched failing" discipline the Global Constraints require; do it
for each measurement in this plan, not only here.

- [ ] **Step 8: Commit**

```bash
cd ~/Git/cbs-db-per-entity
git add payment/types.go store/mem/tx_payment.go store/pg/tx_payment.go \
        store/pg/schema/0001_init.sql store/storetest/payment.go
git commit -m "feat(payment): what a message says about each party, stored on the payment

PartyDetails is the agent's BIC and the account holder's name: the whole of what
a counterparty bank is told, and the whole of what building an outbound message
needs. It is stored rather than resolved because resolving it means reading
another bank's deposit register, which is the crossing sub-project 8 removes."
```

---

## Task 14.2: The submitting bank fills its own side and is told the counterparty's

**Files:**
- Modify: `payment/system.go:1012-1021` (`InitiatePaymentRequest`), `:1134-1242` (`SubmitPaymentTx`)
- Modify: `payment/errors.go`
- Test: `payment/system_test.go`

**Interfaces:**
- Consumes: `payment.PartyDetails` from 14.1.
- Produces: `InitiatePaymentRequest.DebtorDetails` / `.CreditorDetails`;
  `payment.ErrCounterpartyNotNamed`. Task 14.3 relies on
  `Payment.DebtorDetails`/`.CreditorDetails` being populated by every submission.

- [ ] **Step 1: Write the failing tests**

Add to `payment/system_test.go`:

```go
// TestSubmitTakesTheCounterpartyNameFromTheRequest pins the direction rule: the
// submitting bank fills in its OWN side from its own register and is TOLD the
// counterparty's. It never reads the counterparty's register for either.
func TestSubmitTakesTheCounterpartyNameFromTheRequest(t *testing.T) {
	// Use the package's existing network fixture. Follow whatever
	// TestSubmitDoesNotCheckTheCreditorAccount uses to build one.
	net, debtor, creditor := newTwoBankNetwork(t)

	req := creditTransferRequest(debtor, creditor)
	req.CreditorDetails = payment.PartyDetails{Agent: creditor.BIC, Name: "Whoever The Payer Typed"}

	p, err := net.SubmitPayment(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitPayment: %v", err)
	}
	if got := p.CreditorDetails.Name; got != "Whoever The Payer Typed" {
		t.Errorf("creditor name is %q, want the name the request carried", got)
	}
	// The debtor is this bank's own customer, so its name comes from its own
	// register and NOT from the request — a payer does not get to rename
	// themselves on an instruction.
	if p.DebtorDetails.Name == "" {
		t.Error("the submitting bank did not fill in its own customer's name")
	}
	if p.DebtorDetails.Agent != debtor.BIC {
		t.Errorf("debtor agent is %q, want the submitting bank's own BIC %q", p.DebtorDetails.Agent, debtor.BIC)
	}
}

// TestSubmitRefusesAnUnnamedCounterparty pins that the instruction must carry
// what the message will need. Before this, a request that named no counterparty
// was accepted and the failure surfaced later, when the message was built, out of
// a bank's own register — which is exactly the read being removed.
func TestSubmitRefusesAnUnnamedCounterparty(t *testing.T) {
	net, debtor, creditor := newTwoBankNetwork(t)

	for _, tc := range []struct {
		name    string
		details payment.PartyDetails
	}{
		{"no agent", payment.PartyDetails{Name: "Grace Hopper"}},
		{"no name", payment.PartyDetails{Agent: creditor.BIC}},
		{"neither", payment.PartyDetails{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := creditTransferRequest(debtor, creditor)
			req.CreditorDetails = tc.details
			if _, err := net.SubmitPayment(context.Background(), req); !errors.Is(err, payment.ErrCounterpartyNotNamed) {
				t.Errorf("got %v, want ErrCounterpartyNotNamed", err)
			}
		})
	}
}
```

Replace `newTwoBankNetwork` and `creditTransferRequest` with the fixture helpers
the file already has — read the top of `payment/system_test.go` and use those
names. Do not add new fixtures if equivalents exist.

- [ ] **Step 2: Run and watch them fail**

```bash
cd ~/Git/cbs-db-per-entity && go test ./payment/ -run 'TestSubmitTakesTheCounterpartyName|TestSubmitRefusesAnUnnamedCounterparty' -v
```

Expected: FAIL to compile — `req.CreditorDetails undefined`.

- [ ] **Step 3: Add the request fields and the error**

In `payment/errors.go`, alongside the other payment sentinels:

```go
// ErrCounterpartyNotNamed is a submission that did not say who the other side
// is. The instruction must carry the counterparty's agent and name because the
// message it becomes must, and because this bank cannot look either up — the
// account is at another bank, in a register it may not read.
var ErrCounterpartyNotNamed = errors.New("payment: the instruction does not name the counterparty")
```

In `payment/system.go`, in `InitiatePaymentRequest`:

```go
	// DebtorDetails and CreditorDetails are what the instruction says about each
	// side. Only the COUNTERPARTY's is required — and which side that is depends
	// on the scheme's direction, exactly as everything else here does. The
	// submitting bank's own side is filled from its own register and anything
	// supplied for it is ignored, because a payer does not get to rename
	// themselves on an instruction.
	DebtorDetails   PartyDetails
	CreditorDetails PartyDetails
```

- [ ] **Step 4: Fill them in `SubmitPaymentTx`**

`debtorSideTx` and `creditorSideTx` already fetch the local account via
`checkPartyTx`, which returns the `deposit.Account` precisely "so callers that
need more than existence don't have to fetch it again". Use that.

In `SubmitPaymentTx`, after the `push := scheme.Direction() == Push` line and
**before** the side call, validate the counterparty; after the side call, fill the
local side. The counterparty check goes before any posting so a refusal costs
nothing:

```go
	sc := SchemeContext{Network: s, Tx: tx, Now: now}
	push := scheme.Direction() == Push

	// The counterparty is whichever side this bank is not. Checked BEFORE the
	// side call, so an instruction that names nobody is refused before the
	// debtor leg is posted rather than after.
	counterparty := &p.CreditorDetails
	if !push {
		counterparty = &p.DebtorDetails
	}
	if counterparty.Agent == "" || counterparty.Name == "" {
		return Payment{}, ErrCounterpartyNotNamed
	}
	if err := counterparty.Agent.Validate(); err != nil {
		return Payment{}, fmt.Errorf("counterparty agent: %w", err)
	}
	if err := ledger.ValidateText("counterparty name", counterparty.Name); err != nil {
		return Payment{}, err
	}

	if push {
		err = s.debtorSideTx(ctx, tx, scheme, &p, sc)
	} else {
		err = s.creditorSideTx(ctx, tx, scheme, &p, sc)
	}
	if err != nil {
		return Payment{}, err
	}
```

Note `p` is built from `req` above, so `p.CreditorDetails`/`p.DebtorDetails` must
be copied across in the `Payment{...}` literal at `system.go:1196`:

```go
		DebtorDetails:   req.DebtorDetails,
		CreditorDetails: req.CreditorDetails,
```

Then have `debtorSideTx` and `creditorSideTx` overwrite the LOCAL side from the
account and participant they already loaded. In `debtorSideTx`, after the account
is in hand:

```go
	// The submitting bank's own side comes from its own register, overwriting
	// anything the request supplied: a payer does not rename themselves on an
	// instruction, and this bank is the authority on its own customer.
	part, err := s.participantTx(ctx, tx, p.Debtor.Participant)
	if err != nil {
		return err
	}
	p.DebtorDetails = PartyDetails{Agent: part.BIC, Name: acct.Name}
```

and the mirror in `creditorSideTx` using `p.Creditor` / `p.CreditorDetails`. Read
both functions first — they already resolve the participant for other reasons, in
which case reuse that value rather than resolving twice.

- [ ] **Step 5: Run the tests**

```bash
cd ~/Git/cbs-db-per-entity && go test ./payment/ -v 2>&1 | tail -30
```

Expected: PASS. Many existing tests in `payment`, `mesh` and `api` will now fail
with `ErrCounterpartyNotNamed` because their fixtures name no counterparty. That
is correct and is Step 6.

- [ ] **Step 6: Fix every fixture that now submits an unnamed counterparty**

```bash
cd ~/Git/cbs-db-per-entity && go test ./... 2>&1 | grep -c 'ErrCounterpartyNotNamed\|counterparty'
```

Work through them. The mechanical fix is to add
`CreditorDetails: payment.PartyDetails{Agent: <the payee bank's BIC>, Name: "<a name>"}`
(or `DebtorDetails` for a pull) to the request the fixture builds. Prefer fixing
the shared fixture helper once over editing every call site.

- [ ] **Step 7: Full verification**

```bash
cd ~/Git/cbs-db-per-entity
gofmt -l . && go vet ./... && go build ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
```

- [ ] **Step 8: Watch the guard fail**

Delete the `counterparty.Agent == "" || counterparty.Name == ""` check. Run
`go test ./payment/ -run TestSubmitRefusesAnUnnamedCounterparty`. Expected: FAIL on
all three subtests. Restore.

- [ ] **Step 9: Commit**

```bash
cd ~/Git/cbs-db-per-entity
git add -A
git commit -m "feat(payment): the instruction names the counterparty, and the bank names itself

A submitting bank fills its OWN side from its own register and is TOLD the
counterparty's, refusing an instruction that names nobody. Which side is which is
the scheme's direction, as everything else here is. Anything supplied for the
bank's own side is ignored: a payer does not rename themselves on an instruction."
```

---

## Task 14.3: `partyTx` dies, and the submitting bank's measurement moves

**Files:**
- Modify: `payment/translate.go:502-575`
- Modify: `mesh/books_test.go:724-744`, `:851-877`
- Test: the two measurements above

**Interfaces:**
- Consumes: `Payment.DebtorDetails` / `.CreditorDetails`, populated by 14.2.
- Produces: `partiesOf(p Payment) (debtor, creditor messageParty)` — **no context,
  no Tx**. Task 15 relies on outbound message building doing no I/O at all.

- [ ] **Step 1: Move the measurement first**

In `mesh/books_test.go`, change `TestWhichBooksEachBankActuallyReaches`:

```go
	assertBooksTouched(t, "the payer's bank", h.booksTouchedBy(h.debtorBIC),
		[]ledger.BookID{h.debtorBook, ledger.NetworkBook})
```

and `TestWhichBooksEachBankReachesInAPull`:

```go
	assertBooksTouched(t, "the payee's bank, submitting a collection", h.booksTouchedBy(h.creditorBIC),
		[]ledger.BookID{h.creditorBook, ledger.NetworkBook})
```

- [ ] **Step 2: Run and watch them fail**

```bash
cd ~/Git/cbs-db-per-entity && go test ./mesh/ -run 'TestWhichBooksEachBank' -v
```

Expected: FAIL — `the payer's bank touched [bank_2 bank_3 network], want [bank_2 network]`.
**This failure is the point of the task.** Record the exact output; it is the
before-measurement.

- [ ] **Step 3: Make `partiesOfTx` pure and delete `partyTx`**

Replace `partiesOf`, `partiesOfTx` and `partyTx` (`translate.go:502-575`) with:

```go
// partiesOf resolves a payment's two sides to what a message says about them:
// each bank's BIC and each account holder's name.
//
// It reads NOTHING. Both are on the payment, put there at submission — the
// bank's own side from its own register, the counterparty's from the
// instruction. That is the whole of this change and the whole of why it matters:
// building an outbound message used to read the counterparty's deposit register
// for the name on the account, which is a read of another bank's book on the
// happy path of every submission.
//
// So the comment this replaces — "the ONLY part of building an outbound message
// that touches the store" — is now false in the strongest way available: no part
// of it does. The four builders below were already pure and this joins them,
// which is why the two Network methods lose their context parameter.
func (s *Network) partiesOf(p Payment) (debtor, creditor messageParty) {
	return messageParty{
			BIC:        p.DebtorDetails.Agent,
			Name:       p.DebtorDetails.Name,
			Identifier: p.Debtor.Identifier,
		}, messageParty{
			BIC:        p.CreditorDetails.Agent,
			Name:       p.CreditorDetails.Name,
			Identifier: p.Creditor.Identifier,
		}
}
```

- [ ] **Step 4: Update the callers**

```bash
cd ~/Git/cbs-db-per-entity && grep -rn "partiesOfTx\|partiesOf(" payment/
```

`CreditTransferMessageTx`, `DirectDebitMessageTx` and their non-Tx pairs call it.
Each loses the `ctx`/`tx` it passed. Where a builder now takes no `ctx` and no
`tx` at all, **keep the `Tx` variant only if it still reads something** — the
pull's `DirectDebitMessageTx` still takes a mandate, which `InstructionTx` loads,
so check each individually rather than deleting `Tx` variants wholesale.

Do **not** change `InstructionTx`'s signature: it still reads the mandate from
the store for a pull, so it legitimately keeps its `Tx`.

- [ ] **Step 5: Run the measurements**

```bash
cd ~/Git/cbs-db-per-entity && go test ./mesh/ -run 'TestWhichBooksEachBank' -v
```

Expected: PASS, both directions.

- [ ] **Step 6: Rewrite the two measurement docs**

Both test doc comments in `books_test.go` contain long sections asserting the
crossing is real and permanent — `# The creditor's book is in it too, and that is
a genuine crossing` and its pull mirror. Those are now **false** and are the most
dangerous kind of stale comment in this repository: prose that outran the code, in
the file whose whole purpose is to distrust exactly that.

Rewrite them to say what is now true and to record what closed it, keeping the
finding's history rather than deleting it — follow the style of the note at
`books_test.go:640-648`, which keeps a wrong claim on the record with the reason
it was wrong.

- [ ] **Step 7: Full verification and commit**

```bash
cd ~/Git/cbs-db-per-entity
gofmt -l . && go vet ./... && go build ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
git add -A
git commit -m "refactor(payment): building an outbound message reads nothing at all

partyTx read the counterparty's deposit account out of the counterparty bank's
register to get the name on it, on the happy path of every submission. It is
gone: both sides are on the payment now. The submitting bank's measured book set
drops to its own book and the network's, in both directions, and the two test
docs that argued the crossing was permanent are rewritten rather than deleted."
```

---

## Task 14.4: The receiving bank resolves only its own customer

**Files:**
- Modify: `payment/translate.go:1250-1275` (`partiesIn`), `:1071-1137`
  (`CreditTransferRequest`, `DirectDebitRequest`)
- Modify: `mesh/books_test.go:724-744`, `:851-877` (the receiver assertions)
- Modify: `mesh/bank.go:206-228`, `:247-265` (the two handler docs)

**Interfaces:**
- Consumes: nothing from 14.1–14.3.
- Produces: `partiesIn` narrowed. Task 18 relies on the receiving bank never
  resolving a party at another bank.

- [ ] **Step 1: Move the measurement first**

In `TestWhichBooksEachBankActuallyReaches`:

```go
	assertBooksTouched(t, "the payee's bank", h.booksTouchedBy(h.creditorBIC),
		[]ledger.BookID{h.creditorBook})
```

In `TestWhichBooksEachBankReachesInAPull`:

```go
	assertBooksTouched(t, "the payer's bank, answering a collection", h.booksTouchedBy(h.debtorBIC),
		[]ledger.BookID{h.debtorBook})
```

- [ ] **Step 2: Run and watch them fail**

```bash
cd ~/Git/cbs-db-per-entity && go test ./mesh/ -run 'TestWhichBooksEachBank' -v
```

Expected: FAIL — the receiver's set is still every bank book.

- [ ] **Step 3: Write the behaviour test for what changes**

Add to `payment/translate_test.go`:

```go
// TestAReceivingBankDoesNotResolveTheSendersCustomer pins the behaviour change
// this narrowing IS, rather than only the book set it produces.
//
// A pacs.008 quoting a debtor IBAN that nobody in this network holds used to be
// refused AC01, because resolution swept every member's register and found
// nothing. That refusal was never this bank's to make: the debtor is the SENDING
// bank's customer, and a receiving bank has no way to know whether that account
// exists. It now resolves its own side only, and a message whose creditor it can
// find is accepted whatever the debtor's address says.
func TestAReceivingBankDoesNotResolveTheSendersCustomer(t *testing.T) {
	net, _, creditor := newTwoBankNetwork(t)
	doc := pacs008Quoting(t, net,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE00-NOBODY-0000"},
		creditorIBANOf(t, creditor))

	req, err := net.CreditTransferRequest(context.Background(), doc)
	if err != nil {
		t.Fatalf("CreditTransferRequest: %v — a receiving bank does not check the sender's customer", err)
	}
	if req.Creditor.Participant == "" {
		t.Error("the receiving bank did not resolve its own customer")
	}
}
```

Use the file's existing helpers for building a pacs.008 rather than the invented
`pacs008Quoting`/`creditorIBANOf` names if equivalents exist — read
`translate_test.go` first.

- [ ] **Step 4: Narrow `partiesIn`**

Replace it with two callers-side resolutions. The rule is the direction rule: on a
push the receiver is the creditor's bank, on a pull it is the debtor's.

```go
// localPartyIn resolves the ONE side of a received message that belongs to this
// bank, and records the other from what the message says.
//
// Which side that is follows from the direction and from nothing else: the
// clearing house routed a pacs.008 by CdtrAgt and a pacs.003 by DbtrAgt, so the
// bank holding this message is the creditor's on a push and the debtor's on a
// pull. It is the same rule that decides which half of a submission runs, said
// about the other end of the wire.
//
// The counterparty is NOT resolved. It used to be — partiesIn swept every
// member's register for both addresses — and that sweep is what made a receiving
// bank reach every bank's book. It also produced a refusal that was never this
// bank's to make: AC01 for a debtor IBAN nobody holds is a statement about the
// SENDING bank's customer, which a receiving bank cannot know anything about.
func (s *Network) localPartyIn(ctx context.Context, ident deposit.Identifier) (PartyRef, error) {
	var ref PartyRef
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		ref, err = s.addressedPartyTx(ctx, tx, ident)
		return err
	})
	return ref, err
}
```

In `CreditTransferRequest`, replace the `partiesIn` call:

```go
	dbtrID, err := identifierIn("DbtrAcct", tx.DbtrAcct)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	cdtrID, err := identifierIn("CdtrAcct", tx.CdtrAcct)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	// The creditor is this bank's own customer on a push; the debtor is the
	// sending bank's and is recorded, not resolved.
	creditor, err := s.localPartyIn(ctx, cdtrID)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	debtor := PartyRef{Identifier: dbtrID}
```

and carry the message's own statements about each party onto the request:

```go
		DebtorDetails:   PartyDetails{Agent: agentIn(tx.DbtrAgt), Name: nameIn(tx.Dbtr)},
		CreditorDetails: PartyDetails{Agent: agentIn(tx.CdtrAgt), Name: nameIn(tx.Cdtr)},
```

`agentIn` and `nameIn` are small readers over the message's existing
`BranchAndFinancialInstitutionIdentification` / `PartyIdentification` shapes.
Check `translate.go` for readers that already do this — the outbound builders
write these elements, so the inverse may already exist under another name. If not,
add them next to `identifierIn`, which is the closest neighbour.

`DirectDebitRequest` is the mirror: resolve the **debtor** locally, record the
creditor.

- [ ] **Step 5: `addressedPartyTx` no longer needs the sweep for the local case**

`addressedPartyTx` drives `ResolveIdentifierTx`, which lists every participant and
asks each for the identifier. That is still the only implementation available
while one store holds every register, and narrowing it to "this bank" needs the
bank's own identity, which `Network` does not have.

**Leave `ResolveIdentifierTx` as it is.** The measurement moves anyway, because
resolving ONE address instead of two halves nothing — it still sweeps. Verify this
before proceeding:

```bash
cd ~/Git/cbs-db-per-entity && go test ./mesh/ -run 'TestWhichBooksEachBank' -v
```

If the receiver's set is still every bank book — which it will be — then this
sub-task cannot land as written, and the honest conclusion is that **the
receiver's measurement belongs to Task 18, not Task 14**, because narrowing it
requires the bank to know which register is its own, which requires per-entity
stores. In that case:

1. Revert the two receiver assertions from Step 1.
2. Record the finding in the spec at
   `docs/superpowers/specs/2026-08-02-db-per-entity-design.md`, in the Task 14 row:
   the receiver's set moves in Task 18.
3. Keep Steps 3–4 — the behaviour change (not resolving the sender's customer) is
   still right and still testable, it just does not move the book set on its own.
4. Commit that finding as its own change with the reasoning.

Do not force the measurement by giving `Network` a bank identity; that is Task
18's design and pre-empting it here would be a guess of exactly the kind
`ops.go`'s own doc warns about.

- [ ] **Step 6: Rewrite the two handler docs**

`mesh/bank.go:212-216` and `:251-253` both describe resolution as "a sweep of the
network's directory" and "resolves both parties BY ADDRESS". Both are now wrong in
the first half. `:222-228` — "the request the first question produces is
deliberately discarded" — stays true and gains force; leave it and reference the
narrowing above it.

- [ ] **Step 7: Full verification and commit**

```bash
cd ~/Git/cbs-db-per-entity
gofmt -l . && go vet ./... && go build ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
git add -A
git commit -m "feat(payment): a receiving bank resolves its own customer and records the other

Which side is its own is the direction rule said about the far end of the wire:
the creditor's bank on a push, the debtor's on a pull. The counterparty is
recorded from what the message says and not resolved, which retires a refusal
that was never this bank's to make — AC01 for a debtor IBAN nobody holds is a
statement about the SENDING bank's customer."
```

---

## Task 14.5: The API, the seed and the web app carry the counterparty

**Files:**
- Modify: `api/dto_payment.go`, `api/handlers_bank_payment.go`
- Modify: `seed/seed.go`
- Modify: `web/src/lib/types.ts`, `web/src/app/customer/[pid]/[did]/send/page.tsx`
- Test: `api/server_test.go`, `seed/seed_test.go`

**Interfaces:**
- Consumes: `InitiatePaymentRequest.CreditorDetails` / `.DebtorDetails` from 14.2.
- Produces: two new JSON fields on the payment request DTO.

- [ ] **Step 1: Write the failing API test**

In `api/server_test.go`, alongside the existing payment-submission cases:

```go
// TestPostPaymentRequiresTheCounterpartyName is the API surface of
// ErrCounterpartyNotNamed. It is a 422 and not a 500: the request is
// well-formed JSON that this system will not act on, which is the same class as
// the addressing refusals TestPaymentAddressingRefusalsAre422 covers.
func TestPostPaymentRequiresTheCounterpartyName(t *testing.T) {
	srv := newTestServer(t)
	body := `{"scheme":"sepa.ct","debtor":{...},"creditor":{...},"amount":1000}`
	rec := srv.post(t, "/payments", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status %d, want 422", rec.Code)
	}
}
```

Fill `debtor`/`creditor` from whatever the neighbouring test uses. Use the
existing test-server helper; do not add a new one.

- [ ] **Step 2: Run and watch it fail**

```bash
cd ~/Git/cbs-db-per-entity && go test ./api/ -run TestPostPaymentRequiresTheCounterpartyName -v
```

- [ ] **Step 3: Add the DTO fields and the error mapping**

In `api/dto_payment.go`, on the initiate-payment request DTO:

```go
	// CreditorAgent and CreditorName are what the payer says about the payee:
	// the BIC of the payee's bank and the name on the account. They are required
	// on a push, because this bank cannot look either up — the account is at
	// another bank, in a register it may not read. DebtorAgent and DebtorName are
	// the same for a pull, where the collecting bank is told about the payer.
	DebtorAgent    string `json:"debtorAgent,omitempty"`
	DebtorName     string `json:"debtorName,omitempty"`
	CreditorAgent  string `json:"creditorAgent,omitempty"`
	CreditorName   string `json:"creditorName,omitempty"`
```

Map them onto `payment.PartyDetails` in the request converter. In `api/errors.go`,
add `ErrCounterpartyNotNamed` to whichever table maps domain errors to 422 —
find where `ErrUnaddressableAccount` is mapped and add it there.

- [ ] **Step 4: Update the seed**

`seed/seed.go` submits payments. Each submission gains the counterparty's agent
and name, taken from the participant and account the seed itself created. Run
`go test ./seed/` — `seed_test.go` asserts the seeded dataset's shape.

- [ ] **Step 5: Update the web app**

Add the two fields to the payment-request type in `web/src/lib/types.ts` and two
inputs to the send form at
`web/src/app/customer/[pid]/[did]/send/page.tsx`. Label them "Payee's bank (BIC)"
and "Payee's name". Never hand-write the backend path — the page already uses
`bank(pid, …)`.

- [ ] **Step 6: Verify the web app and look at it**

```bash
cd ~/Git/cbs-db-per-entity/web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

Then run the app and **load the send page in a browser and look at it**. Per the
handoff's property 6, no test in this repository can catch a web rendering
regression — `vitest` is node-only and `.ts`-only, and a screenshot caught a real
defect during 7b that a page-text transcript provably could not. Take a screenshot
of the form.

- [ ] **Step 7: Full verification and commit**

```bash
cd ~/Git/cbs-db-per-entity
gofmt -l . && go vet ./... && go build ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test && cd ..
git add -A
git commit -m "feat(api,web): the payer says who they are paying

Two fields on the instruction and two inputs on the send form: the payee's bank
and the payee's name. A real payer's bank knows the payee's name because the
payer typed it, which is what this makes true here."
```

---

## Task 14.6: The domain fact, in all four layers

**Files:**
- Modify: `README.md`
- Modify: `web/src/components/hint-content.ts`
- Modify: `web/src/lib/quiz/chapters/11-payment-schemes.ts` or `12-sepa.ts`
- Modify: `store/pg/schema/0001_init.sql` (already done in 14.1 — verify only)

**Interfaces:**
- Consumes: everything above.
- Produces: nothing code-facing.

- [ ] **Step 1: Add the fact to `README.md`**

The fact is: **a payer's bank knows the payee's name because the payer typed it,
not because it can look it up.** Banks hold no register of each other's customers,
and a payment instruction carries the counterparty's name and bank because there
is nowhere else for them to come from. Put it in the section that covers
initiating a payment; `README.md` is authoritative and the other layers are
distilled from it.

- [ ] **Step 2: Add the hint key**

Add a `hint-content.ts` entry for the concept, and use it as a `[[wiki-link]]`
from the README-derived text. **Any `[[wiki-link]]` to a key not in
`hint-content.ts` throws at runtime and takes every route down while
`next build` stays green.**

- [ ] **Step 3: Add one quiz question**

Add a question to chapter 11 or 12 whose keyed answer is the fact above. Check
`diversity.test.ts`'s limits first: 18–22 questions, ≥8 distinct `concept` tags,
no tag more than 3×, all three difficulty tiers. If the chapter is at 22, replace
a weaker question rather than adding.

- [ ] **Step 4: Run the web tests, then load a page**

```bash
cd ~/Git/cbs-db-per-entity/web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

`npm run test` catches a bad wiki-link in hint bodies **and** quiz explanations,
which the runtime guard does not scan. Then start the app and load a page.

- [ ] **Step 5: Commit**

```bash
cd ~/Git/cbs-db-per-entity
git add -A
git commit -m "docs: a payer's bank knows the payee's name because the payer typed it

Four layers, as CLAUDE.md requires: README (authoritative), hint content, the
quiz, and the schema comment added with the columns. None of them said this
before, because until now it was not true here — the name was read out of the
payee's bank's register."
```

---

## Self-Review

**Spec coverage.** This plan covers the Task 14 row of the spec and nothing else,
by design; Tasks 15–19 get their own plans. Within Task 14: counterparty
BIC/name/address on the request (14.2, 14.5), `partyTx` removed (14.3), the
receiving bank resolving its own register only (14.4), and the documentation
layers CLAUDE.md requires (14.6).

**Known risk, flagged rather than hidden.** Task 14.4 Step 5 says plainly that
narrowing the *receiver's* book set may not be achievable in Task 14, because
`ResolveIdentifierTx` sweeps and narrowing it needs a bank identity that
`payment.Network` does not have and that Task 18 is what supplies. The step tells
the implementer exactly what to do in that case — revert the assertion, keep the
behaviour change, record the finding in the spec — rather than leaving them to
force it. The spec's Task 14 row is optimistic on this point and this plan is
where that is caught.

**Type consistency.** `PartyDetails{Agent, Name}` is defined in 14.1 and used
under those field names in 14.2, 14.3, 14.4 and 14.5. `ErrCounterpartyNotNamed` is
defined in 14.2 and consumed in 14.5. `partiesOf(p Payment)` loses its context in
14.3 and no later sub-task passes one.

**Fixture names.** Several steps name fixture helpers (`newTwoBankNetwork`,
`creditTransferRequest`, `pacs008Quoting`, `newTestServer`) that may not exist
under those names. Every such step says to read the neighbouring tests and use the
existing helpers instead. This is deliberate: inventing a parallel fixture set in
a repository this heavily tested is worse than a rename.
