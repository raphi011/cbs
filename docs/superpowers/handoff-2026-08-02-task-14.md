# Handoff — after sub-project 8 Task 14, merged to `main`

`main` is at `5eac9d4`, 21 commits ahead of `a050287`. Everything below was verified
against the code at that commit, not against a plan. The full Go suite, the `-race`
suite, the Postgres suite and the web suite were all green at the merge commit.

## Read this first

`docs/superpowers/specs/2026-08-02-db-per-entity-design.md` is the sub-project's design
and is now on `main`. It records four decisions taken before any code, three rulings
this sub-project reverses, and the five crossings the split has to close. Task 14 closed
one of them.

## What Task 14 did

**A submitting bank no longer reads the counterparty bank's deposit register.**
`payment.partyTx` is gone; `partiesOf` is a package function that reads nothing, so
building an outbound message performs **no I/O at all**. The payee's **name** travels on
`InitiatePaymentRequest`; the routing **BIC** is derived from the roster.

The measurement moved, and every move was watched failing first:

| test | was | is |
|---|---|---|
| `TestWhichBooksEachBankActuallyReaches` | payer's bank `[debtor, creditor, Network]` | `[debtor, Network]` |
| `TestWhichBooksEachBankReachesInAPull` | submitting payee's bank `[debtor, creditor, Network]` | `[creditor, Network]` |

`assertBooksTouched` still compares whole sets, so these say what the bank did **not**
reach, not merely what it did.

## The crossing count: one closed, four open

The handoff before this one listed three domain problems. Reading the code for the spec
found **five**. Task 14 closed the first.

| # | crossing | state |
|---|---|---|
| 1 | `partyTx` reads the counterparty's register for a name | **CLOSED** |
| 2 | `ResolveIdentifierTx` sweeps every member's register | open — Task 18 |
| 3 | `SettleCycleTx` posts in every book, one `Store.Update` | open — Task 15 |
| 4 | `ReturnPaymentTx` posts in three books | open — Task 16 |
| 5 | `AddParticipantTx` writes into `CentralBankBook` | open — Task 17 |

Crossings 2 and 5 were **not** in the previous handoff's list. Admission is a
cross-entity atomic write, and the return cannot survive isolation at all until
`OrgnlTxRef` exists on pacs.004 — a pacs.004 names no parties, so a central bank with no
payment rows cannot tell whose settlement accounts to move.

## Two things Task 14 does NOT do, by design

- **The receiving bank's book set is still `h.bankBooks()`.** It resolves only its own
  customer now — which retires an AC01 that was never its to make, since a debtor IBAN
  nobody holds is a statement about the *sending* bank's customer — but
  `ResolveIdentifierTx` still lists every participant and reads every register.
  Narrowing the sweep needs a bank identity `payment.Network` does not have. **What
  changed is which PARTY is resolved, not which registers are searched.** Do not write
  that a bank resolves "against its own register"; five fix rounds went on that sentence.
- **`GET /directory` still returns the payee's name**, read off that account's bank's
  register (`api/handlers_directory.go`). Task 14 closed the crossing on the
  **message-building** path only. Removing `Name`/`Asset` from `directoryEntryDTO`
  belongs with the sweep, in Task 18.

## The regression the whole-branch review caught, and the ruling

Per-task reviews could not see it: after the counterparty's BIC moved onto the request,
it was **payer-supplied, validated for BIC format only, and never compared to the named
participant's real BIC** — while the clearing house *routes* on it (`mesh/csm.go:122`).
Before Task 14 `partyTx` read the real participant, so `CdtrAgt` could not disagree.

Measured with mesh probes, not argued:

- push carrying the payer's own BIC as the creditor agent → payment reaches **Accepted**,
  payer's bank touches `[bank_1 bank_3 network]` — the exact set the branch moved off.
- pull carrying the collector's BIC → **the collecting bank posts the debit in the
  payer's bank's book**, and the payer's bank never sees the collection.

**Ruling: the agent is derived from the roster entry for the named participant; only the
NAME stays asserted by the payer.** That is what real SEPA does — IBAN-only since 2016,
the originating bank derives routing and never trusts a payer-supplied BIC — and it is
what the spec already said: *routing needs the bank, not the name*. The send form no
longer asks for a BIC. `tx.GetParticipant` is network-scoped and is not a `recordingTx`
override, so the roster read records no book access and the measurements did not move.

`TestSubmitDerivesTheCounterpartyAgentFromTheRoster` pins it, and the fixture banks were
given **distinct BICs** (`testBIC` / `testBIC2`) because with a shared BIC the test could
not fail — measured, not assumed.

## A pre-existing hazard, now fixed

`checkPartyTx` flattened **any** `GetParticipant`/`GetDepositAccount` error into
`ErrParticipantNotFound`/`ErrAccountNotInParticipant`. It runs inside `AcceptInboundTx`,
whose errors reach `ReasonFor`, so **a dropped database connection at the receiving bank
reported AC01 to the sender** — telling another bank its customer's IBAN was bad when it
was not — and on a push the payer's debit was then reversed. It now mirrors
`addressedPartyTx`: only a genuine not-found becomes a domain sentinel. Pinned by
`TestAcceptInboundDoesNotBlameTheSenderForAStoreFailure`, whose fixture decorates
`Update` rather than `View`, because that is the path this call sits on.

## Known defects and deferrals carried into `main`

- **`iso20022.ErrMissingElement` has no `api/errors.go` entry**, so a request reaching
  message building with a missing mandatory element surfaces as a bare 500. The reachable
  door is closed by the counterparty guard; the residual is a bank's own register holding
  a nameless account.
