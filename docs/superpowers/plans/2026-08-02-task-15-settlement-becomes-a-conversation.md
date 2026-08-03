# Task 15 — Settlement Becomes a Conversation: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the central bank posting in every member bank's book when a cycle
settles. It posts **one** netting transaction in its own book, is final either
way, and tells each member what happened; each bank posts its mirror leg and its
creditor legs **locally**, in its own unit of work, on advice.

**Architecture:** Settlement splits into three acts by three institutions. The
clearing house nets and instructs (unchanged). The central bank checks each net
payer's **settlement account in its own book**, posts one netting transaction,
answers ACSC or RJCT/AM04, and sends each member a **camt.053** statement of its
reserve account. Each bank posts its mirror leg from that statement and its
creditor legs from the clearing house's existing per-payment pacs.002 fan-out,
which is widened to reach the creditor's bank. The interval between the central
bank's posting and a bank's catching up is the **unreconciled position**, and it
is real.

**Tech Stack:** Go 1.x, `store/mem` + `store/pg` (Postgres), `store/storetest`
conformance suite, `encoding/xml`, Next.js/TypeScript web app, vitest.

## Why this is 15a and 15b, and not the spec's single Task 15

The spec (`docs/superpowers/specs/2026-08-02-db-per-entity-design.md`) schedules
camt.053 in Task 19 but its own settlement flow has the bank posting *"its mirror
leg from the statement and its creditor legs from the payment advices"* — and
that split is load-bearing, not incidental. The reconciliation the whole
sub-project is aiming at is *"suspense returns to zero only if the central bank's
reserve movement and the clearing house's payment list agree"*, which is a check
between **two senders**. If the mirror leg came from the clearing house too there
would be nothing to reconcile.

So camt.053 lands here, as **15a**, and Task 19 keeps the reconciliation it is
actually named for: the closing-balance check, break detection, and the in-system
surface that makes a break visible to an operator rather than only to the test
suite. Record this reallocation in the spec — see Task 15b.4, Step 6.

## The finding that must not be lost: AM04 lives in the mirror leg today

This was measured, not reasoned about, and a naive implementation loses it
silently.

`newMeshHarnessWithAnUnfundedReserve` produces a net payer with a reserve of
nothing, and `TestSettlementIsRefusedWhenAMemberCannotCover` expects RJCT/AM04.
Today that refusal comes from the **mirror leg in the member bank's own book**:
`Reserve at Central Bank` is a `ledger.Asset` account, and
`ledger.Book.checkSufficientBalance` refuses an Asset or Expense account going
below zero.

The central bank's own netting transaction cannot refuse it. A member's
settlement account in `CentralBankBook` is created as `ledger.Liability`
(`payment/system.go:447`), and `checkSufficientBalance` **skips Liability, Equity
and Revenue accounts**. So the moment the mirror leg moves out of
`SettleCycleTx`, a cycle whose net payer holds no reserves is **settled**, and
the shortfall surfaces later, at the bank, as a dead letter.

**Therefore Task 15b.2 adds an explicit reserve check to `SettleCycleTx`,** over
each net payer's settlement account in the central bank's own book, returning
`ledger.ErrInsufficientBalance` so `payment.ReasonFor`'s `borrowedReasons` keeps
mapping it to AM04 exactly as it does now. That is not a workaround: refusing to
take a member's reserve below zero is the central bank declining to extend
uncollateralised intraday credit, which is the decision a settlement agent is
there to make. Step 15b.2/6 watches it fail.

## Global Constraints

- **`store/pg` must never accept or refuse a write differently from `store/mem`.**
  `store/storetest` is the conformance suite that enforces it. Both runs must stay
  green.
- **One migration.** There is no deployed database; `store/pg/schema/0001_init.sql`
  is edited in place, never layered on. Its comments are domain content.
- **Documentation is duplicated by design.** A domain fact corrected or added in
  one of `README.md` (authoritative), `web/src/components/hint-content.ts`,
  `web/src/lib/quiz/chapters/*.ts`, `store/pg/schema/0001_init.sql` must be
  checked in the others — **and in Go/TS comments, including test files.** See
  "A domain-claim correction is a sweep" below.
- **A `[[wiki-link]]` to a key missing from `hint-content.ts`** throws at runtime
  under `RootLayout` and takes every route down while `next build` stays green.
  `npm run test` catches it in hint bodies *and* quiz explanations.
- **`rm -rf web/.next` before `npm run typecheck`.**
- **Never hand-write a backend path** in the web app; use `cb()` / `csm()` /
  `bank(pid, …)`.
- **`web/src/lib/quiz/diversity.test.ts`** holds each chapter to 18–22 questions,
  ≥8 distinct `concept` tags, no tag more than 3×, and all three difficulty tiers.
  Chapter 12 is at 21 with three tags already at the limit of 3.
- **Every behavioural claim is watched failing.** For each measurement this plan
  moves: break the code, watch that exact test fail, restore. A test whose failure
  nobody has observed is not evidence. Three tests shipped on the Task 14 branch
  could not fail; all three were caught by mutation.
- **A domain-claim correction is a sweep, not an edit.** When a claim changes,
  grep every layer for every phrasing of it in the same pass — `README.md`,
  `hint-content.ts`, all 18 quiz chapters, `0001_init.sql` comments, and Go/TS
  comments **including `_test.go` files** — and report the hit table as evidence.
  Task 14.6 took five review rounds for want of this.
- **No test in this repository can catch a web rendering regression.** Any web
  change needs a screenshot, looked at.

## Where the work happens

The worktree `~/Git/cbs-db-per-entity` on branch `spec/db-per-entity` is at
`e3e4f29`, one commit behind the Task 14 merge. Before Task 15a.1:

```bash
cd ~/Git/cbs-db-per-entity
git fetch --all
git status --short          # must be clean
git reset --hard main       # main is 76eca33, the Task 14 merge plus its handoff
git log --oneline -1        # expect 76eca33
```

Every command below runs in that worktree.

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
Homebrew; use `TEST_DATABASE_URL` directly. The Postgres run is **not optional**:
this task adds a table.

---

## File Structure

| file | responsibility in this task |
|---|---|
| `iso20022/camt053.go` | **new.** BankToCustomerStatement: the statement message |
| `iso20022/codes.go` | `BalanceType`, `CreditDebitCode`, `EntryStatus` code sets |
| `iso20022/doc.go` | the message list, and the reversal of "the camt family is deliberately absent" |
| `iso20022/party.go` | `GenericAccountIdentification`'s "nothing produces one today" is no longer true |
| `iso20022/testdata/camt053.xml` | golden file |
| `iso20022/xmllint_test.go` | the schema map gains an entry it will keep skipping |
| `payment/translate.go` | `StatementMessage`, `ReadStatement`, `AccountStatement` |
| `payment/types.go` | `SettlementStatement`, `SettlementAdvice`, `AdviceStatus` |
| `payment/store.go` | three `Tx` methods for the advice row, and the contract notes |
| `payment/system.go` | `SettleCycleTx` narrows; `PostSettlementAdviceTx`; `PostCreditorLegTx`; the reserve check; the unclaimed-balances account |
| `payment/errors.go` | `ErrNotThisBanksPayment`, `ErrStatementNotForThisBank` |
| `store/mem/tx_payment.go`, `store/pg/tx_payment.go`, `store/pg/schema/0001_init.sql`, `store/storetest/payment.go` | the advice row, in both stores and the conformance suite |
| `mesh/ops.go` | `bankOps` gains two methods; `settlementOps`' return widens |
| `mesh/centralbank.go` | sends the statements after settling |
| `mesh/bank.go` | a camt.053 arm; the ACSC arm posts the creditor leg; the actor learns its own participant id |
| `mesh/csm.go` | the ACSC fan-out reaches the creditor's bank too |
| `mesh/mesh.go` | `bank` carries its `ParticipantID` |
| `seed/seed.go` | `builder.settle`, the third composite: the seed plays all three institutions because none exists yet |
| `mesh/books_test.go` | **two** measurements move |
| `README.md`, `hint-content.ts`, quiz chapters 9/11/15/16, schema comments | the domain facts |

---

# Task 15a — camt.053, the message

## Task 15a.1: `iso20022` learns to carry a statement

**Files:**
- Create: `iso20022/camt053.go`, `iso20022/camt053_test.go`,
  `iso20022/testdata/camt053.xml`
- Modify: `iso20022/codes.go`, `iso20022/doc.go:88`, `iso20022/party.go:207-215`,
  `iso20022/xmllint_test.go:90`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `iso20022.Camt053`, `BankToCustomerStatement`, `AccountStatement`
  (the XML struct — note the name collision resolved in 15a.2),
  `StatementEntry`, `CashBalance`, `BalanceType`, `CreditDebitCode`,
  `EntryStatus`. Task 15a.2 builds and reads these; 15b.2 and 15b.3 send and
  receive them.

- [ ] **Step 1: Write the failing golden round-trip test**

Create `iso20022/camt053_test.go`:

```go
package iso20022

import "testing"

// TestCamt053GoldenRoundTrip is the statement's round trip through the codec,
// against a committed sample.
//
// It is the FIRST camt message in this package, and the first message here whose
// account is addressed by the Othr arm of AccountIdentification4Choice rather
// than by IBAN — a reserve account at a central bank has no IBAN, because it is
// not a payment address. See GenericAccountIdentification, whose doc said
// nothing in this system produced one until now.
func TestCamt053GoldenRoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "camt053.xml")

	doc, ok := env.Document.(*Camt053)
	if !ok {
		t.Fatalf("document is %T, want *Camt053", env.Document)
	}
	if got, want := len(doc.BkToCstmrStmt.Stmt), 1; got != want {
		t.Fatalf("statements = %d, want %d", got, want)
	}
	stmt := doc.BkToCstmrStmt.Stmt[0]
	if got := stmt.Acct.Id.Othr; got == nil || got.Id != "acc_cb_reserve_bank_2" {
		t.Errorf("statement account = %v, want the Othr arm carrying acc_cb_reserve_bank_2", got)
	}
	if got, want := len(stmt.Ntry), 1; got != want {
		t.Fatalf("entries = %d, want %d", got, want)
	}
	if got, want := stmt.Ntry[0].CdtDbtInd, CreditDebitCredit; got != want {
		t.Errorf("entry indicator = %q, want %q", got, want)
	}
	if got, want := stmt.Bal[0].Tp.CdOrPrtry.Cd, BalanceTypeClosingBooked; got != want {
		t.Errorf("balance type = %q, want %q", got, want)
	}
}

// TestCamt053RefusesAStatementWithNoBalance pins the element that makes this
// message worth choosing over a camt.054.
//
// A notification carries entries and no balance, so it can drive a posting and
// can never detect a wrong one. A statement carries both. A Bal-less statement
// would be a camt.054 wearing a camt.053's name, and this package refuses to
// build or to parse one.
func TestCamt053RefusesAStatementWithNoBalance(t *testing.T) {
	doc := &Camt053{BkToCstmrStmt: BankToCustomerStatement{
		GrpHdr: StatementGroupHeader{MsgId: "msg_1", CreDtTm: ISODateTime{Time: testTime}},
		Stmt: []AccountStatement{{
			Id:      "set_1",
			CreDtTm: ISODateTime{Time: testTime},
			Acct:    CashAccount{Id: AccountIdentification4Choice{Othr: &GenericAccountIdentification{Id: "acc_1"}}},
			Ntry: []StatementEntry{{
				Amt:       ActiveCurrencyAndAmount{Ccy: "EUR", Value: "10.00"},
				CdtDbtInd: CreditDebitCredit,
				Sts:       EntryStatusChoice{Cd: EntryStatusBooked},
				BookgDt:   DateAndDateTime{Dt: &ISODate{Time: testTime}},
				ValDt:     DateAndDateTime{Dt: &ISODate{Time: testTime}},
			}},
		}},
	}}
	if err := doc.validate(); err == nil {
		t.Fatal("a statement with no balance was accepted; camt.053 is the balance-anchored message")
	}
}
```

`testTime` is the package's existing fixture instant — grep for it in
`pacs009_test.go` and use whatever that file uses; do not introduce a second one.

- [ ] **Step 2: Run it and watch it fail**

```bash
cd ~/Git/cbs-db-per-entity && go test ./iso20022/ -run TestCamt053 2>&1 | head -20
```

Expected: FAIL to compile — `undefined: Camt053`.

- [ ] **Step 3: Add the code sets**

In `iso20022/codes.go`, at the end of the file:

```go
// CreditDebitCode says which way an amount runs, from the point of view of the
// account the statement is about.
//
// It is a separate element rather than a sign on the amount because
// ActiveCurrencyAndAmount cannot be negative — NewAmount refuses one — and that
// is the standard's shape throughout: money is a magnitude and the direction is
// said in words. It is the same separation ledger.Entry makes with Direction,
// arrived at independently.
type CreditDebitCode string

const (
	// CreditDebitCredit is money INTO the account the statement is about.
	CreditDebitCredit CreditDebitCode = "CRDT"
	// CreditDebitDebit is money OUT of it.
	CreditDebitDebit CreditDebitCode = "DBIT"
)

// BalanceType names which balance a Bal element carries.
//
// Only the closing booked balance is produced here, and that is the one this
// system needs: it is the figure a member reconciles its own reserve mirror
// against. OPBD (opening booked) is the obvious companion and is deliberately
// absent — it is derivable from CLBD and the entries, so asserting it would be a
// second source of truth for one fact, which is the shape of error a statement
// exists to detect rather than to introduce.
type BalanceType string

const (
	// BalanceTypeClosingBooked is CLBD: the balance after every entry in this
	// statement has been booked.
	BalanceTypeClosingBooked BalanceType = "CLBD"
)

// EntryStatus is whether an entry is booked or merely expected.
//
// Only BOOK is produced. A settlement statement is sent AFTER the netting
// transaction has committed at the central bank — settlement is final at that
// moment, which is the whole premise of this conversation — so there is no
// pending entry for this system to report. PDNG would be a claim about a
// movement that might not happen, and none of those exists here.
type EntryStatus string

const (
	// EntryStatusBooked is BOOK: the entry is on the account.
	EntryStatusBooked EntryStatus = "BOOK"
)
```

- [ ] **Step 4: Add the message**

Create `iso20022/camt053.go`:

