# Handoff — Task 18e, and Task 18 is finished

**Everything is green and there is no outstanding item on Task 18's plan.**
`go build ./...`, `go vet ./...`, `gofmt`, `go test ./... -count=1` (12/12) and
`make test-schemas` are clean, and so are `npx tsc --noEmit`, `npx eslint src`,
`npm run test` (180 tests, 13 files) and `npm run build` in `web/`.

Five commits on top of `0c60d97`:

```
2534926 the reconciliation harness, and it has been shown five breaks
2de0561 the sweep: the Go docs and CLAUDE.md stop describing the plan
d98f93c the sweep: the README says what the split left
a73293c the sweep: the hints and the quiz are told about the split
83fe5c3 the last reversal, the two open notes, and the lost deep links
```

This supersedes `handoff-2026-08-07-task-18cd-green.md`.

---

## 1. The reconciliation harness

`payment/recon` opens all N+2 databases at once and asks the questions no
institution in the system may ask. It is the successor to `mesh/books_test.go`'s
recorder, and the succession is worth stating precisely because both instruments
are still live: **the recorder's subject is a CROSSING**, and Task 18 made most
crossings impossible rather than merely wrong. What replaces the crossing is two
institutions' books that no longer agree, with no crossing anywhere and no
message misdelivered — the class the spec records as invisible to everything
this sub-project had.

### The five checks, and what each spans

| check | the two things held against each other |
|---|---|
| `reservesMirror` | a bank's own `Reserve at Central Bank` vs the agent's liability to it |
| `suspenseIsExplained` | a non-zero clearing suspense vs everything still owed to that bank |
| `cyclesAndSettlementsAgree` | the clearing house's cycle vs the agent's settlement of it |
| `partiesHoldTheirCopy` | the clearing house's copy of a payment vs each party bank's |
| `admissionWroteItsThreeRows` | a bank's own row vs the agent's account vs the roster entry |

### Two kinds of finding, and only one is a defect

A **break** is two books that disagree with nothing in this system able to make
them agree again. An **unreconciled position** is a clearing suspense that has
not returned to zero with something outstanding against it — a payment in
flight, or a reserve movement the agent made and the bank has not booked. The
second is modelled on purpose and is what the Settlement Finality Directive is
about; a harness that called it a break could not be run against a network with
a payment in it. `Check` fails a test on the first and reports the second.

### The one derivation to be careful with

`unbooked(bic, asset)` is where the harness earns its keep, and it has two
sources because reserves move for two reasons:

- **A cut-off** leaves a `settlements` row with a signed position per member, so
  what a bank still owes is READ. Zero positions are skipped, because the agent
  sends no statement for one.
- **A return leaves the agent NOTHING.** The only durable trace is the
  idempotency key on the reversal, so the movement is DERIVED from the clearing
  house's copy of the returned payment, by the rule that a return sends the
  money back to the payer: **the debtor's bank gains and the creditor's bank
  loses**, on a pull exactly as on a push, because the debtor is the payer under
  both schemes. A payment whose two agents are one institution is skipped —
  `SettleReturn` moves reserves BETWEEN two members and there are none.

That derivation is the one piece of domain knowledge in the harness rather than
a read, and `TestAnUnbookedReturnExplainsBothBanksReserves` is what holds it to
being right. **Watched failing:** with the signs flipped, both banks break by
twice the amount, in opposite directions.

### Where it is calibrated and where it is run

`mesh/recon_test.go` — three controls (settled, returned, two-asset) and five
damaged states written into the databases behind every institution's back, each
of which the harness has to name and name the right books for. The damage is
done through the store rather than through the domain because **none of these
states is reachable through the domain, which is the whole reason the harness
exists**; what produces them in a real system is a lost message or a handler
that failed halfway, and what those leave is rows.

`seed/seed_test.go`'s `TestTheSeededNetworkReconciles` runs it over the widest
deployment this repository builds — four banks, eleven payments in five
statuses, five cycles in three, two settlements and a return — and asserts the
unreconciled positions rather than ignoring them: three banks legitimately end
with one, because that scenario ends with five payments in flight.

---

## 2. What the sweep found, beyond the list it was given

The step-8 and green handoffs listed the sweep's items and every one is closed.
Four things were found that were on no list:

1. **`PostCreditorLegTx` was renamed `SettleAtBankTx` at Task 18d and seventeen
   references still named the old function**, across README, `mesh`, `payment`
   and `docs/`. The rename note keeps its own mention, because it is about the
   old name.
2. **`payment.PartyDetails.Agent`'s doc said the counterparty's BIC is never
   taken from the instruction**, which Task 18a reversed and `api`'s own DTO had
   been contradicting ever since. `hint-content.ts` carried the same claim. The
   quiz did not — chapter 12 had already been corrected.
3. **The README's payment example could not have worked.** It sent
   `"participant"` on both parties, which the API rejects with
   `unknown field`, and `BANK_B` was `:8084`, which is Nordhaven and not the
   Verde its IBAN names.
4. **`api/surface.go:105` was already fixed** — the handoff listed it and
   `handleListParticipants` had moved to `Stores().Banks` at 18d.