- **Two of three refusal subtests in `TestPostPaymentRequiresTheCounterpartyName` pin
  only "refused by something"** — an empty agent is caught by BIC validation regardless.
  The guard itself is pinned exactly at the domain layer, and the test says so.
- **`InitiatePaymentRequest.Agent` is ignored by `SubmitPaymentTx`** but kept because the
  inbound translators produce the same type from a received message where the agent is
  real data. Splitting the type is the eventual fix.
- **~45 `PartyDetails{Agent: …}` fixture literals remain** in `payment/*_test.go`,
  `store/storetest` and `store/pg/pg_test.go`. They set correct values and assert nothing
  false.
- **`docs/superpowers/plans/` and `specs/` carry pre-ruling wording.** Dated process
  artefacts, deliberately not rewritten. `docs/expansion-roadmap.md` carries supersession
  annotations in its own style.
- Everything the previous handoff listed that Task 14 did not touch is still true,
  including that **no golden file has ever been schema-validated here** — `make
  test-schemas` fails and `TestGoldenFilesValidateAgainstTheSchema` skips every subtest.
  A skip is not a pass.

## The process lesson, which cost the most

**Five of six sub-tasks needed a fix round, and in every case the Go was right on the
first attempt and the prose was wrong.** Task 14.6 alone took five rounds. The cause was
structural, not careless: a reviewer names one false sentence, the fix corrects that one,
and the correction makes the *next* instance visible — because duplicated documentation
hides a shared error until something forces the layers apart.

Round 1 fixed `hint-content.ts` and exposed `README.md:865`. Round 2 fixed that and the
quiz, exposing `README.md:845`. Round 3 fixed that, exposing the schema comment. Round 4
swept every layer at once, found 43 candidates, fixed 8 — including one nobody had
pointed at — and its audit still found one more. Round 5's own confirming grep found two
further instances in Go **test** files, which no sweep had covered.

**So: a domain-claim correction is a sweep, not an edit.** When a review finds a false
claim in one layer, grep every layer for every phrasing of that claim in the same pass —
`README.md`, `hint-content.ts`, all 18 quiz chapters, `0001_init.sql` comments, and Go/TS
comments **including test files** — and report the hit table as evidence. Tasks 15–19
touch these same layers repeatedly; put that step in the plan rather than letting review
find it five times.

The other rule that keeps earning its place: **a test whose failure nobody has observed
is not evidence.** This branch shipped three tests that could not fail and caught all
three by mutation — deleting the guard and watching the named test stay green. Two were
caught in review, one only in the final whole-branch pass.

## What the next sub-project is

**Task 15 — settlement becomes a conversation.** The largest domain change in the
sub-project and the one the previous handoff called the hardest item.

`SettleCycleTx` currently posts the central bank's netting transaction, a mirror leg in
**every** member's ledger, and a creditor leg in **every** payee's ledger, in one
`Store.Update`. After Task 15:

1. the clearing house nets the batch and sends the pacs.009;
2. the central bank checks each net payer's reserve and posts **only its own netting
   transaction**, answering ACCP or RJCT/AM04 — and is **final** either way;
3. each bank posts its mirror leg and its creditor legs **locally**, on advice.

The interval between 2 and 3 is the **unreconciled position**, and it is real: settlement
is final at the central bank and participants catch up afterwards. A bank's clearing
suspense is where it shows.

Two consequences worth knowing before planning it:

- **The unclaimed-balances account falls out of this**, rather than being a separate
  feature. Once a bank posts its own creditor legs, a payee's closed account fails **one
  payment at one bank** instead of the whole batch — so `CheckCreditTx` can finally be
  called at settlement. The rulings at `payment/system.go`'s `SettleCycleTx` and
  `ReturnPaymentTx` docs stop being rulings and become a fix.
- **`TestWhichBooksTheCentralBankReachesWhenItSettles` is the tripwire.** It asserts
  `h.allBooks()` today and must drop to `[CentralBankBook, Network]`. **That failure is
  the signal Task 15 has started, not a regression** — the same contract Task 14 had with
  the two measurements it moved.

Task 19 adds camt.053 and the reconciliation that makes a mis-booked position detectable
in-system. Until then, cross-store divergence is detectable only by the test suite.

## Verification, exactly

```bash
gofmt -l . && go vet ./... && go build ./...
go test ./...                                                            # store/mem
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

Postgres is local Homebrew — **`make test-pg` uses Docker and does not work here.** The
Postgres run is not optional: Task 14 added four columns and a `storetest` conformance
subtest, and `store/pg` must never accept or refuse a write differently from `store/mem`.

## The rules that cost the most when forgotten

- A `[[wiki-link]]` to a key missing from `hint-content.ts` throws at runtime and takes
  **every** route down while `next build` stays green. `npm run test` catches it in hint
  bodies *and* quiz explanations. Run it, and load a page.
- **No test in this repository can catch a web rendering regression.** Require a
  screenshot for web changes, and look at it. On this branch the screenshot is what
  revealed that the send form displayed the resolved payee name one line above the field
  asking the payer to type it — the UI teaching the negation of the fact the branch
  existed to establish.
- `rm -rf web/.next` before `npm run typecheck`.
- Never hand-write a backend path; use `cb()` / `csm()` / `bank(pid, …)`.
- `web/src/lib/quiz/diversity.test.ts` holds each chapter to 18–22 questions, ≥8 distinct
  `concept` tags, no tag more than 3×, and all three difficulty tiers. Chapter 12 is at
  21 with three tags already at the limit of 3.
- Documentation is domain content. `README.md` is authoritative; `hint-content.ts`, the
  quiz chapters and `0001_init.sql`'s comments duplicate it **by design** and must agree
  with it and with the code.
