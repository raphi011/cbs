# Handoff — Task 18a is complete, `spec/sqlite-only-store`

**All four of 18a's crossings are closed and nothing is committed.** 53 files
modified plus 7 untracked, +3855/−1122, on top of `40f188c`. Everything below was
verified against the working tree, not against a plan.

This supersedes `handoff-2026-08-06-task-18a.md`, which is still in the tree and
describes the halfway state. **Delete it with the commit** — two handoffs for one
task is how the wrong one gets read.

The task was **18a — the three crossings close, on one store**; its plan is
`docs/superpowers/plans/2026-08-06-task-18-split-the-stores.md`. Read that plan's
*Global constraints* before starting 18b; this document does not restate them.

## Verification, all run by the controller after the last change

```bash
gofmt -l . && go vet ./... && go build ./...                                # clean
go test ./... -count=1                                                      # 12/12 ok, ~40s
go test ./store/sqlite/ -count=10 -run 'TestRaces|TestConcurrentTxRaces'    # ok, 2.8s
go test ./mesh/ ./api/ ./cmd/... ./store/sqlite/ -race -count=1             # ok, ~4m
cd web && rm -rf .next && npx tsc --noEmit && npm run lint && npm run test  # 180 tests, 13 files
make test-schemas                                                           # 0 skips, 44 PASS
```

The `-race` run takes about **four minutes** — mesh 45.6s, api 174.9s,
cmd/server 12.1s, store/sqlite 12.6s. Run it in the background rather than
concluding it hung.

**The app was driven in a browser**, which the previous handoff correctly flagged
as not done. See *What was driven* below.

## The XSDs are now present, and that changed the task

`iso20022/testdata/xsd/` gained **`camt.050.001.05.xsd` and
`camt.025.001.05.xsd`**, downloaded through Chrome from the ISO 20022
**archive** — both are superseded versions, so they are not on the
message-definitions page. `testdata/README.md` now records the archive URL, that
it takes a `?search=` query, and that `camt.051.001.05` sits one row below the
one this repository wants.

So the two new messages are **schema-validated rather than carefully read**, and
`make test-schemas` is **0 skips / 44 PASS** (was 32). The directory is
gitignored, so the schemas are not in the commit and a fresh worktree still needs
them.

`web/node_modules` is not in git either. Docker does not work on this machine and
nothing needs it.

## The two decisions this task was given, and who made them

Both were asked of the user before code was written. Both stand:

1. **The lodgement is a real ISO conversation: camt.050 + camt.025.**
2. **The clearing house's directory became the roster.**

## What 18a landed

18a.1, 18a.2, 18a.3 and 18a.1b are unchanged from the previous handoff and are
not re-described here. What is new:

### 18a.4 — `DepositTx` stops posting in `CentralBankBook`

`BankAccounts` gained `VaultCash`, created in `FoundBankTx` per asset. `DepositTx`
posts `Debit Vault Cash / Credit customer` in one book and reads no account
outside it.

- **A third subledger, `Treasury`.** Vault cash is neither a customer deposit nor
  an interbank position. It is created **after** the other two on purpose:
  subledgers are numbered in creation order, so appending left every existing
  account number where it was.
- **`bank_assets` gained a `vault_cash` column**, with its argument inside the
  statement's parentheses.
- **The settlement guard MOVED rather than being deleted.**
  `accts.Settlement == ""` used to refuse a *deposit* with
  `ErrSettlementMemberNotFound`; it now refuses a *lodgement*. That closes the
  known defect "the deposit refusal says 'is founded and not yet admitted', false
  in the partly-admitted state" **by deletion** — the imprecise sentence is gone
  with the guard rather than fixed in place.
- **The ledger already refuses lodging cash a bank does not hold.** Vault Cash is
  an Asset and `ledger.Book` guards Assets against going negative, so
  `LodgeReservesTx` deliberately makes no check of its own —
  `ErrInsufficientBalance` → AM04 via `borrowedReasons`.