```go
package iso20022

import (
	"encoding/xml"
	"fmt"
)

const camt053Namespace = "urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"

func init() {
	registerDocument("camt.053.001.08", camt053Namespace, func() Document { return &Camt053{} })
}

// Camt053 is BankToCustomerStatement: an account servicer telling an account
// holder what happened on an account the holder does not keep.
//
// # Why the camt family stopped being deliberately absent
//
// The package doc recorded the whole family as out of scope, and the reason it
// gave was true when it was written: no institution in this system needed to be
// TOLD about a movement on an account it does not hold. Every actor could read
// every book. Sub-project 8 creates the first institution that cannot — a member
// bank whose reserve at the central bank moves in the CENTRAL BANK's book, which
// after the split it may not read — so the movement has to arrive as a message
// or not at all. The reversal is recorded in doc.go and in the sub-project's
// design; it is a change of circumstance, not a change of mind.
//
// # Why a statement and not a notification
//
// A camt.054 carries entries and no balance. It can drive a posting and it can
// never detect a wrong one. A camt.053 carries Ntry — what to book — and
// Bal/CLBD — whether you booked it right, which is the check a member's reserve
// mirror needs and the only in-system detector of a mis-booked position. Nostro
// reconciliation in the field is balance-anchored for exactly this reason. One
// message family covers both jobs, so this package carries one.
//
// # "Customer" here is a bank
//
// The message definition's Cstmr is the account HOLDER, whoever that is. On this
// wire the servicer is the central bank and the holder is a member bank, which
// is the same relationship a retail bank has with a depositor one layer down.
// Nothing in the message says the holder is a person, and the type does not
// pretend otherwise.
//
// Deliberately omitted, and legal in the standard: MsgPgntn (this system sends
// one page), Stmt/ElctrncSeqNb and LglSeqNb (there is no statement series yet —
// see Task 19), Stmt/FrToDt (a settlement statement covers one cycle, named on
// the entry, not a date range), TxsSummry, Ntry/NtryDtls and every charge,
// interest and related-party element. Each is absent rather than empty.
type Camt053 struct {
	XMLName       xml.Name                 `xml:"urn:iso:std:iso:20022:tech:xsd:camt.053.001.08 Document"`
	BkToCstmrStmt BankToCustomerStatement  `xml:"BkToCstmrStmt"`
}

func (Camt053) MessageDefinitionIdentifier() string { return "camt.053.001.08" }
func (Camt053) namespace() string                   { return camt053Namespace }

func (d Camt053) validate() error { return d.BkToCstmrStmt.validate() }

// BankToCustomerStatement is a group header and one or more statements.
type BankToCustomerStatement struct {
	GrpHdr StatementGroupHeader `xml:"GrpHdr"`
	Stmt   []AccountStatement   `xml:"Stmt"`
}

func (m BankToCustomerStatement) validate() error {
	if err := m.GrpHdr.validate(); err != nil {
		return err
	}
	if len(m.Stmt) == 0 {
		return fmt.Errorf("%w: Stmt", ErrMissingElement)
	}
	for i := range m.Stmt {
		if err := m.Stmt[i].validate(); err != nil {
			return fmt.Errorf("Stmt[%d]: %w", i, err)
		}
	}
	return nil
}

// StatementGroupHeader is a message identifier and a creation instant.
//
// It is NOT CreditTransferGroupHeader, which pacs.008 and pacs.009 share: that
// type carries NbOfTxs, SttlmInf and IntrBkSttlmDt, none of which a statement
// has. A shared struct here would emit three elements the schema does not allow
// in a camt.053.
type StatementGroupHeader struct {
	MsgId   string      `xml:"MsgId"`
	CreDtTm ISODateTime `xml:"CreDtTm"`
}

func (h StatementGroupHeader) validate() error {
	if h.MsgId == "" {
		return fmt.Errorf("%w: GrpHdr/MsgId", ErrMissingElement)
	}
	if h.CreDtTm.IsZero() {
		return fmt.Errorf("%w: GrpHdr/CreDtTm", ErrMissingElement)
	}
	return nil
}

// AccountStatement is one account's statement: which account, what its balance
// is, and what moved.
//
// The field order is the schema's sequence order and must not be changed.
//
// Bal is validated as NON-EMPTY, which the schema also requires (1..n) and which
// is the element this message is chosen for. See the type doc on Camt053.
type AccountStatement struct {
	Id      string           `xml:"Id"`
	CreDtTm ISODateTime      `xml:"CreDtTm"`
	Acct    CashAccount      `xml:"Acct"`
	Bal     []CashBalance    `xml:"Bal"`
	Ntry    []StatementEntry `xml:"Ntry"`
}

func (s AccountStatement) validate() error {
	if s.Id == "" {
		return fmt.Errorf("%w: Stmt/Id", ErrMissingElement)
	}
	if s.CreDtTm.IsZero() {
		return fmt.Errorf("%w: Stmt/CreDtTm", ErrMissingElement)
	}
	if err := s.Acct.validate(); err != nil {
		return fmt.Errorf("Acct: %w", err)
	}
	if len(s.Bal) == 0 {
		return fmt.Errorf("%w: Stmt/Bal", ErrMissingElement)
	}
	for i := range s.Bal {
		if err := s.Bal[i].validate(); err != nil {
			return fmt.Errorf("Bal[%d]: %w", i, err)
		}
	}
	for i := range s.Ntry {
		if err := s.Ntry[i].validate(); err != nil {
			return fmt.Errorf("Ntry[%d]: %w", i, err)
		}
	}
	return nil
}

// CashBalance is one balance on the account, and which balance it is.
type CashBalance struct {
	Tp        BalanceTypeChoice `xml:"Tp"`
	Amt       ActiveCurrencyAndAmount `xml:"Amt"`
	CdtDbtInd CreditDebitCode   `xml:"CdtDbtInd"`
	Dt        DateAndDateTime   `xml:"Dt"`
}

func (b CashBalance) validate() error {
	if b.Tp.CdOrPrtry.Cd == "" {
		return fmt.Errorf("%w: Bal/Tp/CdOrPrtry/Cd", ErrMissingElement)
	}
	if err := b.Amt.Validate(); err != nil {
		return fmt.Errorf("Bal/Amt: %w", err)
	}
	if b.CdtDbtInd == "" {
		return fmt.Errorf("%w: Bal/CdtDbtInd", ErrMissingElement)
	}
	return b.Dt.validate()
}

// BalanceTypeChoice names which balance this is. The standard offers a code or a
// proprietary identifier; only the code arm is carried, as ServiceLevelChoice
// does, because every balance this system reports is in the external code set.
type BalanceTypeChoice struct {
	CdOrPrtry BalanceTypeCode `xml:"CdOrPrtry"`
}

// BalanceTypeCode is the extra element of nesting the standard puts between Tp
// and the code. It exists so the XML comes out right and for no other reason.
type BalanceTypeCode struct {
	Cd BalanceType `xml:"Cd"`
}

// StatementEntry is one movement on the account.
//
// AcctSvcrRef is the SERVICER's reference for the entry, and here it is the
// CLEARING CYCLE the movement discharged. That is what makes the statement
// actionable: a member bank has no cycles of its own — it never sees a batch —
// so the only way it can tell which cut-off a reserve movement belongs to is for
// the central bank to say. See payment.ReadStatement.
//
// The field order is the schema's sequence order and must not be changed.
type StatementEntry struct {
	Amt         ActiveCurrencyAndAmount `xml:"Amt"`
	CdtDbtInd   CreditDebitCode         `xml:"CdtDbtInd"`
	Sts         EntryStatusChoice       `xml:"Sts"`
	BookgDt     DateAndDateTime         `xml:"BookgDt"`
	ValDt       DateAndDateTime         `xml:"ValDt"`
	AcctSvcrRef string                  `xml:"AcctSvcrRef,omitempty"`
	AddtlNtryInf string                 `xml:"AddtlNtryInf,omitempty"`
}

func (e StatementEntry) validate() error {
	if err := e.Amt.Validate(); err != nil {
		return fmt.Errorf("Ntry/Amt: %w", err)
	}
	if e.CdtDbtInd == "" {
		return fmt.Errorf("%w: Ntry/CdtDbtInd", ErrMissingElement)
	}
	if e.Sts.Cd == "" {
		return fmt.Errorf("%w: Ntry/Sts/Cd", ErrMissingElement)
	}
	if err := e.BookgDt.validate(); err != nil {
		return fmt.Errorf("Ntry/BookgDt: %w", err)
	}
	if err := e.ValDt.validate(); err != nil {
		return fmt.Errorf("Ntry/ValDt: %w", err)
	}
	return nil
}

// EntryStatusChoice is booked or pending. Only the code arm is carried, for
// BalanceTypeChoice's reason.
type EntryStatusChoice struct {
	Cd EntryStatus `xml:"Cd"`
}

// DateAndDateTime is a date OR a date-time, never both and never neither.
//
// This is an xsd:choice, which encoding/xml cannot express, so both arms are
// pointers and validate enforces exactly-one — the same shape
// AccountIdentification4Choice has and for the same reason. Only the date arm is
// produced here: a settlement's booking and value dates are days, and a
// date-time would assert a precision the cut-off does not have.
type DateAndDateTime struct {
	Dt   *ISODate     `xml:"Dt,omitempty"`
	DtTm *ISODateTime `xml:"DtTm,omitempty"`
}

func (d DateAndDateTime) validate() error {
	switch {
	case d.Dt != nil && d.DtTm != nil:
		return fmt.Errorf("%w: DateAndDateTime has both Dt and DtTm", ErrInvalidChoice)
	case d.Dt != nil, d.DtTm != nil:
		return nil
	default:
		return fmt.Errorf("%w: DateAndDateTime has neither Dt nor DtTm", ErrInvalidChoice)
	}
}
```

- [ ] **Step 5: Write the golden file**

Create `iso20022/testdata/camt053.xml`. Read `iso20022/testdata/pacs009.xml`
first and match its envelope shape, indentation and instant exactly; only the
document differs.

```xml
<Envelope>
  <AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">
    <Fr><FIId><FinInstnId><BICFI>CBSEDEFFXXX</BICFI></FinInstnId></FIId></Fr>
    <To><FIId><FinInstnId><BICFI>BRVODEFFXXX</BICFI></FinInstnId></FIId></To>
    <BizMsgIdr>msg_7</BizMsgIdr>
    <MsgDefIdr>camt.053.001.08</MsgDefIdr>
    <CreDt>2024-01-15T10:00:00Z</CreDt>
  </AppHdr>
  <Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08">
    <BkToCstmrStmt>
      <GrpHdr>
        <MsgId>msg_7</MsgId>
        <CreDtTm>2024-01-15T10:00:00Z</CreDtTm>
      </GrpHdr>
      <Stmt>
        <Id>set_1</Id>
        <CreDtTm>2024-01-15T10:00:00Z</CreDtTm>
        <Acct>
          <Id><Othr><Id>acc_cb_reserve_bank_2</Id></Othr></Id>
        </Acct>
        <Bal>
          <Tp><CdOrPrtry><Cd>CLBD</Cd></CdOrPrtry></Tp>
          <Amt Ccy="EUR">3000.00</Amt>
          <CdtDbtInd>CRDT</CdtDbtInd>
          <Dt><Dt>2024-01-15</Dt></Dt>
        </Bal>
        <Ntry>
          <Amt Ccy="EUR">2500.00</Amt>
          <CdtDbtInd>CRDT</CdtDbtInd>
          <Sts><Cd>BOOK</Cd></Sts>
          <BookgDt><Dt>2024-01-15</Dt></BookgDt>
          <ValDt><Dt>2024-01-15</Dt></ValDt>
          <AcctSvcrRef>cyc_1</AcctSvcrRef>
          <AddtlNtryInf>Settlement of clearing cycle cyc_1</AddtlNtryInf>
        </Ntry>
      </Stmt>
    </BkToCstmrStmt>
  </Document>
</Envelope>
```

If `pacs009.xml`'s header BICs or instant differ, use ITS values here — the
golden files are read as a set and a second convention would be noise. Adjust the
test's `testTime` expectations to match.

- [ ] **Step 6: Add the schema-map entry**

In `iso20022/xmllint_test.go:90`, beside `"pacs009.xml": "pacs.009.001.08.xsd"`,
add:

```go
		"camt053.xml": "camt.053.001.08.xsd",
```

**This will SKIP, not pass.** `iso20022/testdata/xsd/` is absent for licensing
reasons and `TestGoldenFilesValidateAgainstTheSchema` skips every subtest. A skip
is not a pass, and the entry is added anyway so the file is validated the day the
schemas arrive rather than silently omitted from the set.

- [ ] **Step 7: Run the tests**

```bash
cd ~/Git/cbs-db-per-entity && go test ./iso20022/ -v 2>&1 | tail -40
```

Expected: PASS. `FuzzUnmarshal` and the codec tests now see a sixth registered
message; if `codec_test.go` asserts a message COUNT anywhere, update it — grep
for `documentRegistry` in `codec_test.go` before assuming it does not.

- [ ] **Step 8: Watch the balance guard fail**

Delete the `if len(s.Bal) == 0` block from `AccountStatement.validate`. Run
`go test ./iso20022/ -run TestCamt053RefusesAStatementWithNoBalance`. Expected:
FAIL. Restore.

- [ ] **Step 9: Reverse the two rulings, in the two places that hold them**

In `iso20022/doc.go`, the `# Messages` list becomes six and the "Deliberately
absent" sentence changes. Replace `//   - pacs.009.001.08 …` block's successor
text with:

```go
//   - camt.053.001.08 BkToCstmrStmt — a statement: an account servicer telling
//     an account holder what happened on an account the holder does not keep.
//     The central bank sends one to each member after a cut-off, for that
//     member's reserve account. It is sub-project 8's, and it is the message
//     that reverses the ruling below.
//
// Deliberately absent: pain.001 and pain.008 (the customer-to-bank layer),
// camt.056 recalls and pacs.007 reversals, message signing, and runtime XSD
// validation. Each is recorded in the design document with the reason.
//
// # A reversed ruling: the camt family
//
// The whole camt family was recorded here as deliberately absent, and the reason
// was true when it was written: no institution in this system needed to be TOLD
// about a movement on an account it does not hold, because every actor could
// read every book. Sub-project 8 creates the first institution that cannot — a
// member bank whose reserve at the central bank moves in the CENTRAL BANK's
// book — so the movement has to arrive as a message or not at all. camt.053 is
// carried; the rest of the family is not, and camt.054 is refused on the
// specific ground that a notification carries no balance and therefore cannot
// detect a wrong posting. See Camt053.
```

In `iso20022/party.go:207-215`, `GenericAccountIdentification`'s doc says
*"Nothing in this system produces one today — SEPA is IBAN-only."* That is now
false. Rewrite it, keeping the finding's history:

```go
// GenericAccountIdentification is the non-IBAN arm of an account
// identification: an identifier plus, optionally, the scheme that issued it.
//
// It existed for a long time because it is the OTHER half of a choice, and a
// choice with one arm is not a choice — nothing in this system produced one, and
// its doc said so. camt.053 is what changed that: a member bank's reserve
// account at the central bank has no IBAN, because it is not a payment address
// and no customer ever quotes it. It is identified by the servicer's own account
// identifier, which is exactly what this arm is for. It is still where a card
// PAN would arrive.
```

- [ ] **Step 10: Full verification and commit**

```bash
cd ~/Git/cbs-db-per-entity
gofmt -l . && go vet ./... && go build ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
git add -A
git commit -m "feat(iso20022): camt.053, the message that carries a balance

An account servicer telling an account holder what happened on an account the
holder does not keep. It reverses this package's own ruling that the camt family
is deliberately absent, and the reversal is a change of circumstance: until
sub-project 8 no institution here needed to be TOLD about a movement, because
every actor could read every book.

camt.053 and not camt.054 because a notification carries entries and no balance,
so it can drive a posting and can never detect a wrong one."
```

---

## Task 15a.2: `payment` builds and reads a statement

**Files:**
- Modify: `payment/translate.go` (a new section after `SettlementMessage`)
- Modify: `payment/types.go` (`SettlementStatement`)
- Test: `payment/message_test.go`

**Interfaces:**
- Consumes: `iso20022.Camt053` and friends from 15a.1.
- Produces:
  - `payment.SettlementStatement{Member ParticipantID; Agent iso20022.BIC;
    Account ledger.AccountID; Asset ledger.AssetCode; CycleID CycleID;
    SettlementID SettlementID; Movement ledger.Amount; ClosingBalance
    ledger.Amount; ValueDate time.Time}` — what the central bank captured inside
    its unit of work.
  - `payment.StatementMessage(st SettlementStatement, mc MessageContext)
    (iso20022.Envelope, error)`.
  - `payment.AdvisedMovement{Account ledger.AccountID; Asset ledger.AssetCode;
    Movement ledger.Amount; ClosingBalance ledger.Amount; CycleID CycleID;
    ValueDate time.Time}` and `payment.ReadStatement(doc *iso20022.Camt053)
    ([]AdvisedMovement, error)`.

  `AdvisedMovement` is deliberately NOT the same type as `SettlementStatement`:
  one is what the sender knows and the other is what the receiver can see, and
  they differ — a receiver learns no `ParticipantID` and no `SettlementID`,
  because those are the sender's identifiers for its own rows. Task 15b.3 reads
  `AdvisedMovement`.

- [ ] **Step 1: Write the failing round-trip test**

Add to `payment/message_test.go`:

```go
// TestStatementMessageRoundTripsThroughTheWire pins that what the central bank
// captured inside its unit of work is what the member bank can act on: the
// movement, the closing balance, the asset and the cycle.
//
// Both directions in one test, because a builder and a reader that agree with
// each other and not with the wire is the failure this catches — the same reason
// the codec's own golden tests run both ways.
func TestStatementMessageRoundTripsThroughTheWire(t *testing.T) {
	st := payment.SettlementStatement{
		Member:         "bank_2",
		Agent:          "BRVODEFFXXX",
		Account:        "acc_cb_reserve_bank_2",
		Asset:          "EUR",
		CycleID:        "cyc_1",
		SettlementID:   "set_1",
		Movement:       250000,
		ClosingBalance: 300000,
		ValueDate:      testNow,
	}
	env, err := payment.StatementMessage(st, payment.MessageContext{
		From: "CBSEDEFFXXX", To: st.Agent, MsgID: "msg_7", Now: testNow,
	})
	if err != nil {
		t.Fatalf("StatementMessage: %v", err)
	}

	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := iso20022.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	moves, err := payment.ReadStatement(back.Document.(*iso20022.Camt053))
	if err != nil {
		t.Fatalf("ReadStatement: %v", err)
	}
	if len(moves) != 1 {
		t.Fatalf("movements = %d, want 1", len(moves))
	}
	want := payment.AdvisedMovement{
		Account:        st.Account,
		Asset:          st.Asset,
		Movement:       st.Movement,
		ClosingBalance: st.ClosingBalance,
		CycleID:        st.CycleID,
		ValueDate:      ledger.DayStart(testNow),
	}
	if moves[0] != want {
		t.Errorf("movement round-tripped as %+v, want %+v", moves[0], want)
	}
}

// TestStatementMessageCarriesTheDirectionInWordsAndTheAmountAsAMagnitude pins
// the one place a sign could be lost.
//
// ActiveCurrencyAndAmount cannot be negative — NewAmount refuses one — so a net
// PAYER's movement travels as a positive amount with CdtDbtInd = DBIT, and the
// sign is reconstructed on the way in. A builder that emitted the magnitude and
// dropped the indicator would produce a statement that reads as a credit, which
// would make a bank post its mirror leg BACKWARDS: reserve up when it went down.
func TestStatementMessageCarriesTheDirectionInWordsAndTheAmountAsAMagnitude(t *testing.T) {
	st := payment.SettlementStatement{
		Member: "bank_2", Agent: "BRVODEFFXXX", Account: "acc_1", Asset: "EUR",
		CycleID: "cyc_1", SettlementID: "set_1",
		Movement: -250000, ClosingBalance: -1, ValueDate: testNow,
	}
	env, err := payment.StatementMessage(st, payment.MessageContext{
		From: "CBSEDEFFXXX", To: st.Agent, MsgID: "msg_7", Now: testNow,
	})
	if err != nil {
		t.Fatalf("StatementMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Camt053)
	entry := doc.BkToCstmrStmt.Stmt[0].Ntry[0]
	if got, want := entry.CdtDbtInd, iso20022.CreditDebitDebit; got != want {
		t.Errorf("entry indicator = %q, want %q", got, want)
	}
	if got := entry.Amt.Value; got != "2500.00" {
		t.Errorf("entry amount = %q, want the magnitude 2500.00", got)
	}
	if got, want := doc.BkToCstmrStmt.Stmt[0].Bal[0].CdtDbtInd, iso20022.CreditDebitDebit; got != want {
		t.Errorf("balance indicator = %q, want %q — a negative reserve is a DEBIT balance", got, want)
	}

	moves, err := payment.ReadStatement(doc)
	if err != nil {
		t.Fatalf("ReadStatement: %v", err)
	}
	if moves[0].Movement != -250000 {
		t.Errorf("movement read back as %d, want -250000", moves[0].Movement)
	}
	if moves[0].ClosingBalance != -1 {
		t.Errorf("closing balance read back as %d, want -1", moves[0].ClosingBalance)
	}
}
```

`testNow` is the file's existing fixture instant — read the top of
`payment/message_test.go` and use whatever it already has rather than adding one.

- [ ] **Step 2: Run and watch it fail**

```bash
cd ~/Git/cbs-db-per-entity && go test ./payment/ -run TestStatementMessage 2>&1 | head -20
```

Expected: FAIL to compile — `undefined: payment.SettlementStatement`.

- [ ] **Step 3: Add `SettlementStatement`**

In `payment/types.go`, immediately after the `Settlement` struct:

```go
// SettlementStatement is one member's share of a settlement, as the CENTRAL BANK
// saw it at the moment it posted: the movement on that member's reserve account
// and the balance the account was left at.
//
// It is captured INSIDE SettleCycleTx's unit of work and returned, rather than
// re-read afterwards, because the closing balance is a claim about a moment. A
// balance read after the commit is a different number the instant anything else
// settles, and a statement asserting the wrong one is worse than no statement:
// the whole point of carrying Bal/CLBD is that a member can check its own
// posting against it.
//
// Movement is SIGNED — positive means the member's reserve went up — and the
// message it becomes is not: ActiveCurrencyAndAmount cannot be negative, so the
// direction travels as CdtDbtInd. See StatementMessage.
type SettlementStatement struct {
	Member       ParticipantID
	Agent        iso20022.BIC
	Account      ledger.AccountID
	Asset        ledger.AssetCode
	CycleID      CycleID
	SettlementID SettlementID

	Movement       ledger.Amount
	ClosingBalance ledger.Amount
	ValueDate      time.Time
}
```

- [ ] **Step 4: Add the builder and the reader**

In `payment/translate.go`, at the end of the file:

```go
// ---------------------------------------------------------------------------
// The statement
// ---------------------------------------------------------------------------

// StatementMessage renders one member's share of a settlement as the camt.053
// that tells it.
//
// One member per message, because each goes to a different bank and a statement
// is addressed to the holder of the account it is about. The standard permits
// several Stmt elements in one document and this system never builds one: a
// second statement in this message would be about an account the recipient does
// not hold.
//
// # The sign leaves the amount and becomes a word
//
// ActiveCurrencyAndAmount cannot be negative — NewAmount refuses it — so the
// magnitude goes in Amt and the direction in CdtDbtInd, on BOTH the entry and
// the balance. That is the standard's shape everywhere and it is the same
// separation ledger.Entry makes with Direction. Reconstructing the sign is
// ReadStatement's job, and losing it there would make a member post its mirror
// leg backwards.
//
// # The cycle rides on AcctSvcrRef
//
// A member bank has no cycles — it never sees a batch — so the only way it can
// tell which cut-off a reserve movement discharged is for the central bank to
// say. AcctSvcrRef is the servicer's own reference for the entry, which is
// exactly what a cycle id is from the central bank's side.
func StatementMessage(st SettlementStatement, mc MessageContext) (iso20022.Envelope, error) {
	if st.Account == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Stmt/Acct/Id/Othr/Id", iso20022.ErrMissingElement)
	}
	if st.CycleID == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Ntry/AcctSvcrRef", iso20022.ErrMissingElement)
	}
	entryAmt, entryInd, err := signedAmountOf(st.Movement, st.Asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	balAmt, balInd, err := signedAmountOf(st.ClosingBalance, st.Asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	day := iso20022.ISODate{Time: ledger.DayStart(st.ValueDate)}

	doc := &iso20022.Camt053{BkToCstmrStmt: iso20022.BankToCustomerStatement{
		GrpHdr: iso20022.StatementGroupHeader{
			MsgId:   mc.MsgID,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
		},
		Stmt: []iso20022.AccountStatement{{
			Id:      string(st.SettlementID),
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
			Acct: iso20022.CashAccount{Id: iso20022.AccountIdentification4Choice{
				Othr: &iso20022.GenericAccountIdentification{Id: string(st.Account)},
			}},
			Bal: []iso20022.CashBalance{{
				Tp:        iso20022.BalanceTypeChoice{CdOrPrtry: iso20022.BalanceTypeCode{Cd: iso20022.BalanceTypeClosingBooked}},
				Amt:       balAmt,
				CdtDbtInd: balInd,
				Dt:        iso20022.DateAndDateTime{Dt: &day},
			}},
			Ntry: []iso20022.StatementEntry{{
				Amt:          entryAmt,
				CdtDbtInd:    entryInd,
				Sts:          iso20022.EntryStatusChoice{Cd: iso20022.EntryStatusBooked},
				BookgDt:      iso20022.DateAndDateTime{Dt: &day},
				ValDt:        iso20022.DateAndDateTime{Dt: &day},
				AcctSvcrRef:  string(st.CycleID),
				AddtlNtryInf: "Settlement of clearing cycle " + string(st.CycleID),
			}},
		}},
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// signedAmountOf splits a signed ledger amount into the magnitude the standard
// carries and the word that says which way it runs.
//
// Zero is a CREDIT of nothing. It is reachable on a BALANCE — a member whose
// reserve is exactly empty — and never on an entry, because a position of zero
// produces no leg and therefore no statement. Choosing CRDT for it is arbitrary
// and stated rather than left to be inferred; DBIT would read the same on the
// wire and neither means anything.
func signedAmountOf(amt ledger.Amount, asset ledger.AssetCode) (iso20022.ActiveCurrencyAndAmount, iso20022.CreditDebitCode, error) {
	ind := iso20022.CreditDebitCredit
	magnitude := amt
	if amt < 0 {
		ind, magnitude = iso20022.CreditDebitDebit, -amt
	}
	out, err := amountOf(magnitude, asset)
	if err != nil {
		return iso20022.ActiveCurrencyAndAmount{}, "", err
	}
	return out, ind, nil
}

// AdvisedMovement is what a member bank can see in a statement about its own
// reserve account: the movement, the balance it was left at, and the cut-off
// that caused it.
//
// It is a DIFFERENT type from SettlementStatement, deliberately. That one is
// what the sender knew; this is what the receiver can learn, and they are not the
// same set of facts — a member sees no ParticipantID and no SettlementID,
// because those are the central bank's identifiers for its own rows and nothing
// in the message carries them. Collapsing the two types would put fields on the
// receiving side that are always empty and invite a caller to trust them.
type AdvisedMovement struct {
	Account        ledger.AccountID
	Asset          ledger.AssetCode
	Movement       ledger.Amount
	ClosingBalance ledger.Amount
	CycleID        CycleID
	ValueDate      time.Time
}

// ReadStatement reads a received camt.053 as the movements it advises.
//
// One entry per statement is REQUIRED and more is refused whole. This system's
// central bank posts exactly one netting movement per member per cycle, so a
// statement carrying two is one this reader has no rule for — and posting the
// first while dropping the second would move a bank's reserve mirror by the wrong
// amount with nothing anywhere recording it. It is onlyTransaction's argument,
// made about a different message.
//
// A statement with no CLBD balance is refused for the reason camt.053 was chosen
// over camt.054: without it there is nothing to check a posting against, and a
// message that cannot be checked is a notification wearing a statement's name.
//
// The scale comes from ledger.LookupAsset on the entry's OWN currency, never
// from a constant — the same rule ReadSettlement follows one message over.
func ReadStatement(doc *iso20022.Camt053) ([]AdvisedMovement, error) {
	stmts := doc.BkToCstmrStmt.Stmt
	out := make([]AdvisedMovement, 0, len(stmts))
	for i, s := range stmts {
		if len(s.Ntry) != 1 {
			return nil, fmt.Errorf("payment: Stmt[%d] carries %d entries; a settlement statement advises one movement", i, len(s.Ntry))
		}
		entry := s.Ntry[0]
		movement, asset, err := signedAmountIn(entry.Amt, entry.CdtDbtInd)
		if err != nil {
			return nil, fmt.Errorf("Stmt[%d]/Ntry/Amt: %w", i, err)
		}
		closing, ok, err := closingBalanceIn(s.Bal)
		if err != nil {
			return nil, fmt.Errorf("Stmt[%d]/Bal: %w", i, err)
		}
		if !ok {
			return nil, fmt.Errorf("payment: Stmt[%d] carries no CLBD balance; a statement with nothing to check against is a notification", i)
		}
		acct := s.Acct.Id
		if acct.Othr == nil {
			return nil, fmt.Errorf("payment: Stmt[%d]/Acct is not identified by Othr; a reserve account has no IBAN", i)
		}
		day := time.Time{}
		if entry.ValDt.Dt != nil {
			day = entry.ValDt.Dt.Time
		}
		out = append(out, AdvisedMovement{
			Account:        ledger.AccountID(acct.Othr.Id),
			Asset:          asset,
			Movement:       movement,
			ClosingBalance: closing,
			CycleID:        CycleID(entry.AcctSvcrRef),
			ValueDate:      day,
		})
	}
	return out, nil
}

// signedAmountIn puts the sign back on: the magnitude the standard carried and
// the word beside it become one signed ledger amount.
//
// An indicator that is neither CRDT nor DBIT is REFUSED rather than defaulted.
// Defaulting to credit would turn an unreadable direction into a reserve
// increase, which is the most expensive way to be wrong about a settlement.
func signedAmountIn(amt iso20022.ActiveCurrencyAndAmount, ind iso20022.CreditDebitCode) (ledger.Amount, ledger.AssetCode, error) {
	value, asset, err := amountIn(amt)
	if err != nil {
		return 0, "", err
	}
	switch ind {
	case iso20022.CreditDebitCredit:
		return value, asset, nil
	case iso20022.CreditDebitDebit:
		return -value, asset, nil
	default:
		return 0, "", fmt.Errorf("payment: CdtDbtInd is %q, which says neither credit nor debit", ind)
	}
}

// closingBalanceIn finds the CLBD balance among however many a statement
// carries, and reports whether there was one.
//
// It searches rather than taking Bal[0] because the standard permits several and
// their order is not fixed; a reader that took the first would eventually read
// an opening balance as a closing one, and the two differ by exactly the entries
// this message exists to advise.
func closingBalanceIn(bals []iso20022.CashBalance) (ledger.Amount, bool, error) {
	for _, b := range bals {
		if b.Tp.CdOrPrtry.Cd != iso20022.BalanceTypeClosingBooked {
			continue
		}
		value, _, err := signedAmountIn(b.Amt, b.CdtDbtInd)
		if err != nil {
			return 0, false, err
		}
		return value, true, nil
	}
	return 0, false, nil
}
```

