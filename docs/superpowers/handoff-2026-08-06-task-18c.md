# Handoff — Task 18c is next, `spec/sqlite-only-store`

**18b is complete and committed. The carried defects are closed. 18c is
unstarted.** Two commits past `03aa3d8`, **nothing is pushed**.

Read this first. `handoff-2026-08-06-task-18b.md` and
`handoff-2026-08-06-task-18a-complete.md` stay for their reversal lists and the
parts of the defect list this document says are unchanged; neither is the entry
point any more.

The task is **18c — three shapes, three schemas**; its plan is
`docs/superpowers/plans/2026-08-06-task-18-split-the-stores.md`. **Read that
plan's *Global constraints* and its three *open questions* before starting.**
Every measurement below was taken against the working tree today.

## What is on the branch since the last handoff

| commit | what |
|---|---|
| `836aa67` | 18b: `payment.Network` gains an identity (33 files, +1237/−622) |
| _pending_ | the three carried defects close (31 files, +637/−252) |

## 18b in one paragraph, and the ruling it was given

`NewNetwork(store, clock, id)`. `payment.Networks` mints one `Network` per
institution and is the only thing in the repository holding more than one view;
the mesh gives each actor its own, and **each api listener holds the one
belonging to its own surface**. That last part was the ruling: `api` held ONE
`Network` and told bank listeners apart with `boundPID`, which worked only while
the identity was an argument — the moment it became constructor state, one shared
`Network` meant `GET /directory` on two banks' ports resolving in the same
register. `forBank` binds `nets.Bank(pid)`; it needs no I/O, because a bank IS
its own book.

Nine `by payment.ParticipantID` arguments on `mesh/ops.go` are gone, and so is
`ResolveIdentifier`'s. `centralBank` is nil on every network but the settlement
agent's. `ErrNotThisInstitutionsAct` is the sentinel for both halves.

## The defect commit, and the ruling IT was given

