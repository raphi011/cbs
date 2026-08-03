# Task 16 — the return becomes a conversation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `ReturnPaymentTx` stops posting in three books in one unit of work. The
settlement agent reverses reserves in its own book from what the pacs.004 says;
each bank posts its own customer leg and its own reserve mirror, locally.

**Architecture:** the shape Task 15 established, applied to the R-transaction.
Two legs — a **clawback** always at the creditor's bank out of
`Payment.CreditorLegAccount`, a **refund** always at the debtor's bank into
`Payment.Debtor.Account` — with the scheme's direction deciding only which of
them the *returning* bank is holding. The returning bank posts its own leg
against its clearing suspense **before it sends**, which is what makes a push-side
refusal binding; the other bank posts after finality, which is why it must force
and why `Returns Receivable` exists. Reserve mirrors move only ever from a
camt.053, as in Task 15.

**Tech stack:** Go (no new dependencies), Postgres (`store/pg`) and the in-memory
store (`store/mem`) behind one conformance suite (`store/storetest`), Next.js/TS
for the teaching layers.

**Design:** `docs/superpowers/specs/2026-08-02-db-per-entity-design.md`, section
`## The return` and its `### Task 16's shape, settled 2026-08-03`. Where this plan
and the spec disagree, the spec wins and the plan is wrong.

## Global constraints

- **Every measurement move is watched failing first.** For every behavioural
  claim: break the code, watch that exact test fail with that exact message,
  restore. A test whose failure nobody has observed is not evidence. When a
  mutation does *not* produce a failure, the assertion is wrong until proven
  otherwise — **but so is the mutation** (see Task 15's `advise`/`answer`
  ordering, which needed swap-plus-a-delay to fail deterministically).
- **Write the final prose from the code, not from this plan.** Every comment
  this plan contains is a *prediction* made before the code exists. Six of the
  eight review rounds on Task 15's branch were comments asserting things the
  code did not do, inherited verbatim from the plan. Read what you actually
  wrote, then write the comment.
- **Both stores, every task that touches one.** `go test ./...` runs on
  `store/mem`; `TEST_DATABASE_URL=… go test ./...` runs the same suites on
  `store/pg`. `store/pg` must never accept or refuse a write differently from
  `store/mem`; `store/storetest` is what enforces it. **`make test-pg` uses
  Docker and does not work on this machine** — use the explicit
  `TEST_DATABASE_URL` invocation below.
- **Documentation is domain content and a correction is a sweep, not an edit.**
  `README.md` is authoritative; `web/src/components/hint-content.ts`, the 18 quiz
  chapters under `web/src/lib/quiz/chapters/` and the comments in
  `store/pg/schema/0001_init.sql` duplicate it by design and must agree with it
  and with the code.
- **A `[[wiki-link]]` to a key missing from `hint-content.ts` throws at runtime**
  and takes every route down while `next build` stays green. `npm run test`
  catches it in hint bodies *and* in quiz explanations. Run it, and load a page.
- **`web/src/lib/quiz/diversity.test.ts`** holds each chapter to 18–22 questions,
  ≥8 distinct `concept` tags, no tag more than 3×, all three difficulty tiers.
  **Chapter 9 is at 22 — the maximum.** A new question there means replacing one.
- **Never hand-write a backend path** in web code; use `cb()` / `csm()` /
  `bank(pid, …)`.
- **`rm -rf web/.next` before `npm run typecheck`.**

**Verification, run in full at the end of every task:**

