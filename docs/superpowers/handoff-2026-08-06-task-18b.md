# Handoff — Task 18b is next, `spec/sqlite-only-store`

**18a is complete, committed and reviewed. 18b is unstarted.** The working tree
is clean at `8a3f85f`, four commits past `40f188c`, and **nothing is pushed**.

Read this first. `handoff-2026-08-06-task-18a-complete.md` is 18a's completion
record and stays for its verification commands, its reversal list and its
defect list — this document does not restate them.

The task is **18b — `payment.Network` gains an identity**; its plan is
`docs/superpowers/plans/2026-08-06-task-18-split-the-stores.md`. **Read that
plan's *Global constraints* before starting.** Everything below was measured
against the working tree today, not copied from the plan.

## What is on the branch since the last handoff

| commit | what |
|---|---|
| `77a5c3f` | 18a: the four crossings close (59 files) |
| `31308d7` | 18a's handoff |
| `185087d` | the whole-branch review's three fixes |
| `8a3f85f` | the review recorded in 18a's handoff |

The whole-branch review **has now run** — three axes, all 19 commits against
`origin/main` (`48f2cdb`). It found a money defect per-task review could not
see, which is the third branch running that this has happened on. Do not treat
it as covering 18b: it covered the tree as of `31308d7`.

## 18b in one paragraph

`NewNetwork` takes **which entity this is**. A bank's network knows its
`ParticipantID`, its `BookID` and its BIC; the clearing house's and the central
bank's know theirs. Every act that today takes a participant argument to decide
*whose* book to touch reads it from the identity instead, or is refused.
`ResolveIdentifierTx` loses 18a's argument. `centralBank` stops being a field on
every `Network` — only the settlement agent's has one.

## The one ruling to make before writing code

**18b cannot stop at `payment` and `mesh`. `api` is forced along with it, and
the plan puts that in 18d.**

`api` holds **one** `*payment.Network` (`api/server.go:102`, `s.net`) and tells
its listeners apart with a separate field, `boundPID` (`:50`). `forBank`
(`:228`) hands each listener a shallow copy whose comment says so outright:
*"every listener shares the one Network"*.

That works today only because the identity travels as an argument.
`api/handlers_directory.go:72` calls
`s.network().ResolveIdentifier(ctx, s.boundPID, ident)`. The moment
`ResolveIdentifier` reads its identity from the `Network` instead, one shared
`Network` means `GET /directory` on bank A's port and on bank B's port resolve in
the **same** register — which is precisely the crossing 18a closed, reopened one
layer up and invisible to the recorder, because `api` is not an actor.

So one of these has to be decided and written down:

- **Pull `forBank`'s network selection forward from 18d.** `api` holds N+2
  `Network` values and `forBank` picks one. This is 18d's first bullet arriving
  early; the store is still one, so it is a wiring change and not a split.
- **Keep the argument on `ResolveIdentifier` alone** until 18d, and let the
  identity govern everything else. Cheaper, and it leaves the task's own headline
  bullet half-done — the plan lists `ResolveIdentifierTx` losing 18a's argument
  as one of 18b's four.

I did not decide this. It is the first thing 18b's plan needs and it changes the
size of the task by a lot.

## What 18b touches, counted

**`NewNetwork`: 23 call sites**, which is exactly the plan's number. Six are
non-test — `payment/system.go`, `cmd/server/main.go`, and
`store/storetest/races.go` among them; the other 17 are tests across `mesh`,
`api`, `seed` and `payment`.

**Ten `by payment.ParticipantID` arguments on the three interfaces in
`mesh/ops.go`:** `CreditTransferRequest`, `DirectDebitRequest`, `AcceptInbound`,
`PostReturnLeg`, `ReverseReturnLeg`, `PostSettlementAdvice`, `PostCreditorLeg`,
`LodgeReserves`, `RecordMembership` — and `ResolveIdentifier`, which is on
`payment` rather than on an ops interface because `api` is its caller.

**Every mesh call site passes `b.pid`, and I checked all ten.** They are all in
`mesh/bank.go` and there is no case where an actor passes another participant's
id. So from the mesh's side the removal is mechanical: the argument is already
the actor's own identity in every instance, and `bank.pid` already documents
itself as *"a loan and not the answer"*. **The pressure is entirely on `api`**,
per the ruling above.