**A mandate is the CREDITOR's bank's row.** The user was asked; this is what the
code already said (`SDD.ValidateMandate`: *"the half the CREDITOR's bank runs,
because in SEPA the creditor holds the mandate"*) and what 18c's own table says
(`mandates` is in the `bank` shape).

- `payment.Mandate` gains `Asset`, from the creditor's own account.
- `CreateMandateTx` refuses a network that is not the creditor's bank
  (`ErrNotThisBanksMandate`), checks only its own party, and **records** the
  debtor — the shape 18a gave the counterparty's BIC.
- `ListMandates`, `GetMandate` and `RevokeMandateTx` are this bank's own.
- `POST`/`GET /mandates` moved from the clearing house's port to the bank's;
  `mandateAsset`/`mandateAssets` are **deleted**.
- `mandates` gains an `asset` column, argument inside the parentheses,
  **verified present in `sqlite_master`** by executing the schema and reading the
  row back.
- `ReceiveLodgementTx`'s `in.Account != "" &&` escape is gone.

**One guard was made LATER and it is a real loss, recorded rather than hidden.**
A mandate whose two accounts are in different assets used to be refused at
creation. That comparison needed both banks' registers. It is now refused at the
first collection, by the debtor's bank, with the same `ErrAssetMismatch` — see
`TestAMismatchedMandateIsRefusedAtItsFirstCollection`, which is the old test
rewritten rather than deleted.

## Watch-it-fail, both commits

Every guard was broken and the exact assertion watched failing, then restored:

| broken | what failed |
|---|---|
| `Network.self` returns the zero participant | 20 assertions |
| `centralBankBook` hands every network the book | 12 assertions |
| the scheme registry unshared per `Network` | 3 institutions |
| the zero-`Identity` panic removed | 1 |
| `forBank` binds one bank's network for all | `TestDirectoryDoesNotAnswerForAnotherBanksCustomer` answers **200 with bank_1's customer on bank_2's port** |
| `forBank` keeps the parent's network | `TestEachListenerActsAsItsOwnInstitution` |
| the on-us check resolves in another bank's register | 8 mesh tests, `TestTheCSMTouchesOnlyTheNetworkBook` among them |
| `CreateMandateTx` stops refusing another bank's | `TestAMandateBelongsToItsCreditorsBankAndToNoOther` |
| `ListMandates` stops filtering | same test, on the count |
| the `in.Account != ""` escape put back | `TestALodgementQuotingNoAccountIsRefused` |

**The lodgement fix had NO test until the break found that out.** Restoring the
escape broke nothing, so the test was written afterwards. Break every fix; a
green suite after a fix proves only that nothing else noticed.

## Two things that were found by writing tests, not by reading the diff

1. **The central-bank guard sat after an idempotent early return.**
   `OpenSettlementAccountTx` returns early for an asset it already holds, so a
   redelivered `acmt.007` reached a *clearing house's* network and got a member
   row back with **no refusal at all**. Same in `ReceiveLodgementTx` and
   `SettleReturnTx`. All three check first now, and so do the two translators —
   a malformed pacs.008 handed to the clearing house used to come back a parse
   error, saying nothing about the institution. This is the method doc's class 4
   exactly: *a guard closing a hole and leaving its own "does not apply" value
   open.*
2. **A scheme registry per `Network` broke three tests** that register a scheme
   on one handle and act on another. Separate STORES are the split; separate
   scheme registries never were, and two institutions disagreeing about which
   schemes exist is one unable to read what the other can write. `Networks`
   shares one registry across everything it mints, and that survives 18d.

## What 18c inherits, measured today

| | |
|---|---|
| `store/sqlite/schema/0001_init.sql` | **1729 lines, 31 tables** (the plan measured 1669/32) |
| `ledger.NetworkBook` | **122 references**, 82 of them in `mesh/books_test.go` |
| `NewNetwork` direct call sites | **6**, all tests, all deliberate — five `failingStore` decorators and the zero-`Identity` panic. Everything else goes through `NewNetworks` (18 sites) |
| `ListBanks`/`GetBank` non-test | **38** |
| `storetest` suites | `RunLedger`, `RunDeposit`, `RunProduct`, `RunLending`, `RunPayment`, `RunRaces`, `RunConcurrentTxRaces` |

**The table count is 31 and not 32** — the plan's number was taken at `2ed68ad`
and one table has gone since. Re-derive the 18c assignment table against the file
rather than pasting the plan's.

**Two of 18c's assignments are now settled by code rather than by the plan:**

- `mandates` is the **bank** shape's, which the plan already said and the code
  now agrees with. Its `asset` column is new since the plan was written.
- The `csm` shape has **no deposit register**, and nothing reads one from a
  clearing house any more. `mandateAssets` was the last such reader.

## The three open questions, and what has changed for them

1. **What does `RunRaces` mean across three stores?** Unchanged and unanswered.
   Its four cases now name the institution performing each act — `Admit` goes
   through `nets.ClearingHouse()`, `nets.CentralBank()` and `nets.Bank(id)` act
   by act — so the crossings are visible in the source rather than implied.
   **Answer it by running it, not by reasoning. Do not delete it to make 18c
   compile.**
2. **Does the bank hold a settlement-position row?** Unchanged. Resolve against
   what reads it before assigning the table.
3. **Where does the audit log live?** Unchanged, and it is a real API change that
   is in no plan bullet.
4. **`store/testenv.Store`** aliases `storetest.Store`, which names four layers.
   Unchanged.

## Rules that still hold

- **There is one migration**, edited in place. **It dies at 18c** — that is this
  task. Two tasks have now changed its shape (`bank_assets.vault_cash` at 18a,
  `mandates.asset` here), so "nothing about the store changes" was never a rule.
- **Nothing cross-checks the SQL.** `store/mem` and `store/pg` are both gone.
- **The ephemeral store hides read-then-write defects.** Anything measured about
  ordering opens a file. `TestTheRetryBudgetOutlastsASlowWriter` is the one that
  does.
- **Two of the nine races do not bite on one run** —
  `ConcurrentAdmissionsAgreeOnOneCentralBank` and
  `ConcurrentMarkReversedOnlyOneWins`. `-count=10` for anything resting on either.
- **A `[]byte` is a BLOB and a `STRICT TEXT` column refuses it (3091).**
- **`sqlite_master` holds only comments inside a statement's parentheses.** The
  cheap way to check: execute the schema into `:memory:` and read
  `select sql from sqlite_master where name = '<table>'`. That is how
  `mandates.asset`'s argument was verified rather than assumed.
- **`iso20022/doc.go` opens with a description and no message count.**

## Known defects and deferrals carried

Three are **closed** by the second commit: `mandateAssets`' crossing,
`CreateMandateTx`'s two-party read, and `ReceiveLodgementTx`'s empty-account
escape. Everything else in 18a's handoff is unchanged, and these are the ones
worth naming here:

- **A cycle that nets to zero strands every payment in it, for ever.** Still the
  most serious thing nobody owns, and still in no plan bullet.
- **`csm.held` does not survive a restart.**
- **A partly-completed multi-asset admission cannot be re-driven.** Task 19's.
- **An on-us payment has nowhere to go.**
- **A refused camt.025 leaves the lodging bank's reserve mirror overstated.**
  Unreachable behind `LodgeReservesTx`'s guard; 18e's harness is what would find
  it.
- **A cross-bank address collision is no longer observable by anything.**
- **`resolveOwnPartyTx`'s unaddressed-party fallback.** 18d deletes it.
- **`Mesh.clearingHouse` performs two reads that are the BANK shape's:**
  `joinRoster`'s `ListBanks` and `Admit`'s `GetBank`/`FoundBank`. They are named
  on the field's doc rather than hidden, they are all in one place, and they are
  on 18d's list in its own words — every `ListBanks`/`GetBank` call site becomes
  a roster read at the clearing house, a message, or an error. **18c will make
  them fail**, because the `csm` shape has no `banks` table.
- **`storetest.Admit` founds through the clearing house** for the same reason:
  the joining bank has no handle of its own yet. Same fate at 18c.
- ~25 positional "option C/D" references in quiz explanations.
- `iso20022`'s `GenericAccountIdentification1/Id` presence-only validation.
- The pacs family's and camt.053's builders have no `ValidateAgainstTheSchema`
  coverage.
- **`docs/cbs-vs-book.md` item 5 is half closed.**

## Reversals carried for 18e's list

18a's four stand. Add:

5. **A mandate is the creditor bank's row, not the network's.** Reverses
   "mandates are a network-level resource in the API", which `hint-content.ts`
   asserted and `README.md`'s clearing-house table showed.
6. **A mismatched-asset mandate is created and refused at first collection.**
   Reverses `CreateMandateTx`'s "the two accounts have to agree before the
   mandate exists".
7. **`payment.Network` is one institution's handle.** Reverses every claim that
   the process holds one shared network — `README.md`'s "six ports over one
   shared `payment.Network`" was the live one.

## Documentation state

18b and the defect commit corrected only what they falsified; **the sweep is
still 18e's.** Corrected here: `README.md` (the one-shared-Network claim, both
endpoint tables, the composite-key parenthesis), `hint-content.ts`'s `mandate`
key, `mesh/doc.go`, `mesh/books_test.go`'s deferral note, `web/src/lib/identity.ts`
and its test, `quiz/types.ts`'s `EXPLORE_ROUTES` and three `explore` links in
`quiz/chapters/12-sepa.ts` that pointed at the moved page.