`TestFundingAReserveReachesTwoBooks` was **watched failing on exactly its two
crossing assertions** (the book set shrank to `[bank_1]`; zero transactions in
the central bank's book) and then rewritten as
`TestTakingCashInReachesOnlyTheBanksOwnBook`, which asserts the crossing is gone
three ways — book set, transaction count, **and the vault balance**, because the
first two are satisfied by a deposit that posts nothing at all.

### 18a.5 — the lodgement is a conversation

`bankOps.LodgeReserves` + `settlementOps.ReceiveLodgement`, `Mesh.Lodge`,
`POST /lodgements` (**202**), a `LodgeReservesForm` on the bank's reserves card,
and the seed lodges after funding.

**The message shape forced the design, and this is the paragraph to read before
changing it.** A `camt.025` carries **no amount** — Receipt3 has no element for
one. So a bank cannot post its own leg *from* the receipt, and the only
alternatives are to post before sending or to remember the outstanding request in
the actor. The second is `csm.held`'s shape, whose known defect is that it does
not survive a restart. **So the bank posts first**, exactly as `Mesh.Submit` does,
and `TestTheReceiptCarriesNoAmount` pins the absence so that a field added to
`ReceiptDetails` fails a test rather than quietly invalidating the argument.

`TestALodgementIsTwoBooksInTwoUnitsOfWork` is the measurement that says the
crossing *closed* rather than merely moved: the lodging bank reaches its own book,
the central bank reaches its own, and there are **exactly 2** units of work —
probed by widening the assertion to `< 99` and confirming it reported 2.

**One residual, and it is honest rather than hidden.** A *refused* camt.025 leaves
the bank's reserve mirror overstated, and 18a cannot unwind it because the amount
is not on the receipt. What makes it unreachable is the asking-side guard:
nothing writes a bank's settlement reference before the agent's account exists, so
a bank able to lodge is one the agent holds an account for. It is logged at ERROR
with that reasoning, and 18e's reconciliation harness is the instrument that would
find it. **Carried as a known defect below.**

### 18a.6 — the documentation sweep

Done by the expensive method, and it earned it. Grepping for the **deleted**
sentence rather than the written one found **eleven** further live copies of "a
deposit raises the bank's reserve" and **four** of "the directory sweeps every
bank" that reading the obvious files had missed.

Files read in full: `README.md` (the addressing, admission and settlement
sections), `web/src/components/hint-content.ts`, `payment/doc.go`,
`payment/bank.go`, `payment/types.go`, `mesh/ops.go`, `mesh/centralbank.go`,
`mesh/bank.go`, `api/surface.go`, `api/handlers_participant.go`,
`store/sqlite/schema/0001_init.sql` (the `banks`, `bank_assets` and `payments`
statements), `iso20022/doc.go`, `iso20022/testdata/README.md`, `web/CLAUDE.md`,
`docs/cbs-vs-book.md`, `docs/expansion-roadmap.md`.

Worth knowing:

- **`README.md` gained a section**, *Vault Cash and the Lodgement*, and every
  anchor in the file was validated by script — 0 broken.
- **Two new hint keys**, `vault-cash` and `lodgement`. Both are referenced from
  `hint=` props, so a missing one would have taken every dev route down; both were
  rendered in a browser.
- **A quiz question's correct ANSWER changed**, not just its explanation:
  `ch11-q16`'s option 1 said a founded bank cannot fund a customer account.
- **`web/CLAUDE.md`** said funding raises the reserve in step. That is instructions
  to future agents, so a wrong claim there propagates.
- **`docs/expansion-roadmap.md`'s log was ANNOTATED, not rewritten.** Its entries
  are dated records; the convention in that file is a `*Superseded by…*` note, and
  the 2026-08-01 entry's prediction that "Task 18 is what would close it" now
  carries one saying it did.
- **`docs/cbs-vs-book.md` needed nothing.** Its item 5 is about `CHECK`
  constraints and is unrelated to 18a — the previous handoff listed it as a
  carried defect and it stays one.

### 18a.7 — verification, and what was driven

**Two stale-binary lessons worth carrying.** `POST /lodgements` returned 404 in
the browser while passing in tests: `make dev` was holding a `go run` build that
predated the route. Restarting fixed it. **If a route 404s in the app and passes
in `go test`, restart the backend before believing the code is wrong.**

Driven in a browser against `make dev`, with no console errors on any route:

| driven | result |
|---|---|
| a deposit over HTTP | reserve **unchanged** at €2,550.00, deposits up — the crossing is gone end to end |
| the Lodge dialog, €500 | reserve €2,550.00 → **€3,050.00**, exactly +500 |
| the send form, correct BIC | **Accepted**, cross-bank, balance −25.00 — this is what pins 18a.1b's `resolveOwnPartyTx` |
| the send form, **wrong** BIC | **Rejected `AC01`**, "account does not belong to participant: IT60VERDE2001", payer refunded +10 in their own statement |
| the `vault-cash` hint | renders with its table and related-concept chips |

**The wrong-BIC case is the whole safety argument for 18a.1** and it is now
observed rather than reasoned: a payer who types the wrong bank's BIC does not
divert the money, because that bank holds no such address.

## Corrections to the Task 18 plan, each measured

The previous handoff's four corrections stand. Two more:

| the plan says | the code says |
|---|---|
| 18a is four bullets | It is four bullets **plus a schema change**. `bank_assets` needed a `vault_cash` column, so "nothing about the store changes here" is not quite true — the shape changed, the backend did not |
| — | **`iso20022` gained a test-only fix.** `TestAnyBICAndBICFIShareOneLexicalSpace` errored on a schema declaring only ONE of the two BIC types. camt.050 and camt.025 legitimately declare only `BICFI` — neither names an organisation — so "declares one" is now passed over like "declares neither". **Ten of twelve schemas still declare both**, so the claim is checked exactly as hard as before; skipping is provably safe because a type a schema does not declare is one no element in it can have |

## What is left: 18b, and it is next

18b — **`payment.Network` gains an identity** — is unstarted. Its plan bullets are
unchanged, and 18a made two of them concrete:

- **`ResolveIdentifierTx`'s `by ParticipantID` argument is the one 18b removes.**
  It is threaded through `localPartyIn`, `addressedPartyTx`,
  `CreditTransferRequest`, `DirectDebitRequest`, `AcceptInbound` and
  `resolveOwnPartyTx`, and `bank.pid` is what passes it. `mesh/bank.go`'s `pid`
  field documents itself as "a loan and not the answer".
- **`LodgeReserves` and `ReceiveLodgement` take an argument for the same reason.**
  `LodgeReserves(ctx, by, asset, amount, mc)` should lose `by`, and
  `ReceiveLodgement` should stop being reachable on a network that is not the
  settlement agent's.

Then 18c (three shapes), 18d (the wiring), 18e (the harness and the sweep).

## Rules that still hold

- **There is one migration.** `store/sqlite/schema/0001_init.sql`, edited in
  place. It dies at **18c**, not before.
- **Nothing cross-checks the SQL.** `store/mem` and `store/pg` are both gone.
- **The ephemeral store hides read-then-write defects.** Anything measured about
  ordering opens a file. `TestTheRetryBudgetOutlastsASlowWriter` is the one that
  does.
- **Two of the nine races do not bite on one run** —
  `ConcurrentAdmissionsAgreeOnOneCentralBank` and
  `ConcurrentMarkReversedOnlyOneWins`. `-count=10` for anything resting on either.
- **A `[]byte` is a BLOB and a `STRICT TEXT` column refuses it (3091).**
- **`sqlite_master` holds only comments inside a statement's parentheses.**
- **`iso20022/doc.go` opens with a description and no message count.** It says
  "Six." in no version any more; do not increment a number back into it.

## Reversals carried for 18e's list

18e writes five rulings up as reversals. 18a adds to it:

1. **The counterparty's BIC is asserted, not derived** — reverses Task 14. (The
   previous handoff's sixth entry.)
2. **A founded bank CAN take a deposit** — reverses the refusal `DepositTx` made.
3. **The camt family's absence** was "camt.053 only"; it is now **camt.053,
   camt.050 and camt.025**. `iso20022/doc.go`'s reversal section is rewritten and
   records why camt.051 and camt.054 are still refused *specifically*.
4. **`GET /directory` resolves across the network** — reversed in four layers.

## Known defects and deferrals carried

Unchanged from the previous handoff except where marked:

- **A partly-completed multi-asset admission cannot be re-driven.** Task 19's.
- **`csm.held` does not survive a restart.**
- **A cycle that nets to zero strands every payment in it, for ever** — still the
  most serious thing nobody owns.
- **An on-us payment has nowhere to go.**
- ~25 positional "option C/D" references in quiz explanations.
- `iso20022`'s `GenericAccountIdentification1/Id` presence-only validation.
- **`docs/cbs-vs-book.md` item 5 is half closed.**
- **`CreateMandateTx` reaches both banks' books**, on the clearing house's port.
  Not in the spec's crossing table; **18a did not close it.**
- **`resolveOwnPartyTx`'s unaddressed-party fallback.** 18d deletes it.
- **NEW: a refused camt.025 leaves the lodging bank's reserve mirror
  overstated.** Unreachable behind `LodgeReservesTx`'s guard; logged at ERROR;
  18e's harness is what would detect it.
- **NEW: a cross-bank address collision is no longer observable by anything.**
  Two customers at two banks may hold one address and nothing in the system can
  notice, because no reader spans two registers. Recorded in `README.md` and
  `hint-content.ts` rather than fixed — fixing it needs a scheme-level proxy
  lookup, which this system deliberately has none of.
- **NEW: `camt.025`'s `StsCd` is an unverifiable code.** `Max4AlphaNumericText`
  with no enumeration in the XSD, so `xmllint` accepts anything. The values used
  are `ACSC`/`RJCT`, reused from `TransactionStatus` rather than invented. Same
  unpaid debt `BankTransactionCode` carries; recorded on
  `iso20022.RequestHandling` and in `testdata/README.md`.

## What has NOT run on this branch

**No whole-branch review**, and this is now 17 commits' worth of change plus an
uncommitted 53-file one. Task 15's whole-branch review found a Critical money bug
that per-task review structurally cannot see; Task 17's found four. **It should
run before this merges.**

The pacs family's and camt.053's builders still have no
`ValidateAgainstTheSchema` coverage — `payment/schema_test.go` covers the three
acmt messages and the two camt lodgement ones, and says so. Closing it for the
rest is a sweep of its own.
