# CLAUDE.md

Guidance for Claude Code when working in this repository (a core banking system
plus a quiz and web UI).

The teaching book _How Money Moves_ lives in the _Lead Engineer's Field Guide_
(the `second-brain` repo, Part IX), not here.

## Comments: what earns its place

**Three lines is the budget, and it is a ceiling rather than a target.** Every
Go comment in this repository states its claim in at most three lines. There is
no larger allowance for an exported type, a long-form `#` section or a
`doc.go`: a package doc is three lines like everything else. The one exception
is a single sentence that will not fit, which is kept whole rather than cut in
half.

**The argument is not here.** Refusal rationales, absence-records, the
"why not the other way" reasoning and the worked examples were removed
deliberately, not lost. Do not reinstate them beside the code. Where a decision
needs to be recoverable, it goes in `docs/adr/` as a numbered ruling or in
`docs/specs/` as a design record, and the comment states the rule and points
there in the same three lines.

**Write the rule, not the history.** A comment states what is true now. It does
not say what the code used to do, which task changed it, what an earlier version
of the comment claimed, or that a ruling was reversed. Words that should not
appear in a comment: "used to", "no longer does", "Task N", "sub-project N",
"this comment said", "an earlier version".

**Do not restate the code.** If the sentence can be read off the identifiers and
the three lines below it, delete it. Prefer a better name to a comment that
explains a worse one.

**Say it once.** A rule belongs in one place, with the other sites pointing at
it. Three copies become three versions.

**The budget is not enforced mechanically.** Nothing in CI measures it, so it
holds only as long as it is applied in review. If it is drifting, measure it
before arguing about it: `find . -name '*.go' | xargs grep -h '^\s*//' | wc -l`
was 12,058 when this rule was written.

**The schemas are exempt, and deliberately.** The three `0001_init.sql` files
carry the domain argument for the relational mapping, at whatever length it
needs. See the section below: what is ABSENT from a schema has no column to
hang a comment on, so the prose is the only record of it.

## Domain knowledge stays consistent across layers

The banking/accounting/payments content is duplicated, by design, across:

- `README.md` — the authoritative source.
- `CONTEXT.md` — **not** a fifth copy: a disambiguation table, one line and an
  `_Avoid_` list per term, holding only which word wins where several name the
  same thing. It carries no argument and no explanation; when a term here and a
  term there disagree, the README is right and this file is stale. Renaming a
  domain concept is an edit to it.
- `web/src/components/hint-content.ts` — distilled from the README.
- `web/src/lib/quiz/chapters/*.ts` — the 18-chapter quiz.
- `store/sqlite/schema/{bank,csm,centralbank}/0001_init.sql` — the relational
  mapping, and the whole schema. **Three files, one migration apiece**, because
  each kind of institution has a database of its own: a member bank's, the
  clearing house's, the settlement agent's. Each is a single `0001` with nothing
  layered on top because no database is deployed — every one this repository
  meets is ephemeral or a throwaway file, and both migrate from empty.

  Their comments are domain content, not implementation notes: which key is
  composite and why, why no balance is stored, why `entries` needs an ordering
  column, why the audit table has no foreign key, why no `asset` column in any of
  the three carries a `CHECK`. **What is ABSENT is the substance**: a bank's
  `payments` has no `cycle_id`, the clearing house's `cycles` has no
  `settlement_id`, the settlement agent has no payments table and no customers at
  all. Chapters 15 and 16 and the README's _Persistence_ section teach exactly
  these claims, so a schema change is a documentation change.

  `bank/0001_init.sql` is the canonical home for the arguments that span two
  files or three — the seq allocation rule, the absent `CHECK` on an asset code,
  the absent parent foreign key — and the others name them at the point they
  apply rather than restating them. Copying is how one fact ends up in nine
  places and then in three versions.

  **Where a comment goes is load-bearing.** SQLite stores a statement's text in
  `sqlite_master`, so a comment INSIDE the parentheses reaches a schema dump and
  one ABOVE the statement is dropped silently. Every argument about something the
  schema does NOT do therefore lives inside the statement it concerns — an absent
  constraint has no column to hang a `COMMENT ON COLUMN` from.
  `TestSchemaArgumentsReachSqliteMaster` fails if one moves back to column 0, and
  it is three cases, one per shape.

