# Handoff — after sub-project 7b (ISO 20022 mesh), merged to `main`

`main` is at `7321d83`, 58 commits ahead of the previous `10df781`. Everything below was
verified against the code at that commit, not against a plan.

## What `main` now is

A core banking system whose institutions talk in **ISO 20022 messages over a mesh of actor
goroutines** — one per member bank, one clearing house, one central bank, each with an
unbounded inbox. **Only bytes cross an actor boundary**, so malformed input is a reachable
failure mode and FF01 is testable. Four flows run through it:

| flow | shape |
|---|---|
| credit transfer | payer's bank → CSM → payee's bank → CSM → payer's bank (4 hops) |
| direct debit | payee's bank submits → CSM → payer's bank posts the debtor leg on receipt |
| settlement | the cut-off emits `pacs.009`; the central bank settles or answers RJCT/AM04 |
| return | the bank that received the original → CSM → central bank, answer back (4 hops) |

`payment`'s monolithic operations are split into per-actor halves (`SubmitPaymentTx`,
`AcceptInboundTx`, `AcceptAtCSMTx`, `RejectAtCSMTx`, `ReverseDebtorLegTx`). `api` and
`cmd/server` drive the mesh; the `-entity` flag and `POST /settlements` are gone;
`POST /payments` answers **202**, and the payment is `Initiated` when the response is written.

## The properties that are load-bearing and that nothing enforces

1. **`Drain` FIRST, then `Stop`.** `Stop` closes every inbox before joining any actor, so it
   *cuts* in-flight chains — debtor debited, `pacs.002` never sent. Correct at both call sites
   and heavily documented, but it is convention, not enforcement. Worth moving inside `Stop`.
2. **The send must stay outside the unit of work.** Pinned by
   `TestARolledBackSubmitSendsNothing`. An enqueue inside a transaction schedules work against
   state a rollback may remove, and with an unbounded queue it would not even deadlock.
3. **A submitting bank reaches the counterparty's book.** `payment`'s `partyTx` reads the
   counterparty's deposit register to name them in the message, so *"no bank reaches another
   bank's book" is false today*. Measured, documented in `mesh/books_test.go` and `mesh/doc.go`.
   Closing it needs the payee's **name** to travel on `InitiatePaymentRequest`.
4. **The narrowed interfaces narrow what a handler can NAME, not what it can do.**
   `GetParticipant` returns a `*payment.Participant` whose `bind()` attaches live book-scoped
   handles. The book recorder in `mesh/books_test.go` is what actually bites.
5. **"One decision, one message" is not a property of this mesh.** A rejected pull sends two
   `pacs.002` *when the payer's bank has already posted the debtor leg* — the bank waiting for
   the answer and the bank holding the money are different institutions.
6. **No test in this repository can catch a web rendering regression.** `vitest` is node-only
   and `.ts`-only. A screenshot caught a real defect during 7b that a page-text transcript
   provably could not. Require screenshots for web changes, and look at them.
7. **No golden file has ever been schema-validated here.** `iso20022/testdata/xsd/` is absent
   for ISO licensing reasons; `TestGoldenFilesValidateAgainstTheSchema` skips every subtest and
   `make test-schemas` fails. Stated in `README.md`, `payment/doc.go` and the roadmap. A skip is
   not a pass.

## Known defects carried into `main`, with rulings

- **Money can be credited into a `Closed` account.** `ReturnPaymentTx` credits `debtorGL` and
  `SettleCycleTx` credits `creditorGL` with **no `CheckCreditTx`**, where `creditorSideTx` and
  `DepositTx` both call it. Reproducible in four steps. **Both remedies are design rulings**:
  refusing the return strands the money with the payee for ever; refusing at settlement fails
  the whole batch and recreates the terminal state that `POST /cycles/{cid}/settle` exists to
  clear — in a form that route *cannot* clear, because funding is fixable and a closed account
  is not. Documented at `payment/system.go:1681-1721` and `:875-881`. **The design answer is an
  unclaimed-balances account, and it belongs to the next sub-project.**
  A contributor who "fixes" this by adding `CheckCreditTx` to `SettleCycleTx` reintroduces a
  terminal, unrecoverable cycle for the whole batch, and no test will stop them.
- **`csm.settle` reads the cycle outside a transaction.** Two concurrent
  `POST /cycles/{cid}/settle` can both see `Closed` and both send a `pacs.009`; the second is
  caught at `SettleCycleTx` and dead-lettered. The dead letter is the only signal, and the
  racing-operators case is untested.