**`centralBank`: 18 readers in `payment/system.go`**, plus the exported
`CentralBank()` accessor at `:216`. They are not evenly distributed — the
settlement, lodgement and admission paths hold nearly all of them
(`:1095`, `:1838`, `:2188`, `:2210`, `:2226`, `:3778`, `:3792`). Deciding what a
`Network` without a `centralBank` book *cannot do* is one of the two arguments
the plan says the docs must make; the other is why the identity is constructor
state rather than a per-call argument.

**`ledger.NetworkBook`: 120 references.** That is **18d's**, not 18b's, and the
ruling for it is already made — `261be19`'s commit message: THE COUNTER FOLLOWS
THE ROW. Do not start it here.

## Watch it fail

The plan names `TestTheCSMTouchesOnlyTheNetworkBook` and the recorder's
per-actor sets, and the bar it sets is worth quoting: *a bank's network built
with a clearing house's identity must fail a test, not merely behave oddly.*
That test exists (`mesh/csm.go:79`, `mesh/operator_test.go:53` and
`mesh/ops.go:318` all point at it).

## Rules that still hold

- **There is one migration**, `store/sqlite/schema/0001_init.sql`, edited in
  place. It dies at **18c**, not before. 18a already changed it once —
  `bank_assets.vault_cash` — so "nothing about the store changes" is not a rule
  you can lean on.
- **Nothing cross-checks the SQL.** `store/mem` and `store/pg` are both gone.
  The review diffed the rewrite against `git show 48f2cdb:store/pg/*` and found
  it faithful; that is a one-time check of the tree as it stood, not a standing
  guarantee.
- **The ephemeral store hides read-then-write defects.** Anything measured about
  ordering opens a file. `TestTheRetryBudgetOutlastsASlowWriter` is the one that
  does.
- **Two of the nine races do not bite on one run** —
  `ConcurrentAdmissionsAgreeOnOneCentralBank` and
  `ConcurrentMarkReversedOnlyOneWins`. `-count=10` for anything resting on either.
- **A `[]byte` is a BLOB and a `STRICT TEXT` column refuses it (3091).**
- **`sqlite_master` holds only comments inside a statement's parentheses.**
- **`iso20022/doc.go` opens with a description and no message count.**

## What 18a's review left for 18b to trip over

Three carried defects are in `api` and `payment`, which is where 18b works:

- **`mandateAssets` reads a member bank's deposit register from the CLEARING
  HOUSE's port** (`api/handlers_payment.go:36`). The HTTP twin of
  `CreateMandateTx`'s crossing. It will not survive 18c — the `csm` shape has no
  deposit register — so 18b is the last comfortable moment to decide who owns it.
- **`CreateMandateTx` calls `checkPartyTx` for BOTH parties**
  (`payment/system.go:1869,1873`). Not in the spec's crossing table; 18a did not
  close it.
- **`ReceiveLodgementTx` accepts an instruction quoting no account** — the
  `in.Account != "" &&` escape. Unreachable through `ReadLodgement`; reachable in
  principle because `ReceiveLodgement` is exported on `settlementOps`.

The rest of the defect list is in 18a's handoff and is unchanged.

## Verification

```bash
gofmt -l . && go vet ./... && go build ./...                                # clean
go test ./... -count=1                                                      # 12/12 ok, ~40s
go test ./store/sqlite/ -count=10 -run 'TestRaces|TestConcurrentTxRaces'    # ok
go test ./mesh/ ./api/ ./cmd/... ./store/sqlite/ -race -count=1             # ok, ~4m
cd web && npx tsc --noEmit && npm run lint && npm run test                  # 180 tests, 13 files
make test-schemas                                                           # 42 PASS, 0 skips
```

Two things to know rather than rediscover. The `-race` line takes about **four
minutes** and exceeds a two-minute command timeout, so it looks like a hang — run
it in the background. And `make test-schemas` reports **42**, not the 44 an
earlier handoff claimed; the count was re-measured uncached. `iso20022/testdata/xsd/`
is gitignored, so a fresh worktree needs the schemas fetched again — the archive
URL is in `iso20022/testdata/README.md`.

**Not re-run since `185087d`:** the `-race` pass and the `-count=10` race suites.
That commit touches a mesh handler and prose rather than anything concurrent, but
it is stated rather than assumed.