When you correct a domain fact in one layer, check and fix the same claim in the
others.

**The learner-facing layers name no repo symbol.** `hint-content.ts` and the
quiz chapters are read by someone learning banking, not by someone reading this
code, so they carry no Go package, type, method or error name, no HTTP method
and path, and no component or source file name. A rule states what an
institution does — "the submitting bank refuses an instruction that names no
counterparty" — not which function refuses it. What they DO carry is vocabulary
a banker would recognise: `CdtrAgt`, `pacs.008`, `AC01`, IBAN, `ACT/365`, and
the three schemas' table and column names, the relational mapping being one of
these layers itself. Where the honest answer is that something is not built,
say so without the symbol — the limitation is domain content. `README.md` and
`docs/` are engineer-facing and keep their identifiers; distilling one into a
hint is what drops them.

Two mechanical rules that the build will not catch for you:

- A `[[wiki-link]]` to a key that is not in `hint-content.ts` throws at runtime
  under `RootLayout` and takes **every** route in the dev app down. It is
  guarded on `NODE_ENV !== "production"`, so `next build` stays green. `npm run
  test` catches it — in hint bodies *and* in quiz explanations, which the
  runtime guard does not scan (see `concept-links.test.ts`). Run it, and load a
  page.
- `web/src/lib/quiz/diversity.test.ts` holds each chapter to 18–22 questions,
  ≥8 distinct `concept` tags, no tag more than 3×, and all three difficulty
  tiers.

## N+2 stores, and they need no setup

`go test ./...`, `make dev` and `make run` need no database and there is no
second run to keep green. There is one backend, `store/sqlite`, on
`modernc.org/sqlite` — real SQLite transpiled to Go, so the module has Go
dependencies and no external ones.

**How many databases a deployment has is N+2**: one per member bank, the
clearing house's, and the settlement agent's. No statement spans two of them, a
bank reading another bank's rows finds nothing, and a method reaching for a table
its institution's schema does not create **does not compile**. The `banks` table
lives only in a bank's own database; `settlements` lives only in the central
bank's.

**There is one store type per institution**, and that is what refuses the
crossing: `sqlite.OpenBank`, `OpenClearingHouse` and `OpenCentralBank` return
`*BankStore`, `*ClearingHouseStore` and `*CentralBankStore`, whose units of work
are `payment.BankTx`, `payment.CsmTx` and `payment.CentralBankTx`. There is no
`payment.Tx` and no `payment.Store`. Reach is 69 / 21 / 34 methods, and
[ADR-0007](docs/adr/0007-a-store-per-institution.md) is the ruling. The shape is
a CONSTRUCTOR rather than a parameter, so there is no seam where a store opened
as one institution hands out another's transaction.

**A balance is a fold in the domain, not an aggregate on the seam.** The store
answers `ledger.Tx.ScanEntries` with rows; `ledger.BookBalance`,
`ValueDateBalance`, `ValueDatedSeries`, `SubsidiaryBalances` and
`deposit.ActiveHoldTotal` are package-level folds over it, each taking the
one-method interface it needs — `ledger.EntryScanner`, `deposit.HoldLister` —
so a rule about entries is testable with no store at all. `ledger.signed` is
the sign rule and `deposit.Hold.ActiveAt` the expiry rule, each written once.
See [the design record](docs/specs/2026-08-12-derived-balances-off-the-store-seam-design.md),
which also records what the move cost: a balance is ~2.3x the SQL `SUM` it
replaced, because an `Entry` is wider than a sum.

**Every act exists twice — `X` opens a unit of work, `XTx` runs inside one — and
both halves stay.** The wrappers carry the callers (209 sites in `payment`
alone); the `XTx` methods carry the rules. A wrapper is its signature and a call
to `unit.Run` or `unit.Run2`, which turn `Update` or `View` into a call that
returns a value; the seam is named at the call site because read-only and writing
are a domain distinction. Three do not fit and each is a signal rather than a
lapse: `payment.RecordRelayed` loops its `Tx` sibling over a batch, and 20
error-only wrappers have no value to carry. Nesting is refused at runtime by
`sqlite.ErrNestedTransaction`, which is what makes the doubling safe and is the
thing to preserve. [ADR-0009](docs/adr/0009-the-doubling-stays-and-the-boilerplate-goes.md)
is the ruling, and it records why `Tx`-first was refused.