Confirm `payment/translate.go` already imports `time` and `ledger` — it does;
`settlementDateOf` uses both.

- [ ] **Step 5: Run the tests**

```bash
cd ~/Git/cbs-db-per-entity && go test ./payment/ -run TestStatementMessage -v
```

Expected: PASS, both.

- [ ] **Step 6: Watch the sign fail**

In `signedAmountIn`, make the `CreditDebitDebit` arm return `value` instead of
`-value`. Run
`go test ./payment/ -run TestStatementMessageCarriesTheDirection`. Expected: FAIL
on `movement read back as 250000, want -250000`. Restore.

- [ ] **Step 7: Full verification and commit**

```bash
cd ~/Git/cbs-db-per-entity
gofmt -l . && go vet ./... && go build ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
git add -A
git commit -m "feat(payment): build and read a settlement statement

SettlementStatement is what the central bank captured inside its unit of work —
the movement and the balance it left the account at, which is a claim about a
moment and cannot be re-read afterwards. AdvisedMovement is what the member can
learn from the message, and it is deliberately a different type: a receiver sees
no participant id and no settlement id, because those are the sender's names for
its own rows.

The sign leaves the amount and becomes a word, because the standard's money type
cannot be negative."
```

---

# Task 15b — settlement becomes a conversation

## Task 15b.1: The advice row, the unclaimed-balances account, and the two postings as their own operations

**Files:**
- Modify: `payment/types.go` (`SettlementAdvice`, `AdviceStatus`), `payment/store.go`,
  `payment/system.go`, `payment/errors.go`, `payment/participant.go`
- Modify: `store/mem/tx_payment.go`, `store/pg/tx_payment.go`,
  `store/pg/schema/0001_init.sql`
- Test: `store/storetest/payment.go`, `payment/system_test.go`

**Interfaces:**
- Consumes: `payment.SettlementStatement` and `payment.AdvisedMovement` from 15a.2.
- Produces:
  - `payment.SettlementAdvice{Book ledger.BookID; CycleID CycleID; Asset
    ledger.AssetCode; Movement ledger.Amount; ClosingBalance ledger.Amount;
    Status AdviceStatus; MirrorTx ledger.TransactionID; AdvisedAt, PostedAt
    time.Time}`; `AdviceAdvised` / `AdvicePosted`.
  - `Tx.PutSettlementAdvice(ctx, ledger.BookID, SettlementAdvice) error`,
    `Tx.GetSettlementAdvice(ctx, ledger.BookID, CycleID, ledger.AssetCode)
    (SettlementAdvice, error)`,
    `Tx.ListSettlementAdvices(ctx, ledger.BookID) ([]SettlementAdvice, error)`.
  - `ErrSettlementAdviceNotFound`, `ErrNotThisBanksPayment`,
    `ErrStatementNotForThisBank`.
  - `ParticipantAccounts.Unclaimed ledger.AccountID`.
  - `(*Network).PostSettlementAdviceTx(ctx, tx, by ParticipantID, m AdvisedMovement)
    (SettlementAdvice, error)`
  - `(*Network).PostCreditorLegTx(ctx, tx, by ParticipantID, id PaymentID) (Payment, error)`

  **Nothing in the mesh calls the last two yet.** 15b.2 and 15b.3 wire them.
  They are extracted from `SettleCycleTx`, which calls them in this sub-task, so
  every existing measurement stays exactly where it is and the extraction is
  provably behaviour-preserving.

- [ ] **Step 1: Write the failing conformance test for the advice row**

In `store/storetest/payment.go`, following the registration style of the existing
subtests (find `PutIsAnUpsertAndDeepCopies` and add alongside it):

```go
// SettlementAdviceIsScopedToTheBankThatWasAdvised pins that an advice belongs to
// ONE bank's book and that two banks advised of the same cycle do not collide.
//
// The book is part of the key, not a column on it. A member bank's record of
// what it was told about a cut-off is its own — under sub-project 8 it lives in
// that bank's store and nowhere else — and a key that omitted the book would
// make the second bank's advice overwrite the first's here and be unmigratable
// there.
func settlementAdviceIsScopedToTheBankThatWasAdvised(t *testing.T, st payment.Store) {
	ctx := context.Background()
	one := payment.SettlementAdvice{
		Book: "bank_2", CycleID: "cyc_1", Asset: "EUR",
		Movement: -250000, ClosingBalance: 750000,
		Status: payment.AdviceAdvised, AdvisedAt: testTime,
	}
	two := payment.SettlementAdvice{
		Book: "bank_3", CycleID: "cyc_1", Asset: "EUR",
		Movement: 250000, ClosingBalance: 250000,
		Status: payment.AdvicePosted, MirrorTx: "txn_9",
		AdvisedAt: testTime, PostedAt: testTime,
	}
	if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		if err := tx.PutSettlementAdvice(ctx, one.Book, one); err != nil {
			return err
		}
		return tx.PutSettlementAdvice(ctx, two.Book, two)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice: %v", err)
	}

	var gotOne, gotTwo payment.SettlementAdvice
	var listed []payment.SettlementAdvice
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		var err error
		if gotOne, err = tx.GetSettlementAdvice(ctx, "bank_2", "cyc_1", "EUR"); err != nil {
			return err
		}
		if gotTwo, err = tx.GetSettlementAdvice(ctx, "bank_3", "cyc_1", "EUR"); err != nil {
			return err
		}
		listed, err = tx.ListSettlementAdvices(ctx, "bank_2")
		return err
	}); err != nil {
		t.Fatalf("reading advices: %v", err)
	}
	if gotOne != one {
		t.Errorf("bank_2's advice round-tripped as %+v, want %+v", gotOne, one)
	}
	if gotTwo != two {
		t.Errorf("bank_3's advice round-tripped as %+v, want %+v", gotTwo, two)
	}
	if len(listed) != 1 {
		t.Errorf("bank_2 lists %d advices, want 1 — the list is scoped to one book", len(listed))
	}

	// A cycle this bank was never advised of is a sentinel, not a zero value: a
	// bank that read a zero advice would post a mirror leg of nothing and mark
	// a cut-off it never heard about as settled.
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		_, err := tx.GetSettlementAdvice(ctx, "bank_2", "cyc_nope", "EUR")
		if !errors.Is(err, payment.ErrSettlementAdviceNotFound) {
			t.Errorf("got %v, want ErrSettlementAdviceNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}
}
```

Use the file's own fixture instant instead of `testTime` if it names it something
else; read the neighbouring subtests first.

- [ ] **Step 2: Run it and watch it fail to compile**

```bash
cd ~/Git/cbs-db-per-entity && go test ./store/... 2>&1 | head -20
```

Expected: FAIL — `undefined: payment.SettlementAdvice`.

- [ ] **Step 3: Add the type and the sentinel**

In `payment/types.go`, after `Settlement`:

```go
// AdviceStatus is how far a member bank has got with a settlement it was told
// about.
type AdviceStatus int

const (
	// AdviceAdvised is told and not yet booked. It is the UNRECONCILED
	// POSITION: settlement is final at the central bank and this bank has not
	// caught up. A row stuck here is the one detector this system has for a
	// local posting that failed, and it is why the row exists at all.
	AdviceAdvised AdviceStatus = iota
	// AdvicePosted is booked: the mirror leg is in this bank's own ledger.
	AdvicePosted
)

// SettlementAdvice is a member bank's own record of a cut-off it was told about:
// what its reserve moved by, what the central bank says it was left at, and
// whether this bank has booked it yet.
//
// # It belongs to the BANK and not to the network
//
// Book is part of its identity, which is the whole point. A cycle is the
// clearing house's and a settlement is the central bank's; this is the member's,
// and under sub-project 8 it lives in that member's own store. Two banks advised
// of the same cut-off hold two independent rows with independent statuses, which
// is what makes "one bank fell behind" expressible at all.
//
// # ClosingBalance is stored and nothing checks it yet
//
// It is what the central bank says this bank's reserve stands at, and it is the
// only figure in this system that a bank can check its own books against without
// reading another institution's store. Task 19 is where that check happens. It is
// stored now because it arrives now, and a statement's balance discarded on
// receipt is a balance nobody can ever go back for.
type SettlementAdvice struct {
	Book    ledger.BookID
	CycleID CycleID
	Asset   ledger.AssetCode

	// Movement is SIGNED: positive means this bank's reserve went up.
	Movement       ledger.Amount
	ClosingBalance ledger.Amount

	Status   AdviceStatus
	MirrorTx ledger.TransactionID

	AdvisedAt time.Time
	PostedAt  time.Time
}
```

In `payment/errors.go`, alongside the other sentinels:

```go
	// ErrSettlementAdviceNotFound is a cut-off this bank was never told about.
	ErrSettlementAdviceNotFound = errors.New("settlement advice not found")

	// ErrNotThisBanksPayment is a bank asked to post a leg for a payment whose
	// party is somebody else's customer.
	//
	// It is the direction rule said about a settlement advice. On a push the
	// clearing house tells BOTH banks a payment settled — the payer's bank
	// because it has been waiting for the answer to its instruction, the payee's
	// bank because it has a leg to post — and only one of them may post. A
	// system that let either post would credit the payee twice or credit them in
	// the wrong bank's book.
	ErrNotThisBanksPayment = errors.New("payment: this bank is not the party whose leg this is")

	// ErrStatementNotForThisBank is a statement about an account this bank does
	// not hold.
	//
	// A member checks the account the statement names against its OWN reserve
	// account before booking anything from it. A bank that booked whatever
	// arrived would move its reserve mirror on another member's position — and
	// under isolation a misrouted statement is exactly the failure that has no
	// second reader to catch it.
	ErrStatementNotForThisBank = errors.New("payment: this statement is about an account this bank does not hold")
```

- [ ] **Step 4: Add the three `Tx` methods**

In `payment/store.go`, in the `Tx` interface after the settlement block:

```go
	// The advice rows are BOOK-SCOPED, unlike every other method in this block.
	// Participants, payments, mandates, cycles and settlements belong to no
	// single bank and live under ledger.NetworkBook; an advice is one member
	// bank's record of what it was told, so it is keyed by that bank's book —
	// which is also what makes the recorder in mesh/books_test.go see a bank
	// reaching its own book when it books a settlement.
	PutSettlementAdvice(ctx context.Context, book ledger.BookID, a SettlementAdvice) error
	GetSettlementAdvice(ctx context.Context, book ledger.BookID, cycle CycleID, asset ledger.AssetCode) (SettlementAdvice, error)
	ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]SettlementAdvice, error)
```

Add to the contract notes at the bottom of the same file:

```go
//   - GetSettlementAdvice -> ErrSettlementAdviceNotFound. The key is
//     (book, cycle, asset), all three: two banks advised of one cut-off hold two
//     rows, and a bank operating in two assets settles each separately.
//     ListSettlementAdvices is scoped to ONE book and ordered by AdvisedAt then
//     seq, like every other listing here.
//     (SettlementAdviceIsScopedToTheBankThatWasAdvised.)
```

- [ ] **Step 5: Implement in `store/mem`**