- **A failed refund after a rejection is unrecoverable.** `Rejected` is terminal, so no route
  reaches `ReverseDebtorLeg` again. The seam is documented and pinned; the *absence of a
  remedy* is not stated anywhere.
- **Lost update on network rows under Postgres READ COMMITTED.** `PutPayment`/`PutCycle` are
  unconditional upserts, and `resetMu` covers only `Reset`. Reported as measured by one agent,
  not independently re-measured — treat as plausible-unconfirmed.
- **`OpenCycleTx` has the same read-then-write shape** that made the duplicate-reference check
  non-atomic on Postgres, and its doc claims two concurrent callers cannot both open a cycle.
  Not reproduced in 8 runs; treat the comment as unverified rather than false.
- **`POST /payments` on the clearing-house surface bypasses the submitter guard** (no
  `boundPID` there). Pre-existing, live web consumer.
- **An orphan participant row survives a refused admission**, with its chart of accounts and
  reserve, and no route deletes it.
- **`cmd/server`'s listener table is covered only by hand-run probes**, and its port arithmetic
  is duplicated in `web/src/lib/api/backend-url.ts` with no test on either side.
- **Nothing forces the next borrowed foreign error into `borrowedReasons`** — it silently
  becomes MS03.
- **Nothing tests README/route parity.** A route added without a README table row is invisible;
  that is exactly how a deleted endpoint stayed in the web app through 6b.

## What the next sub-project is

`spec/db-per-entity` — **give each institution its own store**. A worktree already exists at
`~/Git/cbs-db-per-entity`, branched from the *old* base `10df781`; **it needs rebasing onto
`7321d83` before any work starts.**

7b built the boundary as *observable* and *nameable* without making it *enforced*, and the
measurements are already the specification:

- The book recorder says exactly which books each actor reaches.
  `TestWhichBooksTheCentralBankReachesWhenItSettles` is written to **fail loudly** the day the
  mirror stops being local — that failure is the signal the split has started, not a regression.
- `bankOps` / `csmOps` / `settlementOps` are the per-entity method lists, now complete and
  mechanically verified to name exactly what the handlers call.

Three things must change that are **domain problems, not infrastructure**:

1. **Settlement and return must stop being atomic across banks.** Today each posts across
   several institutions' books in one `Store.Update`. Across isolated stores that needs an
   unreconciled position — the concept 7b's rejection split already introduced, since rejection
   is the first operation here that can half-happen.
2. **`partyTx`'s cross-bank read has to go**, by making the payee's name travel on the request.
   Until then property 3 above stays false.
3. **The unclaimed-balances account**, which is the answer to the closed-account credit above.

## Verification, exactly

```bash
gofmt -l . && go vet ./... && go build ./...
go test ./...                                                            # store/mem
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

Postgres is local Homebrew — **`make test-pg` uses Docker and does not work here.**
`store/storetest` is the conformance suite: `store/pg` must never accept or refuse a write
differently from `store/mem`, and since 7b it has a concurrency case that detects a divergence
20 runs out of 20 on Postgres.

## The rules that cost the most when forgotten

- A `[[wiki-link]]` to a key missing from `hint-content.ts` throws at runtime and takes **every**
  route down while `next build` stays green. `npm run test` catches it in hint bodies *and* quiz
  explanations. Run it, and load a page.
- `rm -rf web/.next` before `npm run typecheck`.
- Never hand-write a backend path; use `cb()` / `csm()` / `bank(pid, …)`.
- Add a nav entry and its `page.tsx` in the same commit (`nav-integrity.test.ts`).
- `web/src/lib/quiz/diversity.test.ts` holds each chapter to 18–22 questions, ≥8 distinct
  `concept` tags, no tag more than 3×, and all three difficulty tiers.
- Documentation is domain content: `README.md` is authoritative, and `hint-content.ts`, the quiz
  chapters and `0001_init.sql` duplicate it **by design**. A correction in one must be checked in
  the others. Two keyed quiz answers were wrong on `main` before 7b's sweep — learners were being
  marked wrong for the right answer.

## The lesson from 7b, in one line

Every task needed at least one fix round, and **almost every finding was a claim that outran the
code** — a comment describing behaviour the code did not have, or a test that still passed with
the bug reinstated. Two of four holes in one mechanism were introduced by the fix to the previous
hole. The discipline that found them: for every behavioural claim, break the code, watch that
exact test fail, restore. A test whose failure nobody has observed is not evidence.