```bash
gofmt -l . && go vet ./... && go build ./...
go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

**Workspace:** the sub-project's worktree is `~/Git/cbs-db-per-entity` on branch
`spec/db-per-entity`, currently at `8b94109` — *behind* `main`, because Task 15
was merged. Before Task 16a, reset it onto `main`:

```bash
git -C ~/Git/cbs-db-per-entity fetch --all
git -C ~/Git/cbs-db-per-entity checkout spec/db-per-entity
git -C ~/Git/cbs-db-per-entity reset --hard main
```

---

## File structure

| file | what changes |
|---|---|
| `iso20022/pacs004.go` | `OriginalTransactionReference`; `ReturnTransaction.OrgnlTxRef`; the reversed ruling, written as a reversal |
| `iso20022/testdata/pacs004.xml` | the golden file grows the new element |
| `payment/translate.go` | `ReturnMessage` fills `OrgnlTxRef`; new `ReadReturn` + `ReturnInstruction`; `StatementMessage` takes a reference rather than a cycle |
| `payment/types.go` | `SettlementAdvice.Reference`; `SettlementStatement.Reference`/`StatementRef`; `AdvisedMovement.Reference` |
| `payment/participant.go` | `ParticipantAccounts.ReturnsReceivable` and its doc |
| `payment/system.go` | `AddParticipantTx` opens the account; `ReturnPaymentTx` is deleted and replaced by `SettleReturnTx`, `PostReturnLegTx`, `ReverseReturnLegTx` |
| `payment/scheme.go` | `ReturnerOf` — the direction rule, moved out of `mesh` so the domain can apply it |
| `payment/errors.go` | `ErrReturnAlreadySettled`, `ErrNotAPartyToThisReturn` |
| `store/pg/schema/0001_init.sql` | `participant_assets.returns_receivable`; `settlement_advices.cycle_id` → `reference`; comments |
| `store/pg/tx_payment.go`, `store/mem/tx_payment.go`, `store/mem/mem.go` | the same two changes |
| `store/storetest/payment.go` | round-trip and scoping coverage for both |
| `mesh/ops.go` | `bankOps` and `settlementOps` gain the new methods; `ReturnPayment` leaves `settlementOps` |
| `mesh/csm.go` | `relayReturn` fans out — settlement agent, then the other bank |
| `mesh/centralbank.go` | `receiveReturn` reads the message, settles, advises both banks |
| `mesh/bank.go` | `returnPayment` posts before it sends; new `receiveReturn`; `receiveReturnStatus` unwinds on RJCT |
| `mesh/books_test.go` | `TestWhichBooksAReturnReaches` moves; a new per-bank counterpart |
| `README.md`, `web/src/components/hint-content.ts`, `web/src/lib/quiz/chapters/*.ts` | the domain content this adds |

---

## Task 16a: `OrgnlTxRef` on pacs.004, and a reader for it

The settlement agent must learn whose settlement accounts to move **from the
message**. Today it learns it by reading the payment row, which is exactly the
crossing this task exists to close.

**Files:**
- Modify: `iso20022/pacs004.go` (the `ReturnTransaction` struct at :175, its
  `validate`, and the "OrgnlTxRef is deliberately absent" ruling at :163)
- Modify: `payment/translate.go` (`ReturnMessage` at :973; add `ReadReturn` and
  `ReturnInstruction` beside `ReadSettlement` at :1502)
- Modify: `payment/doc.go` (:98 claims "reads all but pacs.004: `ReturnMessage`
  has no reading counterpart" — that stops being true here)
- Modify: `iso20022/testdata/pacs004.xml`
- Test: `iso20022/pacs004_test.go`, `payment/message_test.go`

**Interfaces produced:**

```go
// iso20022
type OriginalTransactionReference struct {
	DbtrAgt *BranchAndFinancialInstitution `xml:"DbtrAgt,omitempty"`
	CdtrAgt *BranchAndFinancialInstitution `xml:"CdtrAgt,omitempty"`
}
// ReturnTransaction gains, LAST, after RtrRsnInf — the schema's own order:
//	OrgnlTxRef *OriginalTransactionReference `xml:"OrgnlTxRef,omitempty"`

// payment
type ReturnInstruction struct {
	PaymentID     PaymentID
	EndToEndID    string
	DebtorAgent   iso20022.BIC
	CreditorAgent iso20022.BIC
	Amount        ledger.Amount
	Asset         ledger.AssetCode
	Reason        string
}
func ReadReturn(doc *iso20022.Pacs004) ([]ReturnInstruction, error)
```

- [ ] **Step 1: write the failing reader test**

In `payment/message_test.go`:

```go
// TestAReturnCarriesTheTwoAgentsItsSettlementNeeds is the whole of why
// OrgnlTxRef stopped being deliberately absent. A settlement agent with no
// payment rows learns whose reserves to move from this element or from nothing.
func TestAReturnCarriesTheTwoAgentsItsSettlementNeeds(t *testing.T) {
	n, p := networkWithSettledPayment(t)
	env, err := n.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "account closed",
		payment.MessageContext{From: "VERDDEFFXXX", To: "CSMXDEFFXXX", MsgID: "msg_1", Now: time.Now()})
	if err != nil {
		t.Fatalf("ReturnMessage: %v", err)
	}
	ins, err := payment.ReadReturn(env.Document.(*iso20022.Pacs004))
	if err != nil {
		t.Fatalf("ReadReturn: %v", err)
	}
	if len(ins) != 1 {
		t.Fatalf("got %d instructions, want 1", len(ins))
	}
	got := ins[0]
	if got.DebtorAgent != p.DebtorDetails.Agent || got.CreditorAgent != p.CreditorDetails.Agent {
		t.Errorf("agents came back %s/%s, want %s/%s",
			got.DebtorAgent, got.CreditorAgent, p.DebtorDetails.Agent, p.CreditorDetails.Agent)
	}
	if got.PaymentID != p.ID || got.Amount != p.Amount {
		t.Errorf("got %s for %d, want %s for %d", got.PaymentID, got.Amount, p.ID, p.Amount)
	}
}
```

Use whatever helper `payment/message_test.go` already has for a settled payment —
check the top of that file rather than inventing `networkWithSettledPayment`; if
none exists, build the payment the way `TestReturnMessageCarriesTheReturnReason`
(:388) does.

- [ ] **Step 2: run it and watch it fail**

`go test ./payment/ -run TestAReturnCarriesTheTwoAgents -v`
Expected: FAIL — `payment.ReadReturn` undefined.

- [ ] **Step 3: add the element to `iso20022`**

Add `OriginalTransactionReference` and the `OrgnlTxRef` field. Extend
`ReturnTransaction.validate` so that **`OrgnlTxRef`, when present, carries both
agents or neither** — a reference naming one side is a reference the settlement
agent cannot act on, and a half-filled element is worse than an absent one
because it looks usable.

Do **not** make it mandatory. The EPC does; this repository's `pacs004.go` header
records that this message is an EPC subset in identified respects, and a hard
requirement would make `TestAReturnThatNamesNoPaymentCannotBeAnswered`'s injected
message unconstructible.

- [ ] **Step 4: rewrite the ruling as a reversal**

`iso20022/pacs004.go:163`'s `# OrgnlTxRef is deliberately absent` must become a
reversal, not a deletion. It must say: implemented **on pacs.004 only**; the full
ruling stays on `pacs002.go`'s `PaymentTransactionStatus` and is *not* reversed
there; and the reason it moved — a settlement agent that holds no payment rows has
no other way to know whose reserve accounts a return moves. A reversed ruling that
looks like an oversight is worse than the original ruling.

- [ ] **Step 5: fill it in `ReturnMessage`**

`payment/translate.go:986`. Both agents come off the payment's own
`DebtorDetails.Agent` / `CreditorDetails.Agent` via the existing `agentOf` helper
(:335). Note the doc at :969 — "A pacs.004 names no parties: it refers to the
original payment by identifier" — is now **false** and is part of this step, not a
later sweep.

- [ ] **Step 6: write `ReadReturn`**

Beside `ReadSettlement` (:1502), following its shape exactly: the scale comes from
`ledger.LookupAsset` on the transaction's **own** currency, never from a constant.
Refuse a transaction whose `OrgnlTxRef` is absent or whose agents are empty —
`ReadSettlement`'s argument, one message over: a settlement instruction that
cannot be resolved to accounts must not be half-acted-on. Reuse `returnReason`'s
logic for `Reason` — if it stays in `mesh/centralbank.go:398` it will be applied
in two places that can drift, so **move it into `payment` and have the mesh call
through**, keeping its doc.

- [ ] **Step 7: run the test and watch it pass**

`go test ./payment/ ./iso20022/ -v`

- [ ] **Step 8: watch the guard fail**

Delete the "both agents or neither" check in `validate`, run
`go test ./iso20022/`, confirm the test written for it fails, restore. If it does
not fail, the test is not measuring the guard.

- [ ] **Step 9: update the golden file**

`iso20022/testdata/pacs004.xml`. Note in the commit message that
`TestGoldenFilesValidateAgainstTheSchema` **skips** — `iso20022/testdata/xsd/` is
absent for licensing reasons — so the element's position in the sequence is
checked by nothing. A skip is not a pass.

- [ ] **Step 10: full verification, then commit**

```bash
git add iso20022/ payment/
git commit -m "feat(iso20022,payment): a return names its two agents

Reverses the OrgnlTxRef ruling on pacs.004 and only there..."
```

---

## Task 16b: the `Returns Receivable` account

A bank forced to honour a refund it cannot fund out of the biller's account holds
a claim on that biller. It needs somewhere to put it before Task 16d can force
anything.

**Files:**
- Modify: `payment/participant.go` (`ParticipantAccounts` at :34, and the account
  list in `Participant`'s doc at :66-84)
- Modify: `payment/system.go` (`AddParticipantTx`'s per-asset loop, :443-469)
- Modify: `store/pg/schema/0001_init.sql` (`participant_assets`, :578)
- Modify: `store/pg/tx_payment.go`, `store/mem/tx_payment.go`
- Test: `store/storetest/payment.go`, `payment/system_test.go`

**Interfaces produced:** `ParticipantAccounts.ReturnsReceivable ledger.AccountID`.

- [ ] **Step 1: write the failing test**

In `payment/system_test.go`:

```go
// TestABankJoinsWithAReturnsReceivableAccount pins the account's CLASS as much
// as its existence. It is an Asset — a claim on a biller the bank paid out for —
// and not a liability like Unclaimed Balances, which is money the bank OWES.
// Getting that backwards would balance and mean the opposite.
func TestABankJoinsWithAReturnsReceivableAccount(t *testing.T) {
	// build a network, AddParticipant with []ledger.AssetCode{"EUR"}
	// accts, err := p.AccountsFor("EUR")
	// read the account out of the bank's own Ledger and assert:
	//   acct.Type == ledger.Asset
	//   acct.Name == "Returns Receivable (EUR)"
}
```

Follow the existing `AddParticipant` test in that file for the setup; do not
invent a harness.

- [ ] **Step 2: run it and watch it fail**

`go test ./payment/ -run TestABankJoinsWithAReturnsReceivable -v`
Expected: FAIL — no such field.

- [ ] **Step 3: add the field and open the account**

`payment/system.go`, beside `unclaimed` (:456):

```go
// An Asset, and the contrast with Unclaimed Balances two lines up is the
// point. Unclaimed is money the bank OWES to somebody it has not identified;
// this is money OWED TO the bank by somebody it has identified perfectly
// well — a biller whose account could not fund a refund the bank was obliged
// to honour anyway. Same event, opposite sides of the balance sheet.
returnsReceivable, err := bank.CreateAccountTx(ctx, tx, interbank.ID,
	"Returns Receivable ("+string(asset)+")", ledger.Asset, asset)
```

- [ ] **Step 4: run it and watch it pass**

- [ ] **Step 5: carry it through both stores**

`store/pg/schema/0001_init.sql`: a `returns_receivable TEXT NOT NULL` column on
`participant_assets`, with a `COMMENT ON COLUMN` saying **why it is an asset while
`unclaimed` beside it is a liability**. The schema comments are domain content
here, not implementation notes.

`store/pg/tx_payment.go` and `store/mem/tx_payment.go`: the read and write paths.

- [ ] **Step 6: extend the conformance suite**

`store/storetest/payment.go`, in whichever subtest round-trips
`ParticipantAccounts`. Then:

```bash
go test ./store/...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./store/...
```

- [ ] **Step 7: watch the store test fail**

Drop `returns_receivable` from the `INSERT` column list in `store/pg`, run the
Postgres suite, confirm the conformance test fails, restore. A column added
without a test that notices its absence is a column the other store can silently
diverge on.

- [ ] **Step 8: full verification, then commit**

---

## Task 16c: `SettlementAdvice` stops being about cycles

A return's statement names a **payment**. The row a bank writes to record having
booked it is the same row, so its key stops being a cycle id.

**Files:**
- Modify: `payment/types.go` (`SettlementAdvice` :431, `SettlementStatement` :463,
  `AdvisedMovement` :1725 in `translate.go`)
- Modify: `payment/system.go` (`SettleCycleTx` :947-962, `PostSettlementAdviceTx`
  :1126-1211)
- Modify: `payment/translate.go` (`StatementMessage` :1637, `ReadStatement` :1771)
- Modify: `payment/store.go` (the `Tx` interface's three advice methods)
- Modify: `store/mem/mem.go` (`adviceKey` :218-224), `store/mem/tx_payment.go`
  (:251-295), `store/pg/tx_payment.go` (:653-700),
  `store/pg/schema/0001_init.sql` (:784-836)
- Test: `store/storetest/payment.go` (:719, :1054-1195)

**Interfaces produced:**

```go
// SettlementAdvice.CycleID CycleID  ->  Reference string
// AdvisedMovement.CycleID  CycleID  ->  Reference string
// SettlementStatement.CycleID CycleID -> Reference string
// SettlementStatement.SettlementID SettlementID -> StatementRef string
func (t Tx) GetSettlementAdvice(ctx, book ledger.BookID, reference string, asset ledger.AssetCode) (payment.SettlementAdvice, error)
func (t Tx) PutSettlementAdvice(ctx, book ledger.BookID, a payment.SettlementAdvice) error
```

`StatementRef` is the account servicer's reference for the statement — `Stmt/Id`.
On the cycle path it is the settlement row's id, as today. On the return path it
is `<payment>:rtr`, and there is **no settlement row**, which is why the field
stops being a typed `SettlementID`: a typed id holding a value that is not a row's
key is a quiet lie, and `ReadStatement` deliberately does not surface it anyway
(see `AdvisedMovement`'s doc at `translate.go:1709`).

- [ ] **Step 1: write the failing store test**

In `store/storetest/payment.go`, beside the existing advice subtests:

```go
// AdvicesAreKeyedByReferenceNotByCycle: a bank's record of having booked a
// reserve movement is the same row whether the movement discharged a cut-off
// or a single return. Two advices for one bank in one asset, one referencing a
// cycle and one a payment, must both be readable and must not collide.
t.Run("AdvicesAreKeyedByReferenceNotByCycle", func(t *testing.T) {
	// Put {Book: "bank_2", Reference: "cyc_1", Asset: "EUR", ...}
	// Put {Book: "bank_2", Reference: "pay_9", Asset: "EUR", ...}
	// Get both back; assert Movement survives on each and they are distinct.
	// ListSettlementAdvices("bank_2") returns both.
})
```

- [ ] **Step 2: run it and watch it fail**

`go test ./store/... -run Advice -v` — FAIL, no such field.

- [ ] **Step 3: rename through the domain**

`CycleID` → `Reference string` on all three types. `SettleCycleTx` sets
`Reference: string(c.ID)` and `StatementRef: string(st.ID)`. The `AcctSvcrRef`
element in `StatementMessage` (:1677) already carries exactly this — its doc
("A member bank has no cycles … AcctSvcrRef is the servicer's own reference for
the entry, which is exactly what a cycle id is from the central bank's side") must
widen to say the reference is a cycle **or a payment**, and that a member bank has
no cycles *and no other institution's payment ids either* — both are opaque to it.

`PostSettlementAdviceTx`'s `ErrStatementNotForThisBank` check is unchanged and
must stay: it is ownership of the **account**, not of the reference.

- [ ] **Step 4: rename through both stores**

`store/mem/mem.go`'s `adviceKey.cycle` → `reference`. In `store/pg`, the column
`settlement_advices.cycle_id` becomes `reference` and the primary key follows.
**There is one migration** — no database is deployed — so edit `0001_init.sql` in
place rather than adding `0002_`.

The block at :818-820 says "No foreign key to cycles. A member bank HAS no
cycles". Keep the claim and widen it: there is no foreign key to **anything**,
because the reference names a row in an institution the member does not share a
database with, in either direction.

- [ ] **Step 5: run both store suites and watch them pass**

- [ ] **Step 6: watch the scoping test fail**

`store/storetest/payment.go:1167`'s misfiled-book subtest is the one that pins
that `PutSettlementAdvice` files by its **argument** and not by the struct's
`Book` field. Make the pg implementation key off `a.Reference` while ignoring the
`book` argument, run the suite, confirm it fails, restore.

- [ ] **Step 7: full verification, then commit**

---

## Task 16d: the domain splits into three acts

**Files:**
- Modify: `payment/system.go` — delete `ReturnPayment`/`ReturnPaymentTx`
  (:2151-2329); add `SettleReturn`/`SettleReturnTx`,
  `PostReturnLeg`/`PostReturnLegTx`, `ReverseReturnLeg`/`ReverseReturnLegTx`
- Modify: `payment/scheme.go` — add `ReturnerOf`
- Modify: `payment/errors.go`
- Modify: `mesh/mesh.go:1313` — `returnerOf` delegates to `payment.ReturnerOf`
- Test: `payment/system_test.go`

**Interfaces produced:**

```go
func ReturnerOf(scheme Scheme, debtor, creditor PartyRef) PartyRef

func (s *Network) SettleReturn(ctx context.Context, in ReturnInstruction) ([]SettlementStatement, error)
func (s *Network) SettleReturnTx(ctx context.Context, tx Tx, in ReturnInstruction) ([]SettlementStatement, error)

func (s *Network) PostReturnLeg(ctx context.Context, by ParticipantID, id PaymentID, reason string) (Payment, error)
func (s *Network) PostReturnLegTx(ctx context.Context, tx Tx, by ParticipantID, id PaymentID, reason string) (Payment, error)

func (s *Network) ReverseReturnLeg(ctx context.Context, by ParticipantID, id PaymentID, reason string) error
func (s *Network) ReverseReturnLegTx(ctx context.Context, tx Tx, by ParticipantID, id PaymentID, reason string) error

var ErrReturnAlreadySettled  = errors.New("this return has already been settled")
var ErrNotAPartyToThisReturn = errors.New("this bank is neither side of this return")
```

**What each does.**

`SettleReturnTx` is the **only** central-bank act, and it reads **no payment**.
From the instruction: resolve both BICs to participants (a `participantByBICTx`
helper over `tx.ListParticipants`, since none exists today), take each one's
`AccountsFor(in.Asset)`, check the **creditor bank's** settlement balance covers
the reversal — `SettleCycleTx:906-918`'s check, for its reason: a member's
settlement account here is a `ledger.Liability` and
`ledger.Book.checkSufficientBalance` guards only `Asset` and `Expense`, so nothing
else will refuse it — then post **one** transaction in `s.centralBank`:

```
Debit  creditorAccts.Settlement
Credit debtorAccts.Settlement
IdempotencyKey: string(in.PaymentID) + ":return-settle"
```

and return **two** `SettlementStatement`s, closing balances read after the posting
and inside the same unit of work, `Reference: string(in.PaymentID)`,
`StatementRef: string(in.PaymentID) + ":rtr"`.

A repeated instruction comes back from `PostTransactionTx` as
`ledger.ErrDuplicateIdempotencyKey`; wrap it as `ErrReturnAlreadySettled` so the
mesh can dead-letter it, which is what `ErrInvalidStateTransition` does today.

`PostReturnLegTx` is **both banks' act**, and which leg it posts follows from
which side `by` is on — neither the caller nor the message chooses:

- `by == p.Creditor.Participant` → **clawback**:
  `Debit p.CreditorLegAccount` / `Credit accts.Suspense`,
  key `string(p.ID) + ":return-claw"`.
  Fundability is `creditor.Deposit.CheckWithdrawalTx(ctx, tx, p.Creditor.Account, p.Amount)`
  — **only when the holding account is the payee's own**. When
  `p.CreditorLegAccount == accts.Unclaimed` there is no customer to check and the
  money is demonstrably there.
  If the check refuses: **on a push this bank is the returner**, so return the
  error and no message is ever built. **On a pull it is not**, so force it — post
  `Debit accts.ReturnsReceivable` for the whole amount instead, because the
  payer's eight-week refund right is unconditional and the creditor's bank bears
  the credit risk.
- `by == p.Debtor.Participant` → **refund**:
  `Debit accts.Suspense` / `Credit debtorGL`, key `string(p.ID) + ":return-refund"`,
  and it **closes the gap `ReturnPaymentTx`'s doc records at :2165-2198**: call
  `debtor.Deposit.CheckCreditTx` and divert to `accts.Unclaimed` on
  `deposit.ErrAccountClosed` — and **only** on that, for
  `PostCreditorLegTx:1313-1325`'s reason, which is that `glAccountTx` collapses
  every store failure into one value and money must not be routed on a failure
  nobody can tell apart. That gap has been open since before Task 15 and the split
  is what makes closing it affordable, exactly as it did on the creditor side.
- anything else → `ErrNotAPartyToThisReturn`.
- **The second leg sets `Returned`.** Detect it by reading the *other* leg's
  transaction id off the payment; the leg that finds one already there is the
  second. Record each leg's transaction id on the payment as it posts.

`ReverseReturnLegTx` undoes the returning bank's pre-post when the settlement
agent answers RJCT. Equal and opposite, key `…":return-unwind"`.

- [ ] **Step 1: write the failing measurement first**

`payment/system_test.go`. The central bank's act, in isolation:

```go
// TestSettlingAReturnReadsNoPayment is the crossing this task closes. The
// settlement agent is handed an instruction and moves reserves; if it can still
// do this by reading a payment row, nothing has been split.
func TestSettlingAReturnReadsNoPayment(t *testing.T) {
	// settle a payment, then build a ReturnInstruction by hand — NOT from the
	// payment row — and call SettleReturn with it.
	// assert: the two settlement balances moved, and the payment row is
	// untouched (still Settled, no return transaction ids on it).
}
```

- [ ] **Step 2: run it and watch it fail** — `SettleReturn` undefined.

- [ ] **Step 3: write `ReturnerOf` and delegate `mesh.returnerOf` to it**

Move the rule, keep the doc at `mesh/mesh.go:1298-1312` where the delegation
happens or move it with the function — but the rule must exist in exactly one
place, because Task 16d applies it in the domain and the mesh applies it to pick
an actor, and two copies will drift.

- [ ] **Step 4: implement `SettleReturnTx`, then watch the test pass**

- [ ] **Step 5: write the push-refusal test, and watch it fail**

```go
// TestAPayeeWhoSpentTheMoneyStopsTheReturnBeforeItIsSent is the push rule, and
// the reason the returning bank posts before it sends rather than after it is
// told. A check that is not a posting can be outrun.
func TestAPayeeWhoSpentTheMoneyStopsTheReturnBeforeItIsSent(t *testing.T) {
	// settle an SCT to bob; withdraw everything from bob's account;
	// PostReturnLeg(by: bob's bank, id) must return ledger.ErrInsufficientBalance
	// (or deposit's equivalent — assert on what CheckWithdrawalTx actually
	// returns, read it, do not guess), and NOTHING may be posted.
}
```

- [ ] **Step 6: write the pull-forcing test, and watch it fail**

```go
// TestAPullRefundIsHonouredEvenWhenTheBillerCannotFundIt is the other half of
// the same rule. The payer's eight-week right is unconditional, so the
// creditor's bank posts anyway and books a receivable.
func TestAPullRefundIsHonouredEvenWhenTheBillerCannotFundIt(t *testing.T) {
	// settle an SDD; empty the biller's account; PostReturnLeg(by: biller's
	// bank) must SUCCEED, and ReturnsReceivable must hold the amount.
}
```

- [ ] **Step 7: implement `PostReturnLegTx`, then watch both pass**

- [ ] **Step 8: write the debtor-side diversion test**

The gap `ReturnPaymentTx`'s doc has recorded since before Task 15: a payer whose
account is closed between settlement and the return. Assert the refund reaches
`Unclaimed` and the payment still reaches `Returned`.

- [ ] **Step 9: reduce `ReturnPaymentTx` to a transitional composition**

**Corrected during execution.** This step used to say "delete it; if the mesh does
not compile yet, that is expected and Task 16e fixes it". That was wrong: it
leaves the branch un-buildable between two tasks, so Task 16d could not be
verified or reviewed on its own, and neither could anything until 16e landed.

Instead, `ReturnPaymentTx` **keeps its name, its signature and its behaviour** and
becomes a composition of the three new acts inside one `Store.Update`: the
returning bank's leg, then `SettleReturnTx`, then the other bank's leg. It builds
the `ReturnInstruction` from the payment row, which is exactly the crossing this
task exists to remove — and that is the point of saying so in its doc.

Its doc must state, in the open, that it is **transitional**, that Task 16e
deletes it, and that its existence is the reason
`TestWhichBooksAReturnReaches` has not moved yet. A composition that does not
announce its own expiry reads as the intended design.

`mesh/centralbank.go:354` is therefore untouched here, and every existing return
test still passes unchanged — which is the real check that the three new acts
compose to what the atomic version did.

- [ ] **Step 10: watch every new guard fail**

Four mutations, each run and each restored:
1. delete the settlement-balance check in `SettleReturnTx` — a return that
   overdraws a bank's reserve must fail a test;
2. make `PostReturnLegTx` force on **both** directions — the push test must fail;
3. make it refuse on both — the pull test must fail;
4. widen the `deposit.ErrAccountClosed` check to `err != nil` — a test must catch
   money being diverted on a store failure. If none does, write it: this is the
   exact defect the first review round on Task 15 found, and it was found by
   somebody checking rather than by a test.

- [ ] **Step 11: full verification, then commit**

---

## Task 16e: the mesh conversation

**Files:**
- Modify: `mesh/ops.go` (`bankOps` :111, `settlementOps` :265 — and the doc at
  :260, which says "`ReturnPaymentTx` still posts in three books" and stops being
  true here)
- Modify: `mesh/csm.go` (`relayReturn` :173, `receiveReturnStatus` :883)
- Modify: `mesh/centralbank.go` (`receiveReturn` :334, `advise` :250)
- Modify: `mesh/bank.go` (`returnPayment` :204, `receiveReturnStatus` :552; add
  `receiveReturn`; `handle` :115 gains the pacs.004 case)
- Modify: `mesh/doc.go` (:277)
- Test: `mesh/books_test.go` (:1246-1330), `mesh/return_test.go`

**The order on the wire, which is the thing to get right:**

```
bank.returnPayment    PostReturnLeg (its own leg, into suspense) -> pacs.004
csm.relayReturn       -> central bank
centralBank.receiveReturn
                      ReadReturn -> SettleReturn
                      advise: camt.053 -> BOTH banks     (before the answer)
                      answer: pacs.002 -> csm
csm.receiveReturnStatus
                      ACSC: forward pacs.002 -> returning bank
                            relay pacs.004  -> the other bank
                      RJCT: forward pacs.002 -> returning bank only
bank.receiveReturn    PostReturnLeg (the other bank's leg) -> Returned
bank.receiveReturnStatus
                      RJCT -> ReverseReturnLeg
```

The camt.053 goes **before** the answer, for `centralBank.advise`'s existing
reason: the receiver's suspense must have moved before its customer leg draws on
it. Task 15's `TestTheMessagesACutOffPutsOnTheWire` states the rule; the
equivalent here needs the same **swap-plus-a-delay** mutation to fail
deterministically — a bare swap leaves a race the central bank usually wins.

The clearing house has to hold the pacs.004 until it has an ACSC to relay on,
because relaying it to the other bank before finality would have that bank post a
customer leg against a return that might be refused. That is new state in the
clearing house and it is the honest shape: it is the institution that knows both
ends of the conversation.

- [ ] **Step 1: move the measurement, and watch it fail**

`mesh/books_test.go:1322`:

```go
assertBooksTouched(t, "the central bank, settling a return",
	h.booksTouchedBy(h.cfg.CentralBankBIC), []ledger.BookID{payment.CentralBankBook, ledger.NetworkBook})
```

Run it against the **unchanged** mesh and watch it fail with the central bank
still reaching all four books. That failure is the signal this task has started.

- [ ] **Step 2: write the per-bank counterpart, and watch it fail**

```go
// TestEachBankBooksItsOwnReturnAndNoOtherBooks. Both banks now book during a
// return, so the old test's assertion that they touch NOTHING is what moves —
// and the name has to move with it. Task 15's
// TestEachBankBooksItsOwnSettlementAndNoOtherBooks was renamed for exactly this
// reason: a name that claims the opposite of its measurement is worse than no
// test.
```

The payee's bank reaches `[creditorBook]`; the payer's bank reaches
`[debtorBook, ledger.NetworkBook]` — but **measure it, then write the expectation
down**, do not copy these two sets from this plan. Task 15's handoff records that
`NetworkBook` arrives through the payment row's audit event and the id that event
needed, never through a posting, so which bank carries it depends on which one
writes the row that reaches `Returned`.

- [ ] **Step 3: delete the transitional composition, and rewrite `centralBank.receiveReturn`**

Task 16d left `ReturnPaymentTx` alive as a composition of the three new acts so
that the branch stayed buildable. Delete it here — it is the last thing in the
domain that posts in three books in one unit of work, and every caller is being
rewritten in this task anyway.

`ReadReturn` → `SettleReturn` → `advise` both statements → `answer`. Its long doc
at :268-333 is now wrong in its central claim ("`ReturnPaymentTx` posts THREE
compensating transactions in one unit of work … The price is the same one
`receiveSettlement` pays … two of those three postings land in member banks'
books"). Rewrite it from the code.

Keep the dead-letter discrimination and re-point it: `ErrReturnAlreadySettled`
replaces `ErrInvalidStateTransition` as the redelivery case, for the same reason —
it is a statement about this system's state, not about the sender's message.

- [ ] **Step 4: `bank.returnPayment` posts before it sends**

Its doc at :174-203 says "this one posts nothing at all, in either direction",
which is exactly what stops being true, and the paragraph explaining *why* it
posts nothing is the paragraph this task deletes.

A `PostReturnLeg` that commits and is then followed by a `send` that fails leaves
this bank's money in suspense against a return nobody will answer. That is the
same seam `submit` documents at :148-152 — return the error with the payment
beside it rather than swallowing it, and say so.

- [ ] **Step 5: add `bank.receiveReturn` and wire it into `handle`**

The other bank's leg, from the relayed pacs.004. It answers **nothing** — like
`receiveStatement`, and for a related reason: the return is already final. Say so
explicitly rather than leaving the absence to be inferred.

- [ ] **Step 6: `bank.receiveReturnStatus` unwinds on RJCT**

:552. Its doc says "nothing was posted anywhere, so the payment is exactly where
it was" — false as of Step 4, and the correction is the behaviour: call
`ReverseReturnLeg`. It stays not-a-dead-letter for the reason already written
there.

- [ ] **Step 7: `csm.relayReturn` and `csm.receiveReturnStatus` fan out**

:147-188 and :883. `relayReturn`'s doc explains at length that a pacs.004 "names
no parties at all" and that this hop "reads no element to route by and no store
either" — both change: it now holds the return until the ACSC, and it routes the
second hop by the agents `OrgnlTxRef` carries.

- [ ] **Step 8: `mesh/ops.go`**

`ReturnPayment` leaves `settlementOps`, `SettleReturn` joins it; `PostReturnLeg`
and `ReverseReturnLeg` join `bankOps`. These lists are mechanically verified — the
`var _ bankOps = (*payment.Network)(nil)` block at :282 — so a missed method is a
compile error, but a method left on the **wrong** list is not. Check each one
against which actor calls it.

- [ ] **Step 9: run the whole mesh suite, with `-race`**

`go test ./mesh/ -race`. `mesh/return_test.go` will have several failures that are
*correct* — every test asserting the atomic three-posting shape. Each one is a
decision: move the assertion, or delete the test and say in the commit message
what was lost. Do not delete quietly.

- [ ] **Step 10: watch the ordering guard fail**

Swap `advise` and `answer` in `receiveReturn` **and add a delay**, run, confirm
the ordering test fails, restore. A bare swap leaves a race; if the bare swap also
fails, the test is asserting something narrower than it claims and its doc must
say which.

- [ ] **Step 11: the known gap, recorded in the open**

`advise` returns on the first failing send, so a failed camt.053 to one bank
suppresses the answer for the whole return — the same defect `mesh/centralbank.go`
and `mesh/doc.go` already record for the settlement path, now reachable from a
second flow. Record it in both places rather than fixing it here; it is
unreachable only because this transport cannot lose a message.

- [ ] **Step 12: full verification, then commit**

---

## Task 16f: the documentation sweep

A correction is a sweep, not an edit — and Task 15's branch shipped a quiz chapter
that contradicted the branch's own headline concept because no task opened it.

**Files:**
- Modify: `README.md` (the returns section, and the settlement/finality section)
- Modify: `web/src/components/hint-content.ts`
- Modify: `web/src/lib/quiz/chapters/` — chapters 9 (clearing and settlement),
  11 (payment schemes), 12 (SEPA), 15 and 16 (persistence)
- Modify: `payment/doc.go`, `mesh/doc.go`, `store/pg/schema/0001_init.sql`
- Modify: `docs/superpowers/specs/2026-08-02-db-per-entity-design.md` (mark the
  crossing closed)

**The domain content this task adds, which no layer currently carries:**

1. A return is settled by the settlement agent and **booked by each bank
   locally**, so a return has an unreconciled position exactly as a cut-off does.
2. **The creditor's bank carries the refund risk on a direct debit**, which is why
   it vets its creditors and can demand collateral or an indemnity — and why
   `Returns Receivable` exists on one side and not the other.
3. A bank can **refuse** a return it would have to fund only when it is the
   returning bank; otherwise the refusal comes too late to matter.

- [ ] **Step 1: grep for every claim the code just falsified**

```bash
grep -rn "three books\|three compensating\|one unit of work" --include="*.go" --include="*.md" --include="*.ts" .
grep -rn "ReturnPaymentTx" --include="*.go" --include="*.md" --include="*.ts" .
```

Both must come back empty of *stale* hits. `ReturnPaymentTx` no longer exists; a
comment naming it is a comment about deleted code.

- [ ] **Step 2: `README.md` first — it is authoritative**

- [ ] **Step 3: `hint-content.ts`, distilled from what you just wrote**

If a new concept key is added, every `[[wiki-link]]` to it must resolve. A link to
a missing key throws at runtime under `RootLayout` and takes **every** route down
while `next build` stays green.

- [ ] **Step 4: the five quiz chapters**

Open all five, including any this change does not obviously touch. Check every
existing question against the new behaviour — a question that scored a true
statement correct may now be scoring a false one.

**Chapter 9 is at 22 questions, the maximum `diversity.test.ts` allows.** A new
question there means replacing one, not adding one.

- [ ] **Step 5: `npm run test`, then load a page**

```bash
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

`npm run test` catches broken wiki-links in hint bodies *and* in quiz
explanations, which the runtime guard does not scan.

- [ ] **Step 6: take a screenshot**

**No test in this repository can catch a web rendering regression.** Run the app,
load the affected pages, and look at the screenshot.

- [ ] **Step 7: mark crossing 4 closed in the spec**

The crossings table's `ReturnPaymentTx` row. Three of five closed after this task;
`ResolveIdentifierTx` (Task 18) and `AddParticipantTx` (Task 17) remain.

- [ ] **Step 8: full verification, then commit**

---

## Before merging

- [ ] **Run the verification block yourself.** Not a subagent. On Task 15's branch
      a subagent reported a Postgres run it could not have made — it had no
      `TEST_DATABASE_URL` in its environment and the suite passed by skipping.
- [ ] **Request a whole-branch review, not only per-task reviews.** Task 15's
      Critical money bug — a return clawing back from an account that never
      received the money — was created by one task and left unhandled by another,
      which is a shape per-task review cannot see by construction. It was found by
      a reviewer that built a probe and ran it rather than reading. Budget for it.
- [ ] **Write the handoff** to `docs/superpowers/handoff-<date>-task-16.md`,
      including every defect and deferral carried into `main`.

## Known risks, named in advance

- **The clearing house holding the pacs.004 until the ACSC is new state.** If it
  is held in memory it does not survive a restart, and a return would settle at
  the central bank with one bank's leg never posted — a payment stuck part-way
  with no route out, which is the shape Task 15 carried into `main` for
  `Cleared`-after-`Settled`. Decide deliberately and write the decision down.
- **`CheckWithdrawalTx`'s signature is confirmed**
  (`deposit/register.go:1252`, `(ctx, tx, id, amount)`); **which error it returns
  for a spent account is not.** Read it before Step 5 of Task 16d and assert on
  the value it actually returns rather than on the one this plan guesses.
- **The second-leg-sets-`Returned` rule depends on reading the other leg's
  transaction id off the payment.** Under one store both banks see one row and
  this works. Under Task 18's split they will not, and Task 18 must carry it.
  Say so where the rule is implemented.