In `store/mem/tx_payment.go`, follow the file's existing pattern for a
book-scoped map (read `tx_deposit.go`'s account map for the shape). Key the map
on a comparable struct:

```go
type adviceKey struct {
	book  ledger.BookID
	cycle payment.CycleID
	asset ledger.AssetCode
}
```

`SettlementAdvice` is all scalars, so the deep-copy contract is satisfied by
struct assignment — **verify** by reading the neighbouring `Put` rather than
assuming, exactly as Task 14.1 Step 4 required.

- [ ] **Step 6: Implement in `store/pg`**

In `store/pg/schema/0001_init.sql`, after `CREATE TABLE settlement_positions`:

```sql
-- settlement_advices is a MEMBER BANK's record of a cut-off it was told about,
-- and it is the first payment-layer table keyed by book.
--
-- Every other table in this section — participants, payments, mandates, cycles,
-- settlements — is network-scoped: those rows belong to no single bank, which is
-- why they carry no book_id. This one does, and the difference is the whole of
-- sub-project 8. A cycle is the clearing house's; a settlement is the central
-- bank's; this is the member's, and when the stores split it moves into that
-- member's own database and the other two do not follow it.
--
-- Two banks advised of one cut-off hold two rows with independent statuses. That
-- is not redundancy: settlement is final at the central bank and participants
-- catch up afterwards, so "this bank has booked it and that one has not" is a
-- state the system must be able to be in. A row still in 'advised' IS the
-- unreconciled position, and it is the only in-system trace of a local posting
-- that failed.
--
-- closing_balance is what the central bank said the reserve stands at. Nothing
-- reads it yet; Task 19 is the reconciliation that does. It is stored because it
-- arrives, and a balance discarded on receipt is one nobody can go back for.
--
-- No foreign key to cycles. A member bank HAS no cycles — after the split the
-- cycles table is not in its database at all — so a constraint here would encode
-- exactly the sharing this sub-project removes.
CREATE TABLE settlement_advices (
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    cycle_id        TEXT NOT NULL,
    asset           TEXT NOT NULL,
    movement        BIGINT NOT NULL,
    closing_balance BIGINT NOT NULL,
    status          TEXT NOT NULL,
    mirror_tx       TEXT NOT NULL DEFAULT '',
    advised_at      TIMESTAMPTZ,
    posted_at       TIMESTAMPTZ,
    seq             BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, cycle_id, asset)
);

COMMENT ON COLUMN settlement_advices.movement IS
    'SIGNED: positive means this bank''s reserve went up. The statement it came '
    'from carries a magnitude and a CdtDbtInd, because the ISO 20022 money type '
    'cannot be negative; the sign is reconstructed on the way in and stored, '
    'because a mirror leg posted in the wrong direction is the most expensive '
    'way to be wrong about a settlement.';
```

Then `store/pg/tx_payment.go`: three methods, columns listed explicitly to match
the file's style. Status is stored as text — follow whatever the file already
does for `payments.status` (read it; do not invent a second convention).

- [ ] **Step 7: Run the conformance suite against both stores**

```bash
cd ~/Git/cbs-db-per-entity && go test ./store/...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./store/...
```

Expected: PASS on both.

- [ ] **Step 8: Add the recorder's overrides**

In `mesh/books_test.go`, in the `--- payment.Tx ---` group of `recordingTx`
overrides, add the three. Each is the same two statements:

```go
func (r *recordingTx) PutSettlementAdvice(ctx context.Context, book ledger.BookID, a payment.SettlementAdvice) error {
	r.rec.note(book)
	return r.Tx.PutSettlementAdvice(ctx, book, a)
}

func (r *recordingTx) GetSettlementAdvice(ctx context.Context, book ledger.BookID, cycle payment.CycleID, asset ledger.AssetCode) (payment.SettlementAdvice, error) {
	r.rec.note(book)
	return r.Tx.GetSettlementAdvice(ctx, book, cycle, asset)
}

func (r *recordingTx) ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]payment.SettlementAdvice, error) {
	r.rec.note(book)
	return r.Tx.ListSettlementAdvices(ctx, book)
}
```

`TestEveryRecordingTxMethodNotesItsBookThenDelegates` parses this file and derives
the expected note expression from each signature, so it will hold these to the
shape automatically. Run `go test ./mesh/ -run TestEveryRecordingTx` and confirm.

- [ ] **Step 9: Add the unclaimed-balances account**

In `payment/participant.go`, add to `ParticipantAccounts`:

```go
	// Unclaimed is where a credit goes when the payee's account will not take
	// it — closed, and therefore terminal.
	//
	// A real bank has one. Money that arrives for an account that cannot receive
	// it does not vanish and does not sit in the payee's closed account: it is
	// held as a liability to whoever eventually claims it, and the bank has a
	// process for finding them. This system had nowhere for it to go, which is
	// why the gap at ReturnPaymentTx and SettleCycleTx was a ruling rather than a
	// line of code.
	Unclaimed ledger.AccountID
```

Update the doc comment above the struct and the `Participant` doc's account list
(`payment/participant.go:18-30` and `:46-59`) to name the fourth internal
account.

In `payment/system.go`'s per-asset loop (`:421-452`), after the `reserve`
account:

```go
		unclaimed, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Unclaimed Balances ("+string(asset)+")", ledger.Liability, asset)
		if err != nil {
			return nil, err
		}
```

and add `Unclaimed: unclaimed.ID` to the `ParticipantAccounts` literal.

A **Liability**, because it is money the bank owes somebody it has not yet
identified — the same class as a customer's deposit, and specifically not an
asset of the bank's.

**This shifts every chart-of-accounts number after it.** Run
`go test ./payment/ ./api/ ./seed/ ./mesh/` and fix the account-number
expectations; `payment/system.go:267-276`'s `cbReservesName` note explains why the
numbering is deliberate and stable, so an expectation that moves is a fixture to
update and not a defect. Update `api/dto_payment.go:18-19` and its converter at
`:49` to expose the fourth account beside `Suspense` and `Reserve`.

- [ ] **Step 10: Extract the two postings, and add `CheckCreditTx` at settlement**

In `payment/system.go`, replace the mirror-leg loop (currently `system.go:848-869`)
with a call to a new method, and the creditor-leg loop (`:882-922`) with another.
Both are extracted VERBATIM apart from the additions named below, so this step is
behaviour-preserving except for the unclaimed-balances fix:

```go
// PostSettlementAdviceTx is a member bank booking a cut-off it was told about:
// the mirror leg, in its OWN ledger, and the row that records that it did.
//
// # What the mirror leg is
//
// A bank's clearing suspense holds money that has left a customer and not yet
// settled between banks. Settlement is when it stops being in transit, so the
// suspense moves against the reserve: a net receiver's reserve goes up and its
// suspense down, a net payer's the reverse. Suspense returns to zero only if the
// central bank's reserve movement and the clearing house's payment list agree,
// which is the reconciliation this whole conversation is for and which needs no
// cross-store read — that is what makes it legal under isolation.
//
// # It is the BANK's act
//
// It used to be the central bank's: SettleCycleTx posted every member's mirror
// leg inside its own unit of work, which is a posting in another institution's
// book. TestWhichBooksTheCentralBankReachesWhenItSettles measured exactly that.
// A settlement agent has no access to a member's ledger and no business in it;
// what it has is a statement to send.
//
// # The statement is checked before it is booked
//
// The account the statement names must be THIS bank's reserve account at the
// central bank. A bank that booked whatever arrived would move its reserve
// mirror on another member's position, and under isolation there is no second
// reader to notice. See ErrStatementNotForThisBank.
//
// # Booking twice is not reachable
//
// The idempotency key is the same "<cycle>:reserve:<participant>" the central
// bank used to post under, so a redelivered statement posts nothing; and the
// advice row is checked first, so it does not even try.
func (s *Network) PostSettlementAdviceTx(ctx context.Context, tx Tx, by ParticipantID, m AdvisedMovement) (SettlementAdvice, error) {
	p, err := s.participantTx(ctx, tx, by)
	if err != nil {
		return SettlementAdvice{}, err
	}
	accts, err := p.AccountsFor(m.Asset)
	if err != nil {
		return SettlementAdvice{}, err
	}
	if m.Account != accts.Settlement {
		return SettlementAdvice{}, fmt.Errorf("%w: %s is not %s's reserve account", ErrStatementNotForThisBank, m.Account, by)
	}

	switch existing, err := tx.GetSettlementAdvice(ctx, p.BookID, m.CycleID, m.Asset); {
	case err == nil && existing.Status == AdvicePosted:
		return existing, nil
	case err != nil && !errors.Is(err, ErrSettlementAdviceNotFound):
		return SettlementAdvice{}, err
	}

	now := s.now()
	advice := SettlementAdvice{
		Book:           p.BookID,
		CycleID:        m.CycleID,
		Asset:          m.Asset,
		Movement:       m.Movement,
		ClosingBalance: m.ClosingBalance,
		Status:         AdviceAdvised,
		AdvisedAt:      now,
	}
	// Written BEFORE the posting, so a posting that fails leaves the row at
	// Advised rather than leaving no trace. That row is the unreconciled
	// position, and it is the only thing in this system that can say a bank was
	// told and did not book.
	if err := tx.PutSettlementAdvice(ctx, p.BookID, advice); err != nil {
		return SettlementAdvice{}, err
	}

	var entries []ledger.Entry
	switch {
	case m.Movement > 0: // net receiver: reserve up, suspense down
		entries = []ledger.Entry{
			{AccountID: accts.Reserve, Amount: m.Movement, Direction: ledger.Debit},
			{AccountID: accts.Suspense, Amount: m.Movement, Direction: ledger.Credit},
		}
	case m.Movement < 0: // net payer: reserve down, suspense up
		entries = []ledger.Entry{
			{AccountID: accts.Suspense, Amount: -m.Movement, Direction: ledger.Debit},
			{AccountID: accts.Reserve, Amount: -m.Movement, Direction: ledger.Credit},
		}
	default:
		// A movement of nothing produces no leg, and the central bank sends no
		// statement for a position of zero. This arm is a guard on a caller.
		return advice, nil
	}
	posted, err := p.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(m.CycleID) + ":reserve:" + string(p.ID),
		Description:    "Net settlement of cycle " + string(m.CycleID),
		Entries:        entries,
	})
	if err != nil {
		return SettlementAdvice{}, err
	}

	advice.Status, advice.MirrorTx, advice.PostedAt = AdvicePosted, posted.ID, now
	if err := tx.PutSettlementAdvice(ctx, p.BookID, advice); err != nil {
		return SettlementAdvice{}, err
	}
	return advice, nil
}

// PostCreditorLegTx is the payee's bank releasing one settled payment out of its
// clearing suspense into the payee's account.
//
// # The check that could not be made before
//
// creditorSideTx and DepositTx both call Deposit.CheckCreditTx before money lands
// in a customer's account. This never did, and its own doc said why: settlement
// was one unit of work over the whole batch, so a check that failed took the
// entire cut-off down for one retail customer who closed an account — and a
// Cleared payment has no route out of the cycle it is in. Refusing was worse than
// stranding, so it stranded, and the ruling was recorded rather than fixed.
//
// The split is what makes the check affordable. One payment at one bank now fails
// on its own, and the residual has somewhere to go: the bank's unclaimed-balances
// account, which is what a real bank does with money that arrives for an account
// that cannot receive it. The payment still reaches Settled, because it did — the
// reserves moved and the payee's bank has been paid. What is unsettled is which
// of that bank's own accounts holds the money, which is between the bank and its
// customer and is not a fact about the payment.
//
// # Only the payee's bank may call it
//
// On a push the clearing house tells both banks the payment settled: the payer's
// bank because it has been waiting for the answer to its instruction, the payee's
// bank because it has this leg to post. Only the second may post it. See
// ErrNotThisBanksPayment.
func (s *Network) PostCreditorLegTx(ctx context.Context, tx Tx, by ParticipantID, id PaymentID) (Payment, error) {
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if p.Creditor.Participant != by {
		return Payment{}, fmt.Errorf("%w: %s is %s's creditor, not %s's", ErrNotThisBanksPayment, id, p.Creditor.Participant, by)
	}
	if p.Status == Settled {
		// A redelivered advice. The ledger's idempotency key would refuse the
		// second posting anyway; this refuses to transition twice, which
		// ErrInvalidStateTransition would otherwise report as a failure to a
		// handler that did nothing wrong.
		return p, nil
	}
	creditor, err := s.participantTx(ctx, tx, by)
	if err != nil {
		return Payment{}, err
	}
	asset, err := s.assetOf(p)
	if err != nil {
		return Payment{}, err
	}
	accts, err := creditor.AccountsFor(asset)
	if err != nil {
		return Payment{}, err
	}

	// Where the money goes: the payee's account if it can take it, and the
	// unclaimed-balances account if it cannot. Both are this bank's own.
	target := accts.Unclaimed
	description := "Unclaimed: " + p.Description
	if glAccount, err := creditor.glAccountTx(ctx, tx, p.Creditor.Account); err == nil {
		if err := creditor.Deposit.CheckCreditTx(ctx, tx, p.Creditor.Account); err == nil {
			target, description = glAccount, p.Description
		} else if !errors.Is(err, deposit.ErrAccountClosed) {
			// ErrAccountClosed is the ONLY refusal CheckCreditTx makes —
			// deposit.requireCreditable checks Closed and nothing else — so
			// anything else from here is a STORE FAILURE and not a statement
			// about the account. Diverting money to unclaimed balances because a
			// database connection dropped would be the settlement-time twin of
			// the defect Task 14 fixed in checkPartyTx, where a dropped
			// connection reported AC01 to another bank.
			return Payment{}, err
		}
	} else if !errors.Is(err, ErrAccountNotInParticipant) {
		return Payment{}, err
	}

	posted, err := creditor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":credit",
		Description:    description,
		ValueDate:      p.ValueDate,
		Metadata:       paymentMetadata(&p),
		Entries: []ledger.Entry{
			{AccountID: accts.Suspense, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: target, Amount: p.Amount, Direction: ledger.Credit},
		},
	})
	if err != nil {
		return Payment{}, err
	}
	p.CreditorLegTx = posted.ID
	if err := transition(&p, Settled); err != nil {
		return Payment{}, err
	}
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentSettled, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
}
```

Then in `SettleCycleTx`, replace the two loops with calls:

```go
		for _, leg := range legs {
			if _, err := s.PostSettlementAdviceTx(ctx, tx, leg.participant.ID, AdvisedMovement{
				Account: leg.accounts.Settlement,
				Asset:   asset,
				// ClosingBalance is not read here and is filled in 15b.2, when
				// this call moves to the bank and the balance comes off a
				// statement. Zero is honest: nothing has told this bank a
				// balance yet.
				Movement: leg.net,
				CycleID:  c.ID,
			}); err != nil {
				return Settlement{}, err
			}
		}
```

and

```go
	for _, pid := range c.PaymentIDs {
		p, err := tx.GetPayment(ctx, pid)
		if err != nil {
			return Settlement{}, err
		}
		if _, err := s.PostCreditorLegTx(ctx, tx, p.Creditor.Participant, pid); err != nil {
			return Settlement{}, err
		}
	}
```

Confirm `payment/system.go` imports `deposit` — it does; `checkPartyTx` uses it.

- [ ] **Step 11: Write the unclaimed-balances test**

Add to `payment/system_test.go`:

```go
// TestASettlementIntoAClosedAccountGoesToUnclaimedBalances is the fix for the
// ruling SettleCycleTx and ReturnPaymentTx both recorded and neither could make.
//
// A payee who empties and closes their account between their bank's acceptance
// and the cut-off used to be credited INTO the closed account: Close requires a
// zero balance, no withdrawal reaches a closed account, and Closed is terminal,
// so the money stranded for ever. The check that would have caught it was
// unaffordable while settlement was one unit of work over the batch — refusing
// took the whole cut-off down for one retail customer, with no route out.
//
// It is affordable now because one payment at one bank fails on its own, and the
// money has somewhere to go.
func TestASettlementIntoAClosedAccountGoesToUnclaimedBalances(t *testing.T) {
	// Build the two-bank fixture the file already uses, submit a credit
	// transfer, accept it, then CLOSE the payee's account before closing the
	// cycle. Follow whatever TestCrossAssetPaymentSurvivesInitiationAndFailsTheWholeCycle
	// uses to drive a cycle to settlement; do not add a second fixture.
	//
	// Assert three things:
	//   1. SettleCycle succeeds — the batch is not taken down by one closed
	//      account.
	//   2. The payment's status is Settled.
	//   3. The payee's bank's Unclaimed Balances (EUR) account holds the amount
	//      and the payee's own GL account holds nothing.
}
```

Fill the body from the file's existing helpers. **The comment block above is the
specification; the assertions are the three numbered claims and nothing else.**

- [ ] **Step 12: Run everything and watch the fix fail**

```bash
cd ~/Git/cbs-db-per-entity
gofmt -l . && go vet ./... && go build ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
```

Expected: PASS, **and every measurement in `mesh/books_test.go` unchanged** —
this sub-task moved code, not institutions. If a book-set assertion moves here,
something was extracted wrongly; investigate rather than editing the expectation.

Then delete the `target = accts.Unclaimed` default (make `target` always the
payee's GL account) and run
`go test ./payment/ -run TestASettlementIntoAClosedAccountGoesToUnclaimedBalances`.
Expected: FAIL. Restore.

- [ ] **Step 13: Commit**

```bash
cd ~/Git/cbs-db-per-entity
git add -A
git commit -m "feat(payment): a bank's own record of a cut-off, and somewhere for unreachable money

Three things that have to exist before settlement can be split, and one that has
been owed since Task 12.

SettlementAdvice is a MEMBER BANK's row: what its reserve moved by, what the
central bank says it was left at, and whether it has booked it. It is the first
book-scoped table in the payment layer, because a cycle is the clearing house's
and a settlement is the central bank's and this one is the member's.

Unclaimed Balances is where a credit goes when the payee's account is closed. It
is what makes CheckCreditTx affordable at settlement: the ruling at
SettleCycleTx said refusing would fail the whole batch, and the answer was never
to refuse but to have somewhere for the money to go.

The mirror leg and the creditor leg become PostSettlementAdviceTx and
PostCreditorLegTx. Nothing calls them from outside SettleCycleTx yet; every
measurement is unchanged, which is the evidence the extraction was faithful."
```

---

## Task 15b.2: The central bank posts its own transaction, and sends the statements

**Files:**
- Modify: `payment/system.go` (`SettleCycle`, `SettleCycleTx`)
- Modify: `mesh/ops.go` (`settlementOps`, `bankOps`), `mesh/centralbank.go`,
  `mesh/bank.go`, `mesh/mesh.go`
- Modify: `mesh/books_test.go` (**two** measurements)
- Test: `mesh/settlement_test.go`

**Interfaces:**
- Consumes: `PostSettlementAdviceTx`, `SettlementStatement`, `AdvisedMovement`,
  `StatementMessage`, `ReadStatement` from 15a.2 and 15b.1.
- Produces:
  - `SettleCycle(ctx, id) (Settlement, []SettlementStatement, error)` and the
    `Tx` variant — the return widens rather than a method being added, because
    the statements are captured inside the unit of work and cannot be re-read
    after it.
  - `bankOps.PostSettlementAdvice(ctx, by ParticipantID, m AdvisedMovement) (SettlementAdvice, error)`.
  - `mesh.bank.pid payment.ParticipantID`.

- [ ] **Step 1: Move both measurements first**

In `mesh/books_test.go`, `TestWhichBooksTheCentralBankReachesWhenItSettles`:

```go
	assertBooksTouched(t, "the central bank, settling a cycle",
		h.booksTouchedBy(h.cfg.CentralBankBIC),
		[]ledger.BookID{h.creditorBook, payment.CentralBankBook, ledger.NetworkBook})
```

The creditor's book is still in it: **this sub-task moves the mirror leg only.**
The creditor leg moves in 15b.3, and that is when this assertion reaches
`[CentralBankBook, NetworkBook]`. Say so in the test's doc, so a reader does not
take the intermediate set for the destination.

And `TestNeitherBankTouchesABookWhileTheCycleSettles` — the second tripwire, and
the one the handoff does not mention:

```go
	for _, who := range []iso20022.BIC{h.debtorBIC, h.creditorBIC} {
		if got := h.booksTouchedBy(who); len(got) != 0 {
			...
```

becomes a per-bank expectation. Both banks now book a mirror leg, so:

```go
	assertBooksTouched(t, "the payer's bank, booking its own settlement",
		h.booksTouchedBy(h.debtorBIC), []ledger.BookID{h.debtorBook})
	assertBooksTouched(t, "the payee's bank, booking its own settlement",
		h.booksTouchedBy(h.creditorBIC), []ledger.BookID{h.creditorBook})
```

Note the ABSENCE of `ledger.NetworkBook`: `PostSettlementAdviceTx` writes a
book-scoped advice row and posts in the bank's own ledger, and appends no audit
event, so nothing reaches NetworkBook. If the run says otherwise, read what
touched it before changing the expectation — an unexpected NetworkBook here is a
finding.

The test must be RENAMED: `TestNeitherBankTouchesABookWhileTheCycleSettles` now
claims the opposite of what it measures, which is the failure mode
`TestWhichBooksEachBankActuallyReaches` was renamed for. Call it
`TestEachBankBooksItsOwnSettlementAndNoOtherBooks`, and keep the old claim on the
record with the reason it stopped being true — follow the style of the note at
`books_test.go:640-648`.

- [ ] **Step 2: Run and watch both fail**

```bash
cd ~/Git/cbs-db-per-entity && go test ./mesh/ -run 'TestWhichBooksTheCentralBank|TestEachBankBooksItsOwn' -v 2>&1 | head -40
```

Expected: FAIL on both. Record the exact output; it is the before-measurement.

- [ ] **Step 3: Narrow `SettleCycleTx` and widen its return**

In `payment/system.go`, delete the `PostSettlementAdviceTx` loop added in 15b.1
and build the statements instead. The netting transaction and the creditor-leg
loop stay for now.

```go
// SettleCycleTx is SettleCycle within a caller-supplied unit of work.
//
// It returns the STATEMENTS beside the settlement because the closing balances
// are a claim about a moment. A caller that re-read them after the commit would
// be quoting whatever the accounts stand at then, and a statement asserting the
// wrong balance is worse than none: the balance is the only thing a member can
// check its own posting against.
func (s *Network) SettleCycleTx(ctx context.Context, tx Tx, id CycleID) (Settlement, []SettlementStatement, error) {
```

After `settlementLegsTx` and before the posting, add the reserve check — **the
finding at the top of this plan**:

```go
	// The central bank's decision, and the whole of what it decides: can each net
	// payer cover its position out of the reserves it holds HERE?
	//
	// It is checked explicitly because the ledger will not check it. A member's
	// settlement account in this book is a LIABILITY — the central bank owes the
	// member its reserve — and Book.checkSufficientBalance only guards Asset and
	// Expense accounts. Until this task the refusal came from the MIRROR leg in
	// the member's own book, where "Reserve at Central Bank" is an Asset; moving
	// that leg to the bank would have taken AM04 with it and settled a cycle
	// whose net payer was short, leaving the shortfall to surface at the bank as
	// a dead letter.
	//
	// Refusing to take a member's reserve below zero is the central bank
	// declining to extend uncollateralised intraday credit, which is the decision
	// a settlement agent exists to make. ledger.ErrInsufficientBalance is
	// returned rather than a new sentinel so that ReasonFor's borrowedReasons
	// keeps mapping it to AM04 — same code, same layer, same meaning.
	for _, leg := range legs {
		if leg.net >= 0 {
			continue
		}
		held, err := s.centralBank.BookBalanceTx(ctx, tx, leg.accounts.Settlement)
		if err != nil {
			return Settlement{}, nil, err
		}
		if held+leg.net < 0 {
			return Settlement{}, nil, fmt.Errorf("%w: %s is short %d in %s",
				ledger.ErrInsufficientBalance, leg.participant.ID, -(held + leg.net), asset)
		}
	}
```

After the netting transaction posts, capture the statements:

```go
	statements := make([]SettlementStatement, 0, len(legs))
	for _, leg := range legs {
		closing, err := s.centralBank.BookBalanceTx(ctx, tx, leg.accounts.Settlement)
		if err != nil {
			return Settlement{}, nil, err
		}
		statements = append(statements, SettlementStatement{
			Member:         leg.participant.ID,
			Agent:          leg.participant.BIC,
			Account:        leg.accounts.Settlement,
			Asset:          asset,
			CycleID:        c.ID,
			Movement:       leg.net,
			ClosingBalance: closing,
			ValueDate:      s.now(),
		})
	}
```

and fill `SettlementID` on each after `st` is built, before returning
`(st, statements, nil)`.

Note the balances are read AFTER the posting, inside the same unit of work, which
is what makes them closing balances. Reading them before would produce an opening
balance labelled CLBD, which is the exact error `closingBalanceIn` refuses on the
other side.

Update `SettleCycle` to match, and every caller: `go build ./...` names them.

- [ ] **Step 4: Give the bank actor its own participant id**

In `mesh/mesh.go`, `bank` gains a field and `AddBank` fills it:

```go
	// pid is which participant this actor IS, which is the question a settlement
	// advice asks: a statement is about one bank's reserve account and a payment
	// advice is about one bank's customer, and the answer must be this actor's
	// own identity rather than a lookup.
	//
	// This is NOT the bank identity Task 18 owes payment.Network. That one is
	// about narrowing ResolveIdentifierTx's sweep to "this bank's register", and
	// it needs the DOMAIN layer to know whose register is whose. This is the
	// mesh's own index turned around: Mesh.banks is already keyed by
	// ParticipantID, so the actor is being told something the mesh knew when it
	// built it. Nothing here narrows a sweep.
	pid payment.ParticipantID
```

`AddBank(p *payment.Participant)` and `joinRoster` both construct banks; set
`pid: p.ID` in each. Grep for `&bank{` to find them all.

- [ ] **Step 5: Wire the two handlers**

In `mesh/ops.go`, `settlementOps.SettleCycle`'s signature widens, and `bankOps`
gains:

```go
	// The bank's half of settlement, and it is the half that used to be done TO
	// it. A member books its own mirror leg from the statement the settlement
	// agent sent; nothing else in this mesh may post in that book, and nothing
	// on this interface lets this bank post in anybody else's.
	PostSettlementAdvice(ctx context.Context, by payment.ParticipantID, m payment.AdvisedMovement) (payment.SettlementAdvice, error)
```

In `mesh/centralbank.go`, `receiveSettlement` sends the statements after the
domain call and before the answer:

```go
	st, statements, err := cb.ops.SettleCycle(ctx, id)
	if err != nil {
		if errors.Is(err, payment.ErrCycleNotClosed) || errors.Is(err, payment.ErrInvalidStateTransition) {
			return fmt.Errorf("mesh: %s was told to settle %s again: %w", cb.bic, id, err)
		}
		return cb.answer(from, orig, string(id), string(id), iso20022.TransactionStatusRejected, err)
	}
	if err := cb.advise(statements); err != nil {
		return err
	}
	return cb.answer(from, orig, string(id), string(id), iso20022.TransactionStatusSettlementCompleted, nil)
```

with:

```go
// advise sends each member the statement of its own reserve account.
//
// # Why the members and not the clearing house
//
// The clearing house is the party that INSTRUCTED and is the one this actor
// answers; the members are the parties whose accounts moved, and they are not
// parties to that conversation at all. Each gets a message about its own account
// and about no other, which is the whole of what an account servicer tells an
// account holder.
//
// # After the unit of work, and before the answer
//
// After, for Mesh.Submit's reason: a statement enqueued from inside SettleCycleTx
// would be one a bank could book against a settlement the store then rolled back.
//
// Before the answer, and the order is load-bearing rather than tidy. The
// clearing house turns the ACSC into per-payment advices, and a payee's bank
// posts its creditor leg out of its clearing suspense on those. The mirror leg
// is what puts the money in that suspense. Both orders produce the same books at
// rest — suspense is a Liability and the ledger does not guard those against
// going negative — but only this one keeps the suspense from being transiently
// overdrawn, and an actor's inbox is FIFO, so sending the statements first is
// what makes the order deterministic rather than incidental.
//
// # A failed send is not a failed settlement
//
// The reserves have moved and the cycle is Settled: that is final, and this
// actor cannot unsay it. So a send that fails comes back as an error to the
// caller — which in this transport reaches Drain as a dead letter — and the
// advice row at the bank that was never told stays absent. That is the
// unreconciled position in its most visible form, and Task 19 is what makes it
// detectable from inside the system.
func (cb *centralBank) advise(statements []payment.SettlementStatement) error {
	for _, st := range statements {
		env, err := payment.StatementMessage(st, payment.MessageContext{
			From:  cb.bic,
			To:    st.Agent,
			MsgID: cb.m.nextMsgID(cb.bic),
			Now:   cb.m.now(),
		})
		if err != nil {
			return fmt.Errorf("mesh: %s could not build the statement for %s: %w", cb.bic, st.Agent, err)
		}
		if err := cb.m.send(cb.bic, st.Agent, env); err != nil {
			return fmt.Errorf("mesh: %s settled %s and could not tell %s: %w", cb.bic, st.CycleID, st.Agent, err)
		}
	}
	return nil
}
```

In `mesh/bank.go`, add the arm to `handle`:

```go
	case *iso20022.Camt053:
		return b.receiveStatement(ctx, from, doc)
```

and the handler:

```go
// receiveStatement is a bank booking its own share of a cut-off, from the
// central bank's statement of its reserve account.
//
// # It answers nothing
//
// Every other inbound message in this package produces a pacs.002. This produces
// none, and that is the message definition rather than an omission: a statement
// is not an instruction and there is nothing to accept or refuse. The central
// bank has already settled — finality is the premise of this whole conversation —
// so a bank that answered "no" would be refusing something that has happened.
//
// What a failure produces instead is an ERROR, which this transport turns into a
// dead letter, and an advice row left at Advised. Those two together are the
// unreconciled position: told and not booked. Task 19 is the reconciliation that
// makes it visible from inside the system rather than only in Drain.
//
// # One statement, one member
//
// This system's central bank sends a member a statement about that member's own
// reserve account and about nothing else, so a document carrying several is one
// this handler has no rule for. It is refused whole rather than partially
// booked, for the reason cycleOf gives: booking the first and dropping the rest
// would move a reserve mirror by the wrong amount with nothing recording it.
//
// The check that the account is THIS bank's is the domain's, not this handler's
// — see payment.PostSettlementAdviceTx and ErrStatementNotForThisBank — because
// it is a question about this bank's chart of accounts and the handler holds no
// chart.
func (b *bank) receiveStatement(ctx context.Context, from iso20022.BIC, doc *iso20022.Camt053) error {
	moves, err := payment.ReadStatement(doc)
	if err != nil {
		return fmt.Errorf("mesh: %s could not read the statement from %s: %w", b.bic, from, err)
	}
	if len(moves) != 1 {
		return fmt.Errorf("mesh: %s got a statement from %s carrying %d accounts; a member is told about its own",
			b.bic, from, len(moves))
	}
	if _, err := b.ops.PostSettlementAdvice(ctx, b.pid, moves[0]); err != nil {
		return fmt.Errorf("mesh: %s could not book the settlement of %s: %w", b.bic, moves[0].CycleID, err)
	}
	return nil
}
```

The handler needs **no** `withActor` call of its own: `Mesh.dispatch` already
invokes every handler with `withActor(ctx, a.bic)` (`mesh/mesh.go:781`), which is
how every other inbound handler is attributed. `csm.closeCycle` adds one only
because a cut-off arrives from outside the mesh and never passes through
`dispatch`.

- [ ] **Step 6: Give the seed a settlement composite**

`seed/seed.go:481` and `:490` call `b.net.SettleCycle` **directly**, and they must
not simply keep doing so. The seed runs BEFORE any actor exists — `cmd/server`
seeds and then starts the mesh, and `main.go` says the order is load-bearing
because `Start` reads the roster once — so there is nothing to send a statement
to and nobody to answer one. A direct `SettleCycle` after this sub-task settles at
the central bank and advises nobody, leaving every seeded bank's suspense
non-zero and its reserve mirror unmoved. `seed/seed_test.go` asserts the dataset's
shape and will say so.

This is the case `builder.initiate` and `builder.reject` already exist for, and
`initiate`'s doc has the argument in full: *"The seed is one process building a
scenario, so it plays every actor; the mesh is what makes them separate."* Add the
third composite beside them:

```go
// settle runs all three institutions' halves of a cut-off — the settlement
// agent's netting transaction and each member's booking of the advice it would
// have been sent — in one unit of work, leaving the cycle Settled and every
// bank's clearing suspense back at zero.
//
// It is initiate's argument applied to settlement, and it became necessary the
// moment settlement stopped being one institution's act. There is no method that
// plays all three, deliberately: the whole of Task 15 is that the central bank
// cannot post in a member's book. The seed can, because the seed is not an
// institution — it is one process building a fixed scenario before any actor
// exists, and a conversation carried out at startup could not promise a fixed
// outcome.
//
// The creditor legs are NOT composed here in this sub-task, because
// SettleCycleTx still posts them. They move in with the same argument when it
// stops.
func (b *builder) settle(id payment.CycleID) {
	check(b.net.Store().Update(b.ctx, func(ctx context.Context, tx payment.Tx) error {
		_, statements, err := b.net.SettleCycleTx(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, st := range statements {
			if _, err := b.net.PostSettlementAdviceTx(ctx, tx, st.Member, payment.AdvisedMovement{
				Account:        st.Account,
				Asset:          st.Asset,
				Movement:       st.Movement,
				ClosingBalance: st.ClosingBalance,
				CycleID:        st.CycleID,
				ValueDate:      st.ValueDate,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
}
```

Replace both `must(b.net.SettleCycle(b.ctx, …))` calls with `b.settle(…)`. Run
`go test ./seed/ ./cmd/... ./api/` — `seed_test.go` asserts the seeded dataset's
shape, and if a reserve or suspense figure moves, read it before changing it.

- [ ] **Step 7: Run the measurements**

```bash
cd ~/Git/cbs-db-per-entity && go test ./mesh/ -run 'TestWhichBooksTheCentralBank|TestEachBankBooksItsOwn' -v
```

Expected: PASS, both.

- [ ] **Step 8: Watch the reserve check fail — the most important mutation in this plan**

Delete the net-payer reserve loop added in Step 3. Run:

```bash
cd ~/Git/cbs-db-per-entity && go test ./mesh/ -run 'TestSettlementIsRefusedWhenAMemberCannotCover' -v
```

Expected: FAIL — the cycle settles and the answer is ACSC rather than RJCT/AM04.
Restore, re-run, confirm RJCT/AM04. **If it still passes with the check deleted,
stop:** something else is refusing and the check has not been located correctly.
Find what, and say so, before continuing.

- [ ] **Step 9: Full verification and commit**

```bash
cd ~/Git/cbs-db-per-entity
gofmt -l . && go vet ./... && go build ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
git add -A
git commit -m "feat(payment,mesh): the central bank tells each member, and each member books its own

The mirror leg leaves the settlement agent's unit of work and becomes a camt.053
the member books for itself. Two measurements move: the central bank stops
reaching the payer's book, and the banks stop reaching none.

The reserve check moves WITH it, and had to. AM04 came from the mirror leg in the
member's own book — Reserve at Central Bank is an Asset account and the ledger
guards those — while a member's settlement account HERE is a Liability, which the
ledger does not guard. Moving the leg without moving the check would have settled
a cycle whose net payer was short and left the shortfall to surface as a dead
letter. It is now the central bank's own explicit refusal to take a member's
reserve below zero, which is the decision a settlement agent exists to make."
```

---

## Task 15b.3: The clearing house advises the payee's bank, which posts its own creditor leg

**Files:**
- Modify: `payment/system.go` (`SettleCycleTx` loses the creditor-leg loop)
- Modify: `mesh/csm.go:803-831` (`tellSettled`), `mesh/bank.go:414-470`
  (`receiveStatus`), `mesh/ops.go` (`bankOps`)
- Modify: `mesh/books_test.go` (the tripwire lands)
- Test: `mesh/settlement_test.go`

**Interfaces:**
- Consumes: `PostCreditorLegTx` from 15b.1; `bank.pid` from 15b.2.
- Produces: `bankOps.PostCreditorLeg(ctx, by payment.ParticipantID, id payment.PaymentID) (payment.Payment, error)`.

- [ ] **Step 1: Land the tripwire**

In `mesh/books_test.go`:

```go
	assertBooksTouched(t, "the central bank, settling a cycle",
		h.booksTouchedBy(h.cfg.CentralBankBIC),
		[]ledger.BookID{payment.CentralBankBook, ledger.NetworkBook})
```

**This is the failure the handoff and the spec both predicted.** It is the signal
Task 15 has arrived, not a regression.

`TestEachBankBooksItsOwnSettlementAndNoOtherBooks` moves too: the payee's bank now
also posts a creditor leg and appends `payment.settled`, which allocates an id in
NetworkBook. So:

```go
	assertBooksTouched(t, "the payer's bank, booking its own settlement",
		h.booksTouchedBy(h.debtorBIC), []ledger.BookID{h.debtorBook})
	assertBooksTouched(t, "the payee's bank, booking its own settlement and paying its customer",
		h.booksTouchedBy(h.creditorBIC), []ledger.BookID{h.creditorBook, ledger.NetworkBook})
```

The asymmetry is a measurement and worth a sentence in the doc: the payer's bank
touches NetworkBook and the payee's does, because only one of them writes a
payment row. Under Task 18 the payment row is per-entity and both will.

- [ ] **Step 2: Run and watch it fail**

```bash
cd ~/Git/cbs-db-per-entity && go test ./mesh/ -run 'TestWhichBooksTheCentralBank|TestEachBankBooksItsOwn' -v 2>&1 | head -40
```

Expected: FAIL on both, with the central bank still reaching `bank_3`. Record it.

- [ ] **Step 3: Take the creditor leg out of `SettleCycleTx`**

Delete the `for _, pid := range c.PaymentIDs` loop entirely. **The payments no
longer transition to Settled here**, which means `SettleCycleTx` no longer reads
a payment at all — it reads the cycle, the participants and its own book, and
nothing else.

Rewrite `SettleCycle`'s doc comment. The section headed `# The settlement window`
argues at length that all of it is one unit of work and that "the interval in
which the books are inconsistent is not observable". That is now **false in the
strongest way**: the interval is not merely observable, it is the thing being
modelled. Replace it, keeping the old claim on the record with the reason it was
right at the time — the settlement window is real, and what changed is that this
system stopped pretending one process could hold every institution's books inside
it.

The doc must say:

- the central bank posts one transaction, in its own book, and is FINAL either
  way;
- what each member does afterwards is that member's, on advice;
- the interval between is the unreconciled position, and in the EU finality is a
  directive rather than a modelling convenience;
- `ErrCycleNotClosed` and the idempotency key are still what make a redelivered
  instruction post nothing.

- [ ] **Step 4: Fan the ACSC out to the creditor's bank**

In `mesh/csm.go`, `tellSettled` currently addresses the submitter alone. Both
banks must hear, and for different reasons.

```go
// tellSettled turns one settled CYCLE into per-PAYMENT advices, addressed to the
// banks that have something to do about each.
//
// # Two recipients, and they are not told the same thing for the same reason
//
// The SUBMITTER is waiting for the answer to its instruction: it sent a pacs.008
// or a pacs.003 naming one payment and has heard ACCP since, and ACSC is what
// closes it. That recipient is unchanged from Task 12.
//
// The CREDITOR's bank has a LEG TO POST. Settlement moved the reserves; the
// payee is paid when its own bank releases the money out of its clearing
// suspense, and until Task 15 the central bank did that FOR it, in that bank's
// own book. This message is what replaces the reach.
//
// On a PULL they are the same institution and there is one message: the payee's
// bank submitted the collection and is also the creditor. On a PUSH they are two,
// and the second is new. Deduplicating rather than sending twice, because a bank
// that received the same advice twice would post nothing the second time — the
// ledger's idempotency key and PostCreditorLegTx's own Settled guard both see to
// that — but would still be told twice, and a system that says everything twice
// teaches nothing about who needs to hear what.
//
// # The central bank could not send these
//
// It is answering about a CYCLE, and settlementOps holds nothing that turns one
// into the payments inside it. That was true before this task and it is why the
// fan-out lives here; what is new is that the message now causes a POSTING at the
// recipient rather than merely closing its instruction.
```

The body: for each payment, build the recipient set as
`{submitterOf(scheme, p.Debtor, p.Creditor).Participant, p.Creditor.Participant}`,
deduplicated, resolve each to a BIC with `c.ops.GetParticipant`, and `c.forward`
to each.

- [ ] **Step 5: Post the leg at the receiving bank**

In `mesh/ops.go`, `bankOps` gains:

```go
	// The payee's bank's half of settlement: release one payment out of its own
	// clearing suspense into its own customer's account.
	//
	// It takes the ACTING participant because both banks are told a payment
	// settled and only one may post it — see payment.ErrNotThisBanksPayment. The
	// domain refuses the other, rather than this package deciding: which bank a
	// payment's creditor banks at is a fact about the payment, and a handler that
	// decided it would be asserting something it cannot check.
	PostCreditorLeg(ctx context.Context, by payment.ParticipantID, id payment.PaymentID) (payment.Payment, error)
```

In `mesh/bank.go`'s `receiveStatus`, the acceptance arm currently returns having
done nothing, and its doc says an acceptance needs nothing. Both change. Add,
before the rejection handling:

```go
	if r.Status == iso20022.TransactionStatusSettlementCompleted {
		// ACSC. This bank posts its creditor leg if the payee is its customer,
		// and does nothing at all if it is not — which on a push is the payer's
		// bank, hearing the answer to the instruction it sent. The domain decides
		// which this bank is; ErrNotThisBanksPayment is the ordinary case for one
		// of the two recipients and is not a failure.
		if _, err := b.ops.PostCreditorLeg(ctx, b.pid, payment.PaymentID(r.TxID)); err != nil {
			if errors.Is(err, payment.ErrNotThisBanksPayment) {
				return nil
			}
			return fmt.Errorf("mesh: %s could not pay its customer for %s: %w", b.bic, r.TxID, err)
		}
		return nil
	}
```

Place it so the existing guards that run first — the empty-`TxID` skip and
whatever `receiveStatus` does before dispatching on status — still run first.
Read the function before inserting; it has a rejection path with two roles and an
`ACCP` path, and this must not reorder either.

Rewrite the doc paragraph at `mesh/bank.go:369-374` that begins *"An ACCEPTANCE
needs nothing from it"*: an ACCP still needs nothing and an ACSC now needs a
posting, and those are different statuses. Getting that wrong makes the payment's
clearing acceptance post its settlement leg.

- [ ] **Step 5b: Finish the seed's settlement composite**

`builder.settle`, added in 15b.2 Step 6, composes the settlement agent's netting
transaction and each member's advice. The creditor legs were left out because
`SettleCycleTx` still posted them; it no longer does, so the seed's payments would
stay `Cleared`, every payee would be unpaid, and **Phase C's
`ReturnPayment` on `SDD-002` would fail with `ErrInvalidStateTransition` and
`must` would panic the seed** — which `cmd/server` runs at startup, so the app
would not boot. `cmd/server/main_test.go` and `api/mesh_test.go` both build the
seeded dataset and will catch it.

Add the third loop to `builder.settle`, inside the same unit of work, after the
advices:

```go
		cycle, err := b.net.GetCycleTx(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, pid := range cycle.PaymentIDs {
			p, err := tx.GetPayment(ctx, pid)
			if err != nil {
				return err
			}
			if _, err := b.net.PostCreditorLegTx(ctx, tx, p.Creditor.Participant, pid); err != nil {
				return err
			}
		}
```

If `GetCycleTx` does not exist under that name, read the cycle with
`tx.GetCycle(ctx, id)` — the seed holds a `payment.Tx` and does not need a
Network wrapper for a plain row read.

Update `settle`'s doc: the sentence saying the creditor legs are not composed
here "because SettleCycleTx still posts them" is now false, and it is the kind of
prose that outran the code. Say instead that all three institutions' halves are
here, and why the seed is allowed to play all three when no institution is.

- [ ] **Step 6: Run the measurements and the flow tests**

```bash
cd ~/Git/cbs-db-per-entity && go test ./mesh/ -v 2>&1 | tail -40
```

Expected: PASS. Several tests in `mesh/settlement_test.go` and `mesh/sct_test.go`
assert message COUNTS; a push now produces one more pacs.002 per payment. Those
counts are correct to update — the extra message is the point of the task — but
each one must be read before it is changed, because a count that moved by more
than one is a fan-out that is sending to the wrong set.

- [ ] **Step 7: Pin the boundary, and watch it fail**

Add to `mesh/settlement_test.go`:

```go
// TestOnlyThePayeesBankPaysThePayee pins which of the two banks told about a
// settled payment may act on it.
//
// On a push the clearing house tells both: the payer's bank because it has been
// waiting for the answer to its instruction, the payee's bank because it has a
// leg to post. If the payer's bank posted it, the payee would be credited in the
// wrong institution's book — the exact crossing this sub-project removes, arrived
// at from the other direction.
//
// It is asserted at the DOMAIN layer, where the refusal lives, because that is
// where it can be made to fail: the mesh never hands the payer's bank's id to a
// payment it does not own, so a mesh-level test would pass with the guard gone.
func TestOnlyThePayeesBankPaysThePayee(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	// The payer's bank asking to post the payee's leg is refused, whatever else
	// is true of the payment.
	_, err := h.net.PostCreditorLeg(context.Background(), h.debtor.ID, p.ID)
	if !errors.Is(err, payment.ErrNotThisBanksPayment) {
		t.Errorf("the payer's bank got %v, want ErrNotThisBanksPayment", err)
	}
}
```

`h.net` is the harness's `*payment.Network`; use whatever field name
`newHarness` gives it — read `mesh/harness_test.go:243-300` and use that.

Then delete the `p.Creditor.Participant != by` guard from `PostCreditorLegTx` and
re-run this test. Expected: FAIL. Restore.

- [ ] **Step 8: Full verification and commit**

```bash
cd ~/Git/cbs-db-per-entity
gofmt -l . && go vet ./... && go build ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
git add -A
git commit -m "feat(payment,mesh): the payee's bank pays the payee

The creditor leg leaves the settlement agent's unit of work. The clearing house's
per-payment advice now reaches the creditor's bank as well as the submitter —
one message on a pull, where they are the same institution, and two on a push —
and that bank releases the money out of its own clearing suspense into its own
customer's account.

TestWhichBooksTheCentralBankReachesWhenItSettles drops from allBooks() to
[CentralBankBook, Network]. That is the tripwire the spec armed and the handoff
predicted; it is the signal the task arrived.

SettleCycleTx no longer reads a payment at all. It reads a cycle, the roster and
its own book, which is the whole of what a settlement agent has."
```

---

## Task 15b.4: The domain facts, in every layer

**Files:**
- Modify: `README.md`
- Modify: `web/src/components/hint-content.ts`
- Modify: `web/src/lib/quiz/chapters/09-clearing-and-settlement.ts`,
  `11-payment-schemes.ts`, `15-*.ts`, `16-*.ts` (use the real filenames — `ls
  web/src/lib/quiz/chapters/`)
- Modify: `store/pg/schema/0001_init.sql` (verify only — the comments were
  written in 15b.1)
- Modify: `docs/superpowers/specs/2026-08-02-db-per-entity-design.md`
- Modify: `payment/doc.go`, `mesh/doc.go`, `mesh/ops.go`, `mesh/centralbank.go`

**Interfaces:**
- Consumes: everything above.
- Produces: nothing code-facing.

- [ ] **Step 1: Sweep for the claims that are now false, BEFORE writing anything**

This is the step Task 14.6 did not have and paid five review rounds for. Run the
sweep first and produce a hit table; the edits follow from it.

```bash
cd ~/Git/cbs-db-per-entity
grep -rn "one unit of work\|single unit of work\|all-or-nothing\|whole batch\|batch fails whole\|atomically\|settlement window" \
  --include='*.go' --include='*.md' --include='*.ts' --include='*.sql' . \
  | grep -iv node_modules
grep -rn "posts in every\|every participant's book\|every member's book\|widest-reaching\|reaches EVERY book\|allBooks" \
  --include='*.go' --include='*.md' --include='*.ts' . | grep -iv node_modules
grep -rn "unclaimed\|strands\|stranded\|closed account" \
  --include='*.go' --include='*.md' --include='*.ts' --include='*.sql' . | grep -iv node_modules
grep -rn "camt" --include='*.go' --include='*.md' --include='*.ts' . | grep -iv node_modules
```

Report the table. Known members of it, so a shorter list is a sweep that missed
something:

| location | claim that is now false |
|---|---|
| `payment/system.go` `SettleCycle` doc | "All of it is one unit of work" — rewritten in 15b.3 |
| `payment/system.go` `ReturnPaymentTx` doc `# A KNOWN GAP` | the settlement half of the gap is closed; the return half is Task 16's |
| `payment/errors.go` `ErrAssetMismatch` doc | "one mismatched payment fails the entire clearing cycle" |
| `mesh/ops.go` `settlementOps` doc | "SettleCycleTx posts in EVERY participant's book"; "the central bank is the widest-reaching actor" |
| `mesh/ops.go` `bankOps` doc | a bank now has two posting methods it did not have |
| `mesh/centralbank.go` type doc | "Settlement posts in every participant's book as well as the central bank's" |
| `mesh/centralbank.go` `receiveSettlement` doc | "the whole batch fails — which is what a settlement window is" |
| `mesh/csm.go` `receiveSettlementStatus` doc | "SettleCycleTx is one unit of work, so nothing was posted anywhere" — still true of RJCT, and the sentence's reasoning changed |
| `mesh/doc.go:142` | "The CENTRAL BANK discharges them, whole or not at all" |
| `iso20022/doc.go` | done in 15a.1 — verify |
| `payment/translate.go:144` | "SettleCycleTx posts the mirror leg straight through" |
| `payment/system_test.go:1129`, `:587` | test comments naming the old shape |
| `payment/translate_test.go:126` | "inside SettleCycleTx: the mirror leg would take an asset account negative" — this is the AM04 finding, in a test comment, and it is now about the central bank's own check |

**`payment/translate_test.go:126` is the one to be most careful with.** It records
the old location of the reserve refusal in prose. Rewriting it to point at the new
check is how the finding survives.

- [ ] **Step 2: The facts, in `README.md`**

`README.md` is authoritative and the other layers are distilled from it. Four
facts, none of which any layer carries today:

1. **Settlement is final at the central bank, and the participants catch up
   afterwards.** In the EU that is the Settlement Finality Directive, not a
   modelling convenience. The interval between is the unreconciled position, and
   a bank's clearing suspense is where it shows.
2. **The clearing house has no ledger; the central bank has no customers; a bank
   has no cycles.** Each institution knows what its own job needs and nothing
   else.
3. **A bank reconciles two advices from two institutions against one balance.**
   The central bank says what its reserve moved by; the clearing house says which
   payments settled. Its suspense returns to zero only if the two agree.
4. **Money that arrives for an account that cannot receive it goes to unclaimed
   balances.** It is a liability of the bank to whoever eventually claims it, and
   having somewhere for it to go is what makes the check affordable.

Put them in the clearing-and-settlement section.

- [ ] **Step 3: Hint keys, then the wiki-links**

Add `hint-content.ts` entries for the concepts before any `[[wiki-link]]` names
them. **A link to a key that is not there throws at runtime under `RootLayout`
and takes every route down while `next build` stays green.** Suggested keys —
check the file's existing naming convention and follow it rather than these
literally: `settlement-finality`, `unreconciled-position`, `unclaimed-balances`,
`nostro-reconciliation`.

- [ ] **Step 4: Quiz questions**

Chapter 9 (clearing and settlement) is where facts 1 and 3 belong; chapter 15 or
16 (persistence) is where "a bank has no cycles" belongs, because it is a claim
about the schema. Check `diversity.test.ts`'s limits first: 18–22 questions, ≥8
distinct `concept` tags, no tag more than 3×, all three difficulty tiers.
**Chapter 12 is at 21 with three tags already at 3** — if a fact lands there,
replace a weaker question rather than adding.

- [ ] **Step 5: Verify the schema comments and run the web tests**

```bash
cd ~/Git/cbs-db-per-entity/web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

`npm run test` catches a bad wiki-link in hint bodies **and** in quiz
explanations, which the runtime guard does not scan. Then start the app, load a
page, and **take a screenshot and look at it** — no test here can catch a
rendering regression, and on the Task 14 branch a screenshot is what revealed the
send form teaching the negation of the fact the branch existed to establish.

- [ ] **Step 6: Record the task reallocation in the spec**

In `docs/superpowers/specs/2026-08-02-db-per-entity-design.md`, the Tasks table
puts camt.053 in Task 19. Add a dated note under the table — do not rewrite the
row, for the reason the other pre-ruling wording is left alone:

```markdown
### camt.053 moved from Task 19 to Task 15, and why (2026-08-02)

Task 15's mirror leg needs an advice from the CENTRAL BANK, and the spec's own
settlement flow says so: the bank posts "its mirror leg from the statement and its
creditor legs from the payment advices". That split is load-bearing rather than
incidental — "suspense returns to zero only if the central bank's reserve movement
and the clearing house's payment list agree" is a check between two SENDERS, and
if both legs came from the clearing house there would be nothing to reconcile.

So the message family landed as Task 15a and the conversation as 15b. Task 19
keeps the reconciliation it is named for: the closing-balance check against
Bal/CLBD, break detection, and the surface that makes a break visible to an
operator rather than only to the test suite. `SettlementAdvice.ClosingBalance` is
already stored and already unread; Task 19 is what reads it.
```

- [ ] **Step 7: Full verification and commit**

```bash
cd ~/Git/cbs-db-per-entity
gofmt -l . && go vet ./... && go build ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test && cd ..
git add -A
git commit -m "docs: settlement is final at the central bank, and the banks catch up

Four facts across every layer that duplicates domain content, per CLAUDE.md, plus
the Go and test comments that argued the opposite at length. Settlement was one
unit of work here and its docs said so in a dozen places; it is a conversation
now, and the interval between the central bank's finality and a member's booking
is the unreconciled position rather than an inconsistency.

The sweep was run before the edits, not after a reviewer found the second
instance. Task 14.6 took five rounds for want of that."
```

---

## Self-Review

**Spec coverage.** This plan covers the Task 15 row of the spec —
*"Central bank posts only its own netting transaction; banks post mirror and
creditor legs locally on advice; settlement-position row; unclaimed balances;
`CheckCreditTx` at settlement"* — plus the camt.053 the row's own flow requires
and the spec scheduled elsewhere. Mapping: netting transaction only (15b.2/15b.3),
mirror leg locally (15b.2), creditor legs locally (15b.3), settlement-position row
(15b.1, as `SettlementAdvice`), unclaimed balances and `CheckCreditTx` (15b.1),
the documentation layers CLAUDE.md requires (15b.4). The spec's step 4 — *"the
clearing house fans its existing per-payment pacs.002 out to each bank"* — is
15b.3 Step 4.

**Spec deviations, both stated rather than absorbed.** (1) camt.053 moves from
Task 19, recorded in 15b.4 Step 6. (2) The spec does not mention that AM04 lives
in the mirror leg today; the plan's second section does, and 15b.2 Step 7 is the
mutation that proves it. This is the single most likely thing to be lost by a
faithful-looking implementation.

**Two tripwires, not one.** The handoff names
`TestWhichBooksTheCentralBankReachesWhenItSettles`.
`TestNeitherBankTouchesABookWhileTheCycleSettles` moves too — the banks now book
during the cut-off — and it must be RENAMED, because its name would then claim the
opposite of its measurement. The central bank's assertion moves TWICE, in 15b.2
and again in 15b.3, and 15b.2 Step 1 says so, so the intermediate set is not
mistaken for the destination.

**Type consistency.** `SettlementStatement` (15a.2) is produced by `SettleCycleTx`
(15b.2) and consumed by `StatementMessage` (15a.2). `AdvisedMovement` (15a.2) is
produced by `ReadStatement` (15a.2) and consumed by `PostSettlementAdviceTx`
(15b.1), called from `receiveStatement` (15b.2). `SettlementAdvice` (15b.1) is
what `PostSettlementAdviceTx` returns and what the three `Tx` methods store.
`ParticipantAccounts.Unclaimed` (15b.1) is read by `PostCreditorLegTx` (15b.1),
called from `receiveStatus` (15b.3). `bank.pid` is added in 15b.2 and used by both
handlers. `iso20022.AccountStatement` (15a.1, the XML struct) and
`payment.AdvisedMovement` are different types in different packages and never
alias.

**The seed is not an actor, and this task is where that starts to cost.**
`seed/seed.go` calls `SettleCycle` directly and runs BEFORE any mesh actor
exists — `cmd/server` seeds first and starts the mesh second, which `main.go`
records as load-bearing. So the seed grows a third composite beside `initiate`
and `reject` (15b.2 Step 6, completed in 15b.3 Step 5b), on `initiate`'s own
stated argument: the seed is one process building a fixed scenario and plays every
actor, and a conversation carried out at startup could not promise a fixed
outcome. Missing this breaks the app at boot rather than in a test — Phase C
returns a payment that would never have reached Settled.

**Ordering risk, flagged rather than hidden.** 15b.1 extracts both postings while
`SettleCycleTx` still calls them, so every measurement is unchanged and the
extraction is provably faithful; 15b.2 and 15b.3 then move one institution each.
That is why 15b.1 Step 12 asserts the book sets did NOT move — the one step in
this plan whose expected result is silence.

**Known gaps carried forward, none of them silent.** `ClosingBalance` is stored
and unread until Task 19. The advice row has no API or web surface — an operator
cannot see an unreconciled position, which is exactly what Task 19 is for. The
return still posts in three books; that is Task 16 and `ReturnPaymentTx`'s half of
the unclaimed-balances gap stays open until then. No golden file is
schema-validated, here or anywhere, and the camt.053 added in 15a.1 inherits that:
`TestGoldenFilesValidateAgainstTheSchema` will skip it. A skip is not a pass.
