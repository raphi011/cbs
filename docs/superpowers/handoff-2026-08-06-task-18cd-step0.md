# Handoff — Task 18c/18d step 0 done, `spec/sqlite-only-store`

**Nothing is committed.** The tree builds except for the two packages that were
already red before this work started, and they are red for the same one reason:
`sqlite.Open` gained `shape` and `book` arguments in the previous commit and
nothing has been rewired to pass them.

```
store/testenv/testenv.go:68   not enough arguments in call to sqlite.Open
cmd/server/main.go:192        not enough arguments in call to sqlite.Open
```

`store/storetest` was the third failing package and is fixed. This supersedes
`handoff-2026-08-06-task-18cd.md` as the entry point; that document's remaining
work list (steps 1–9) stands, minus step 0, and its rulings are not repeated.

## What step 0 was, and why seven of its eight sites disappeared

The plan said: turn the eight `GetRosterEntry(ParticipantID)` call sites into
`GetRosterEntryByBIC`, delete `payment/list.go`'s id-keyed wrapper, and take
`Participant` off `payment.PartyRef`.

**Only one of the eight became a call.** That is the third ruling paying off
rather than a shortcut: every one of those callers converted an id into an
address so it could send a message, and the id IS the address now, so the
conversion had nothing to convert. Seven of them read a value they were already
holding — `p.DebtorDetails.Agent`, `p.CreditorDetails.Agent`, or a BIC-keyed
`NetPositions` key. The survivor is `Mesh.Submit`'s membership check, which was
always asking a different question ("is this address a member?") and is the only
one that needed the roster at all.

`csmOps` and `bankOps` lost `GetRosterEntry` entirely. A member bank no longer
asks the clearing house who its own payment's payer is.

## What is verified, and what is not

**`go build ./...` and `gofmt` are clean apart from the two lines above.**

**No test has been run, and none can be.** Every test package imports
`store/testenv`, which does not compile. That was true before this work too.

To get compiler feedback on the ~270 test-file sites this change touches, the
test packages were type-checked through a `go build -overlay` that swaps in a
one-line `testenv` calling `sqlite.Open(ctx, sqlite.Bank, "typecheck-only", "",
clock)`. The overlay lives in the session scratchpad and is not in the repo; it
is worth recreating, because it is the only feedback available until step 2:

```
go test -overlay=<overlay.json> -gcflags=-e -c -o /dev/null ./payment/
```

Everything compiles under it except the two deferred items below. **Compiling is
the whole of what is known.** Anything in this handoff about runtime behaviour is
reasoning, not measurement.

## Two deferred items, both already the plan's

- **`mesh/books_test.go` still has 26 `ledger.NetworkBook` references.** That is
  the plan's step 6, and it is not mechanical: the recorder's expected book sets
  have to be re-decided per assertion now that each institution writes its own
  log. Left alone deliberately.
- **`store/sqlite/sqlite_test.go` has the two `Open` call sites.** Steps 1–2.

## Three regressions, named because they are real

Each is a consequence of a ruling the user made, and each is a place where the
tree is honestly weaker until the store split lands.

1. **`ListMandates` and `GetMandate` no longer filter.** A mandate carried its
   creditor's participant and the guard compared it against `self`. There is no
   such field — a mandate is the creditor bank's row, so the column would hold one
   value for ever, and the schema argues it out. The row set is supposed to be
   this bank's *by construction*, and the construction is the store split. **Until
   step 2, a bank's `/mandates` lists every bank's.** No per-row workaround was
   added; `Network.ListMandates`' doc names the gap and
   `TestAMandateBelongsToItsCreditorsBankAndToNoOther` asserts the transitional
   truth with a comment saying what to restore.
2. **A payment naming an unknown bank is 422, not 404.** The old 404 came from
   the id-to-BIC step failing on a missing *bank row* — the clearing house reading
   a member's database. There is no bank row in the way now, so an address in no
   roster is `ErrBankNotAdmitted`. `api.TestAPaymentNamingAParticipantThatDoesNot-`
   `ExistIsNotFound` is renamed to `…ABankNoSchemeHasAdmittedIsUnprocessable` and
   its doc argues that the distinction lost was one the reader was not entitled to.
3. **`mesh/sdd_test.go`'s `unaddressable` stub is deleted.** It faked a roster
   lookup failing for one participant, and there is no lookup. The test it served
   — the refund is attempted even when the submitter's message fails — is rewritten
   around the failure that still exists: the SEND, provoked by moving the
   submitter's agent to an address the mesh has no actor for, on the local copy of
   the payment that `tell` is handed.

## Decisions taken that were not in any plan bullet

- **`csm.tell` gained a `payerAccepted bool` argument.** It read `p.DebtorLegTx`
  to decide whether the payer's bank is holding money against a rejected pull, and
  that column is the bank's — the `csm` schema has no leg columns, so on this
  actor's copy the field is empty for every payment and no rejected collection
  would ever reach the payer's bank. The replacement is "has the payer's bank
  accepted this collection", which is the fact the column stood for. **The two
  callers witness it differently and neither witness works for the other:**
  `receiveStatus` has the bank's own pacs.002 (ACCP means the leg is posted and
  the refusal is this actor's); `reject` has no message and reads its own copy's
  status before rejecting. The copy's status cannot serve the first case, because
  `clear` rejects after `AcceptAtCSM` has rolled back. That asymmetry is why it is
  an argument and not a derivation, and it is written on `tell`.