`sqlite.Shape` survives as an internal value and refuses nothing. It carries the
migration directory, the table list `Reset` empties, and the two column-list
flags — `paymentLegs` and `paymentCycle` — which are finer than any interface:
the type refuses a table an institution does not have, and the implementation
still decides which columns it writes.

`store/testenv` has **two entry points** because there are two kinds of suite.
`New` opens ONE member bank's database, which is what the `ledger`, `deposit`,
`product` and `lending` suites want: none of those four layers knows what an
institution is, so a set of databases would be more than they have anything to
say about. `NewSet` opens the whole system, which is what `payment`, `seed` and
`cmd/server` want, because every one of them drives more than one institution.

**Nothing in the domain may read across two institutions**, and the instrument
that checks the books agree anyway is `payment/recon` — the reconciliation
harness, which opens all N+2 databases at once precisely because no institution in the
system may. It is test-only by convention: `cmd/server/recon_test.go` calibrates
it against five deliberately broken states and `seed` runs it over the widest
deployment. If a change could make two institutions' books disagree, that is the
test to add.

**No institution owns the ORDER its acts go in, and `payment/flow` does.** The
four conversations — `Initiate`, `Reject`, `Return`, `Settle` — over
`*payment.Networks`, for a caller that IS the deployment: `seed` and the suites,
never an institution and nothing on the transport path, where each performs its
own half off the file it is handed. Every act stays in its own unit of work; the
package opens no transaction and holds no store.
[ADR-0008](docs/adr/0008-a-conversation-belongs-to-the-deployment.md) is the
ruling, `provision` is the same shape one layer over, and a driver that writes
the sequence out again is how the seed and the suite came to build two different
payments. Which agent plays which part is `payment.SubmitterOf`, `ReceiverOf`,
`ReturnerOf` and `CounterpartyOf` — one rule, and the receiving side and the
returning side are the same body under two act names.

**Nothing cross-checks the SQL.** There is one implementation, so anything that
would need proving against a second has to be proved before it lands. What
replaces the cross-check for the two failures that are otherwise silent is a
guard each, in `store/sqlite/sqlite_test.go`: foreign keys are really enforced,
and there is exactly one non-primary-key unique index.

`store/storetest` is not a conformance suite. It is the shared suite — written
against the domain's store and transaction interfaces, naming no table and no
dialect.

**Which institution runs which suite is not uniform.** The five capability suites
(`RunLedger`, `RunDeposit`, `RunProduct`, `RunLending`, `RunPayment`) take a
**bank's** store, because a bank's schema is the only one holding every table
they reach; the clearing house and the settlement agent have separate suites
over the tables their schemas do hold, and each takes its own store type. Roughly
four fifths of the package is the bank's.
`store/sqlite/conformance_test.go` is where each suite's store is opened.

Three things about the store are worth knowing before changing anything near it:

- **A real `BEGIN`/`COMMIT`.** "These writes commit or roll back together" is a
  claim about the code, and it holds on a fresh checkout with no setup.
- **Each `0001_init.sql` records its reasoning INSIDE each statement's
  parentheses**, because SQLite drops a comment that sits above one. See the
  section above.
- **The ephemeral store hides read-then-write defects.** On it a retry's read
  blocks until the winner commits, so a loser reaches the domain's guard however
  the code underneath behaves; only a file, under WAL, lets a reader past an
  uncommitted writer. Anything you measure about ordering must open a file.
  `TestTheRetryBudgetOutlastsASlowWriter` is the one that does.

## A modular monolith: institutions are parts, the deployment is the whole

One process plays every institution, and it is built out of parts that each play
exactly ONE. Reading what a clearing house does is reading one institution's
code; the process it happens to share is somebody else's concern.

