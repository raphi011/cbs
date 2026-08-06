# Handoff — sub-project 8 Task 17, `spec/task-17-admission`, ready to merge

31 commits ahead of `main` (`cff8381`) before the fix wave; 90 files,
+11178/−1582. Everything here was verified against the code at the merge commit,
not against a plan.

## Verification, run by the controller — not by a subagent

All six lines green:

```bash
gofmt -l . && go vet ./... && go build ./...
go test ./... -count=1                                              # 5.4s
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./... -count=1
go test ./mesh/ ./api/ ./cmd/... -race -count=1
make test-schemas                                                   # 0 skips
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

The Postgres run took **37.7s** wall with the store-touching packages at 21–36s
(api 36.5, payment 36.2, mesh 33.3, store/pg 31.4, deposit 26.6, seed 25.1,
ledger 21.3) against 1.5–3.5s for the same packages on `store/mem`. That is how
you can tell it did not skip. **`make test-pg` uses Docker and does not work on
this machine** — use the explicit `TEST_DATABASE_URL`.

## Two things a fresh worktree needs, and neither is in git

1. **The ISO 20022 schemas.** `iso20022/testdata/xsd/` holds all ten and is
   **gitignored** — not this repository's to vendor. Without them
   `make test-schemas` skips every subtest and **still prints PASS**.
   `cp -R ~/Git/cbs/iso20022/testdata/xsd <worktree>/iso20022/testdata/`.
2. **`web/node_modules`.** Run `cd web && npm ci` in the worktree. Without it
   `tsc`, `eslint` and `vitest` are all absent, so the web line cannot run at
   all — and an agent may report a run it made somewhere else. That happened on
   this task: a run through a symlink to the main checkout, deleted afterwards,
   which was unreproducible by anyone. `npm ci` also puts a Go package under
   `web/node_modules/flatted/golang/`, so `go test ./...` now walks it and
   prints one extra `[no test files]` line. Harmless.

Where the schemas came from and what the first run found is in Task 15's
history: the check ran for the first time at `e899066` and found **every
camt.053 this system had emitted was invalid** on two counts — `AddtlNtryInf`
six elements out of sequence *under a comment asserting the field order was the
schema's*, and `BkTxCd`, the one mandatory child of an entry, missing outright.
Both had survived a per-task review, a documentation sweep and a whole-branch
review with probes. No probe finds that class. Only the schema does.

## Two rules from Task 16's handoff expire at Task 17.1

Task 16's handoff was explicit that **Task 17's own handoff** must say so rather
than copy the rule list forward. Saying it:

- **"There is one migration. Edit `0001_init.sql` in place."**
- **The `TEST_DATABASE_URL` verification line.**

Both were true while admission was being built and **expire at Task 17.1**, the
SQLite swap (sub-project 9, specced at
`docs/superpowers/specs/2026-08-03-sqlite-only-store-design.md`, landing as
17.1/17.2/17.3 between Task 17 and Task 18). `store/pg` and `store/mem` are both
replaced by one `store/sqlite` backend on the cgo-free `modernc.org/sqlite`, so
Task 18 becomes three shapes × one backend, and its isolation is one database
*file* per entity rather than one Postgres schema per entity.

17.1 translates `0001_init.sql` whole. Its comments are the content and they
carry across; the Postgres spellings will not. Where this task had the choice it
wrote the reasoning rather than the mechanism.

## What Task 17 did

`AddParticipantTx` — one unit of work writing three institutions' things — is
deleted. Admission is a relayed conversation, reusing the return's topology:

1. `Mesh.Admit` reserves the BIC at the mesh, founds the bank in the bank's own
   unit of work (`Status = Founded`), drops the actor again if that fails, and
   returns. The scheme's answer arrives later, as a message.
2. The bank sends one `acmt.007` **per asset**, all sharing one `PrcId`.
3. The clearing house **relays and holds nothing**, refusing before relaying a
   BIC already in its roster under a *different* admission reference.
4. The central bank opens one settlement account per asset in its own book,
   records its own `SettlementMember` row keyed by BIC, answers `acmt.010` (or
   `acmt.011`).
5. The clearing house writes its routing entry **from that acknowledgement**,
   then forwards it.
6. The bank records its settlement references and becomes `Member`.

`payment.Participant` is dissolved into `Bank` (the bank's), `SettlementMember`
(the central bank's, keyed by BIC) and `RosterEntry` (the clearing house's).
`ops.go`'s live-handle hole closed with it: no method on `bankOps`, `csmOps` or
`settlementOps` returns a `*ledger.Book`, `*deposit.Register` or
`*product.Catalogue`.

### Four rulings the schemas and the probes forced

Each is written up as a dated ruling in the spec, because a reversed ruling that
reads like an oversight is worse than the original.

1. **One `acmt.007` per asset.** `Acct/Ccy` is `minOccurs="1" maxOccurs="1"`.
   The spec and the plan both assumed a currency list.
2. **`Refs/PrcId` is the conversation's only correlator** — the acknowledgement
   carries no back-reference at all. This is what makes the clearing house's
   pre-relay refusal implementable: `Mesh.Admit` reserves the address first, so
   the only requests that can reach a taken BIC are the same bank's second asset
   and an operator re-drive. Keyed on the BIC the refusal would refuse both and
   fire on neither.
3. **The roster carries no name.** `acmt.010` identifies the owner with
   `OrganisationIdentification29` — a BIC and nothing else — so the clearing
   house could only carry a name by holding it across the relay, which
   contradicts "holds nothing" and re-imports `csm.held`'s restart defect. The
   name had no production reader.
4. **"Cannot pay or be paid" was half enforced, and the fix is at the door.**
   See below.

## What the whole-branch review found, and it earned its keep again

**Third consecutive sub-project in which this review found a Critical invisible
to a clean per-task history, by building a probe and running it.**

**A founded bank could pay and could be paid, and either stranded the whole
cycle.** The spec's own ruling said *cannot pay* held and gave two reasons; both
were false. An **arranged overdraft** (`api/handlers_deposit.go`, no membership
gate) or a **loan disbursement** (`lending/portfolio.go`) puts spendable money
in a founded bank's customer without any deposit, and `FoundBankTx` gives the
bank internal accounts in every asset so `postDebtorLegTx` never fires.
Measured: `Mesh.Submit` accepted, the customer went −250,000, the `pacs.002`
**dead-lettered** so nobody was told, the payment reached `Cleared`, and the
cut-off then failed — stranding **every other member's payments in that cycle**,
their payees unpaid and their payers' money in clearing suspense. `POST
/cycles/{id}/settle` failed identically and `Reject` was refused as an invalid
state transition. Only admitting the bank cleared it.

**Fixed with one sentinel, `payment.ErrBankNotAdmitted`, refused in two places**
— and the two-guard shape is load-bearing, not belt-and-braces: with the door
bypassed, the paying direction is `Rejected` but the `pacs.002` dead-letters,
because `csm.tell` addresses through the roster too, leaving the money in the
non-member's suspense. So `Mesh.Submit` carries the paying direction and
`AcceptAtCSMTx` is the clearing house's judgement and the cycle's protection.
Its asset arm gave `RosterEntry.Assets` its first production reader.

**An already-`Cleared` payment against a non-member has no route out, and that
is deliberate.** Both doors refuse before `Accepted`, roster entries are only
ever written (no delete on either store), and `AcceptAtCSMTx` is the only act
that puts a payment into a cycle — so this tree cannot produce one. A
`Cleared → Rejected` transition would be an operator remedy with no reachable
input. Admitting the bank still settles such a cycle whole, which is the answer
for a store written by an older build.

Also found and fixed: a second `acmt.010` on the admission's **own** reference
rewrote a member's settlement references permanently; `RecordMembershipTx` could
make a bank a `Member` while recording no settlement account and burning its
`AdmissionRef`, refusing its own true admission for ever.

## Clean under probe, so the next reviewer knows what is covered

Money conservation across all four books at five stages — two-asset admission
plus funding, debtor leg cleared, settlement, a mesh-driven return, a settled
direct debit — on **both stores**, mutation-proved. Redelivery of all eight
acmt messages of a two-asset admission, individually: all accepted, nothing
moved. Concurrency on both stores **with a warm pool**: eight simultaneous
`Admit` on eight BICs and on one BIC — one bank, one settlement member, one
roster entry, one reserve account, no divergence. The partly-admitted state does
**not** strand a cycle.

## Crossings after Task 17

| # | crossing | state |
|---|---|---|
| 1 | `partyTx` reads the counterparty's register | **CLOSED** (Task 14) |
| 2 | `ResolveIdentifierTx` sweeps every member's register | open — Task 18 |
| 3 | `SettleCycleTx` posts in every book | **CLOSED** (Task 15) |
| 4 | `ReturnPaymentTx` posts in three books | **CLOSED** (Task 16) |
| 5 | `AddParticipantTx` writes into `CentralBankBook` | **CLOSED** |
| 6 | `DepositTx` posts in two books | open — **must** close at Task 18 |

**Crossings 2 and 6 are what remain, and both are invisible to every instrument
this sub-project has**, because neither operation ever becomes a message. The
recorder watches mesh actors; `ops.go` narrows what a handler may name, and
funding has no handler. **Task 18's reconciliation harness is the successor
instrument**, and that is the reason to build it rather than a nicety.

`DepositTx` was re-routed and **not fixed** at Task 17: it now reads the bank's
own record of its settlement account, which is legitimate — an account holder
quoting its own account number. **The crossing that remains is the posting**, and
no re-routing of a lookup changes that. `TestFundingAReserveReachesTwoBooks`
pins it as a fact: measured `[bank's book, CentralBankBook]` in **one** unit of
work, with `NetworkBook` absent because a deposit draws no id and appends no
audit event. It passes today and fails the day the stores split.

## Known defects and deferrals carried

- **`payment/system.go`'s deposit refusal says "is founded and not yet
  admitted"**, which is false in the partly-admitted state — the bank is a
  `Member`, stuck in one asset only. Measured. Needs code (a second sentence or
  a per-asset sentinel). Task 19's, with the reconciliation instrument.
- **A partly-completed multi-asset admission cannot be re-driven.** A re-drive
  mints a new process id, and a second asset arriving under a different
  reference is refused. Reachable only from a dead letter. Task 19's.
- **`csm.held` does not survive a restart** — carried from Task 16.
- **A cycle that nets to zero strands every payment in it, for ever** — carried
  from Task 16, pre-existing, and still the most serious thing nobody owns.
- **An on-us payment has nowhere to go** — carried from Task 16.
- ~25 positional "option C/D" references in quiz explanations, unresolvable
  because options are shuffled per session and rendered with no letter label.
- `iso20022`'s `GenericAccountIdentification1/Id` is `Max34Text` and `validate`
  checks presence only; a long ledger id would be caught by `xmllint` alone.

## The process lesson, and it has its own file

**`docs/superpowers/2026-08-06-avoidable-review-cycles.md`.** Twenty-three fix
rounds, every one carrying at least one false claim written inside a correction.
Five of them found real money defects; the other eighteen were claims drifting
from code. The three changes that would cut it most: **plans specify behaviour
and tests, never comment text**; **one canonical statement per domain fact, with
every other layer linking to it**; and **`storetest` drives `payment`'s acts**,
which would have caught the three store divergences automatically instead of one
hand-written probe each.

Read that file before writing the next plan. The previous sub-project's handoff
recorded the same lessons in prose and the recurrence happened anyway, which is
itself the argument for making them mechanical.

## What the next task is

**Task 17.1 — the SQLite swap** (sub-project 9), then 17.2 and 17.3, then Task
18. `docs/superpowers/specs/2026-08-03-sqlite-only-store-design.md` is the spec.
17.1 translates `0001_init.sql` whole; the arguments in its comments carry
across and the spellings do not.