**The quiz lost three deep links and gained none.** `EXPLORE_ROUTES` is typed and
holds operator-level routes only; `/bank/{pid}/mandates` has a pid in it, so
there is nothing for those questions to link to. Worth a decision in 18e rather
than leaving three questions quietly thinner.

## Verification

```bash
gofmt -l . && go vet ./... && go build ./...                                # clean
go test ./... -count=1                                                      # 12/12 ok, ~35s
go test ./store/sqlite/ -count=10 -run 'TestRaces|TestConcurrentTxRaces'    # ok, ~4s
go test ./mesh/ ./api/ ./cmd/... ./store/sqlite/ -race -count=1             # ok, ~4m
cd web && npx tsc --noEmit && npm run lint && npm run test                  # 180 tests, 13 files
make test-schemas                                                           # 42 PASS, 0 skips
```

Three things to know rather than rediscover.

**The `-race` line takes about four minutes** and exceeds a two-minute command
timeout, so it looks like a hang — run it in the background. **Do not pipe it
through `tail`**: nothing is written until the whole pipeline exits, so the
output file stays empty and a watcher polling it learns nothing.

**`make test-schemas` reports 42**, counting `--- PASS` lines. A different grep
gives 44 by also counting summary lines; pick one and say which.
`iso20022/testdata/xsd/` is gitignored, so a fresh worktree needs the schemas
fetched again — the archive URL is in `iso20022/testdata/README.md`.

**Run `make test-schemas` from the repository root.** A previous `cd web` in the
same shell makes it fail with "No rule to make target".

## Two process failures from this session, recorded because they are cheap to repeat

1. **`git checkout <file>` to undo a probe destroyed uncommitted work, twice** —
   once the whole of `mesh/mesh.go`, once `ListMandates`. Both were recovered
   from a `cp` backup taken seconds earlier. **Copy the file aside and copy it
   back; never `git checkout` a file with uncommitted work in it.**
2. **A `-run` filter that matches no test prints `ok`**, and it was read as a
   passing measurement. `go test -run TestThatDoesNotExist ./pkg` is
   indistinguishable from a pass. When watching something fail, confirm the
   break applied *and* that the test ran.