- **`SettlementStatement.Member` is deleted**, leaving `Agent`. Same argument as
  the two dropped `payments` columns: one bank as an id and as an address, and the
  settlement agent that builds these holds no table to convert between them.
- **`payment.Network.selfBIC()` and `api.Server.boundBIC()`** are the two places
  that say "a ParticipantID is a BIC" out loud. They exist so the conversion is
  not spelled at ~20 call sites, and so that the type question `AsBank` leaves
  open has exactly two lines to be answered on. **The question is still open** —
  nothing here decided it.
- **`checkPartyTx` and `validateFunds` read `s.self()`.** They read the bank the
  PartyRef named. Every caller was already passing its own side — a bank checks
  its own customer and records what it is told about the other — so this is a
  narrowing, not a redirection. `selfBankTx` is the helper.
- **`bankByBICTx` is a keyed read.** It was a `ListBanks` sweep with a documented
  "first match wins" limit and a request for a unique index. The address is the
  primary key now, so a second bank on one address is not a row the table can
  hold. One of the 38 `ListBanks` sites is gone with it.
- **`m.banks` is still keyed by `ParticipantID`** and the two BIC-derived lookups
  convert. Re-keying it is one edit with `claimAddress`'s sweep and `Lodge`'s
  argument, and all three belong to the admission wiring rather than here. The
  field's doc says so.

## The API's wire format changed, and `web/` has not been touched

- `partyRefDTO` lost `participant`. A party object is `{account, identifier}`.
- `paymentDTO` gained `debtorAgent` and `creditorAgent`; `mandateDTO` gained
  `debtorAgent`; `createMandateRequest` requires `debtorAgent`.
- `directoryEntryDTO`'s `participant` became `agent`.
- The values are BICs where they used to be `bank_1`-style ids.

**`web/src` is unchanged and will be wrong.** It is on the plan's list already
(`web/src/lib/identity.ts` and its test, the `/bank/{pid}` route segment); this
adds the four DTO fields above to what that step has to carry.

## Store-layer changes worth knowing before step 2

- **`banks` has no `bic` and no `book_id` column.** `PutBank` writes neither and
  `scanBank` derives both from the id. A record whose BIC disagrees with its id is
  normalised rather than refused — `FoundBankTx` is the thing that mints one and
  the place that would refuse.
- **`settlement_positions.participant_id` is `bic`**, and `marshalPositions` /
  `unmarshalPositions` are BIC-keyed.
- **`storetest`'s bank fixtures are BICs.** `bankRow` takes one value and fills
  all three identifiers, because the store writes one column and derives the rest;
  a fixture that set them independently could not round-trip. The
  `PaymentListOrderingIsCreatedAtThenSeq` bank rows can no longer use the 9→10
  lexicographic boundary and use four addresses whose alphabetical order is not
  their insertion order instead.
- **`storetest.go` gained `bookC`** for the one audit-paging case that named
  `ledger.NetworkBook`. A name from that package rather than `payment`'s, because
  the file talks only to `ledger.Store` and `ledger.Tx`.

## `payment/audit_test.go` is the one to look at first after step 2

Its premise — one network-wide payment-scope log — is dissolved. `paymentAudit`
now reads all three institutions' logs and merges them by `Seq`, which is a
store-global total order, so every existing assertion keeps meaning "these events
happened, in this order".

**What it deliberately does NOT assert is which book each event landed in.** That
is a per-event claim (a mandate is its creditor bank's, a cut-off is the clearing
house's, a settlement account is the central bank's) and it is worth pinning only
once a store per entity makes it more than a column. The reading was done and is
recorded here so the next person does not redo it:

| event | book |
|---|---|
| `participant.added` | whoever founds — today the clearing house, via `storetest.Admit` |
| `settlement.account.opened` | central bank |
| `member.admitted` | clearing house |
| `membership.recorded` | the bank's own |
| `mandate.created` / `mandate.revoked` | the creditor bank's |
| `cycle.*`, `payment.accepted`, `payment.rejected`, `payment.cleared` | clearing house |
| `payment.initiated` | the submitting bank's |
| `payment.settled`, `payment.returned` | the posting bank's |

`auditReaders` still finds the banks through the clearing house's `ListBanks`,
which is one of the 38 sites the split has to answer for. Its doc says why the
roster cannot replace it here: a FOUNDED bank has no roster entry and does have a
log, and there is a test about exactly that bank.

## One test helper changed shape, and it was a real defect

`payment/system_test.go`'s `initiate` submitted through `sys` — the CLEARING
HOUSE's network. That worked while a payment's refs named their own banks and
`checkPartyTx` read the named one. Submission resolves the submitting side in the
network's own register now, so it goes through `sys.bank(submitterOfReq(sys,
req))`. Production always called it on a bank's network (mesh, api and seed all
do); the helper was the only caller that did not.

Both fixtures (`networkWithTwoBanks`, `networkWithACollection`) now fill BOTH
`DebtorDetails` and `CreditorDetails`. `SubmitPaymentTx` discards the submitting
bank's own agent and refills it from that bank's row, so naming it changes nothing
about the payment — what it buys is that a request still says which bank submits,
which is what `mesh.Mesh.Submit`'s on-us guard compares and what the seed relies
on for the same reason.