**The split runs through seven layers** and all seven hold: the database (N+2),
the store type (`OpenBank` / `OpenClearingHouse` / `OpenCentralBank`), the domain
handle (`BankNetwork` / `ClearingHouseNetwork` / `CentralBankNetwork`), the
transport state (the EBICS queue is a table in the two hosts' schemas), the HTTP
surface (`api/bank`, `api/csm`, `api/centralbank`), the listener (one per
institution, its own port and its own `/ebics`) and the package: `node/bank`,
`node/csm` and `node/centralbank`, one institution apiece, with `cmd/server`
holding the deployment that drives them. See
[ADR-0010](docs/adr/0010-an-institution-holds-what-it-does-the-deployment-holds-the-order.md)
and [the design record](docs/specs/2026-08-13-a-package-per-institution-design.md).

**Each node is handed a `node.Env` at construction** — a clock, a logger, a
journal, an id minter and the two addresses it dials — and holds nothing else
about the process it runs in. `grep -rn 'Deployment' node/` finds nothing, and
that is the claim to preserve. The deployment's own acts are served on an
institution's listener through an `Operator` interface (`api/csm`,
`api/centralbank`), so the type system says whose act each one is.

**Institutions already talk over the wire.** `ebics.Client` POSTs to a configured
URL, which in this deployment is loopback. Everything on the payment path — files
up, shares down, settlement instructions, returns — already crosses a network
boundary. Nothing on that path is an in-process call, and nothing new may become
one.

**What reflects reality is separated; what simulates a deployment is not.** The
business day, its phase order, the journal, `Reset` and the seed are the
DEPLOYMENT'S, and they are allowed to see every institution at once — that is
[ADR-0008](docs/adr/0008-a-conversation-belongs-to-the-deployment.md)'s rule
(`payment/flow`, `payment/recon`) one layer up, and it is why they are not being
taken apart. What must hold is the DIRECTION: an institution's code may not know
that a deployment exists. Anything institution-shaped that reaches for one is
either the environment (a clock, a logger, an id minter, a report sink, the two
addresses it dials — pass it in) or a crossing that belongs on the deployment.

**A bank submits only what it is the submitting agent of.** `bank.Bank.Submit`
answers the scheme, the on-us and the membership questions from that bank's own
rows — its registry, its routing directory, its register — and refuses an
instruction the scheme has the other side send (`payment.ErrNotTheSubmittingAgent`).
`Deployment.Submit` is the ROUTING question and only that.

**And it collects its own routing directory.** The clearing house PUBLISHES its
roster on its own EBICS host and a member collects it under `HRD` — a snapshot,
not a queue, so collecting it empties nothing and every member gets the same
file. It is the one file on this transport that is not ISO 20022, and
[the design record](docs/specs/2026-08-13-a-roster-download-order-type-design.md)
argues why a routing table is not one. No crossing is left on this path: nothing
reads another institution's rows, and the DEPLOYMENT holds only the order —
publish, then collect, which is the day's opening pair.

## What is planned, and what was already decided

`docs/expansion-roadmap.md` is forward-looking: verified defects, the build
sequence, the standing catalogue of domain gaps, and structural work — each with a
status of `todo`, `spec`, `plan` or `wip`.

`docs/specs/` holds one design record per sub-project, named
`YYYY-MM-DD-<slug>-design.md` and linked from the roadmap. A decision, the
alternative it rejected and what that cost live **there**, which is why a comment
in the code states the rule and not the history. Read the relevant record before
changing the shape of anything; write one before a change large enough to need
phasing.

`docs/adr/` is narrower and outlives the sub-project that produced it: one
numbered record per decision a later reader has to know about before changing the
shape of anything near it. A spec is a plan with phasing; an ADR is a ruling in
the present tense. If you find yourself wanting to write "this used to work the
other way" beside the code, that is what an ADR is for. `docs/adr/README.md`
holds the index and the distinction.

## Agent skills

### Issue tracker

Issues live in this repo's GitHub Issues (`raphi011/cbs`), driven via the `gh`
CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

The five canonical roles, each label string equal to its name. See
`docs/agents/triage-labels.md`.

### Domain docs

Single-context: `CONTEXT.md` and `docs/adr/` at the repo root. See
`docs/agents/domain.md`.