### The method that found (3), and it is the one to keep

**Run it.** `go run ./cmd/server -base-port 8081`, then execute the README's
walkthrough verbatim. Two of its lines were wrong and no test in this repository
could have caught either — the example is prose, and neither `api`'s suite nor
`web`'s knows it exists.

---

## 3. Three claims the layers had never been told

Distilled from the README into `hint-content.ts` and the quiz, and each is now
in all three.

- **A payment is THREE ROWS** in three databases that legitimately disagree. The
  lifecycle diagram is a state machine per COPY. The `ACCP` goes only to the
  bank waiting for an answer to its instruction, so the bank that ANSWERED it is
  never told and its copy reads `Initiated` until settlement — which is why
  `Initiated → Settled` is a legal edge at a bank and impossible at the clearing
  house.
- **There is no combined audit log and no order between two of them.** `seq` was
  store-global and is per institution, so `seq 7` names as many events as there
  are institutions and a merged trail interleaves by accident of how busy each
  has been. The honest cross-institution order is NONE, and that is a finding
  rather than a gap: an auditor holding four logs answers it with the MESSAGES.
- **A cycle does not name its settlement.** The id is allocated inside the
  agent's own unit of work in its own database and the `pacs.002` quotes the
  CYCLE. The link exists in the other direction only.

A new hint key, `store-split`, carries all three plus the reconciliation,
because four other hints wanted to point at the same fact and none could.

### The diversity budget is the constraint on the quiz

Chapters 9, 11, 12 and 15 are at the cap of 22 questions, so new content there
went into explanations they already had. Chapter 14 had room for two and takes
the audit log; chapter 16 had room for one and takes `settlements.asset`. **A
new question's `concept` tag has to be checked against the ≤3× rule before it is
written** — `scheme-asset` went to 4 on the first attempt and the fix was the
tag, not the question.

---

## 4. The reversals

All twelve are written where the old claim lived. The last one, and the only one
that was still reading as a stale sentence, was **"there is one migration"** —
now stated in `CLAUDE.md` and the README as a reversal that keeps its reason
intact: no database is deployed, which is why each of the three schemas is still
a single `0001`. What changed is how many schemas the policy governs.

The spec's rule is worth restating because it is the whole point of the
exercise: **a reversed ruling that looks like an oversight is worse than the
original ruling.**

---

## 5. What remains

Nothing of Task 18's. Three things are carried, and none is a defect:

### 5.1 `/clearing-house/settlements` reads the central bank's port

Unchanged from the green handoff and still the right call to defer. The page
reads a port that `web/src/lib/api/endpoints.ts` says a console must not; moving
it under `/central-bank` is a navigation change with a nav manifest, an identity
test and two quiz chapters pointing at it. The note is in `endpoints.ts` above
`listSettlements`. **18e added a third pointer** — chapter 16's new
`settlements.asset` question — so the move is one route rename plus three
`explore` links now.

### 5.2 A bank's own reconciliation is still Task 19's

`payment/recon` is the OMNISCIENT observer, which no institution may be.
`SettlementAdvice.ClosingBalance` is what lets a bank check its own books
against what it was told **without reading anybody else's store**, and nothing
reads it. Those are different instruments for different readers and the second
is not made redundant by the first.

### 5.3 The known defects carried since 18a

Unchanged: a cycle that nets to zero strands every payment in it; `csm.held`
does not survive a restart; a partly-completed multi-asset admission cannot be
re-driven; a refused camt.025 leaves the lodging bank's reserve mirror
overstated (unreachable behind `LodgeReservesTx`'s guard). **The harness is now
the instrument that would detect the last of those**, which is what 18a
predicted of it — but no test provokes it, because the guard makes it
unreachable.

---

## Eight things not to rediscover

1. **`go test ./...` is the whole suite and it passes**, and `make test-schemas`
   is the second gate. Neither needs setup.
2. **A break and an unreconciled position are different findings.** A test that
   asserts "no breaks" is asserting something much weaker than "nothing
   outstanding", and `TestTheSeededNetworkReconciles` asserts both halves on
   purpose.
3. **The harness reads through `payment.Stores`, never through a `Network`.** A
   `Network` is one institution's handle and every method on it is an act that
   institution may perform; reading four institutions' books is not an act.
4. **`recon.Break.Where` is prose, not an identifier**, and the calibration
   tests match it exactly — `member(bic, asset)` builds the member form. A
   prefix match would let "the clearing house" pass for "the clearing house and
   the settlement agent".
5. **A `[[wiki-link]]` to a missing key takes every dev route down and `next
   build` stays green.** `npm run test` catches it; loading a page is the second
   check and it is cheap.
6. **The quiz's diversity test is four separate assertions** and the tag count
   is the one that bites. Check `conceptCounts` before writing a question, not
   after.
7. **Run the README's walkthrough before believing it.** Two of its lines were
   wrong for a task and a half.
8. **A rule that holds only because no caller builds the counter-example is a
   rule nobody is keeping.** That sentence found two defects at 18c/18d and it
   is why `ErrNotAPartyToThisReturn` keeps a guard the store already makes
   unreachable.
