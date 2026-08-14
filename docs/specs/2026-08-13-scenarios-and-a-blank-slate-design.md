# Design — scenarios, and a deployment that starts blank

Based on `main` at `94e5996`. This replaces the sample dataset with a **base
state** and a set of **triggerable scenarios**, and it is a prerequisite for
[the scheduled business day](2026-08-13-a-scheduled-business-day-design.md):
that sub-project cannot pick a base date while the seed dictates one.

## The defect, stated once

**The seed is one large fixed scenario that runs at boot, and half of it bypasses
the transport.**

`seed.Populate` builds four banks, thirteen customers, five facility states, a
product catalogue, holds, snapshots, mandates, four cycle phases and a
general-ledger showcase, dated from `BaseDate` — `2025-09-15`, roughly a year
behind whatever day it is. A reader gets a full network and no way to build a
second one; an operator who wants to see a rejection has no act that produces
one.

The bypass is the sharper half. The builder has two ways to make a payment and
they are not equivalent:

| helper | route | messages produced |
| --- | --- | --- |
| `b.initiate` | `payment/flow` | **none** |
| `b.submit` | `Deployment.Submit` | a `pacs.008` at the next cut-off |

`flow.Initiate` calls `submitter.SubmitPayment`, `receiver.AcceptInbound`,
`csm.RecordRelayed`, `csm.AcceptAtCSM` and `submitter.AcceptAtBank` in turn. No
EBICS, no file, no envelope, nothing journalled. The package exists because the
seed needed history without a transport, and
the interbank-conversation design is the ruling
that gave the order an owner.

That bypass has already produced one silent defect. The whereabouts design
records it: a payment the build left in an open cycle was settled by the first
advance and **delivered to nobody**, three times on every fresh deployment,
because release hands over the share the clearing house BUILT when it took an
uploaded file in and the seed uploads no files. Three guards now stand where
there were none. The bypass that produced it is still there.

## The design

**Boot leaves a base state. Everything else is a scenario an operator triggers,
and every scenario drives the real doors.**

### The base state

What a fresh deployment holds, and nothing else:

- Four banks provisioned — founded in their own books, given a settlement
  account at the central bank, put on the clearing house's roster.
- Each one subscribed, so every member holds the routing table.
- Each one's product catalogue priced.
- Each one **prefunded**: vault cash, and that cash lodged onto reserve.

No customers, no accounts, no payments, no facilities, no mandates, no cycles
beyond the one open per scheme.

### Prefunding needs an act that does not exist

Vault cash enters this system through **one** door: `bank.Deposit`, a customer
paying money in. `b.fund` is that call and `b.lodge` moves the proceeds onto
reserve. A bank with no customers has nothing to lodge, so "prefunded bank, zero
customers" is not currently a state this system can reach.

**A capital injection is the missing act**: debit Vault Cash, credit Equity, at
provisioning. It is one balanced posting and it is what a bank's own reserves are
actually funded from before it has a single depositor. The alternative — one
nominal funding customer per bank — reaches the same balances and makes the base
state a staged photograph rather than a true statement, which is the distinction
this repository is built on.

The absent act is a domain gap independent of this sub-project: there is no way
to capitalise a bank at all today, and the chart of accounts has the Equity side
to post to.

### A scenario is a script of operator acts

```go
// A Scenario is a named thing an operator can make happen. Run drives the
// deployment's own doors, in order, as an operator would.
type Scenario struct {
    ID, Name, Description string
    Run func(context.Context, *Deployment) error
}
```

A registry in `cmd/server`, because a scenario is the DEPLOYMENT's — the same
standing as `payment/flow` and `payment/recon`, and the reason no institution can
reach one. Served on the operator surface beside `POST /clock/day` and
`POST /admin/reset`: `GET /scenarios` lists them, `POST /scenarios/{id}` runs
one.

**Every act a scenario performs goes through a door an operator has.**
`Deployment.Submit`, `Deployment.Return`, `Deployment.Lodge`, the bank listeners'
own acts, and the clock. Never `payment/flow`, and never an institution's network
method directly.

This is the rule the sub-project exists to establish, and it is not stylistic. A
scenario built on `flow` produces a payment that reads as settled on every screen
and **appears in no message log and on no graph**, because no file was ever
built. The first thing anyone would do with such a scenario is the one thing it
cannot support.

### A scenario advances the shared clock

There is one clock and one business date; a scenario that needs days to pass
moves it, and every viewer moves with it. Arrears, capitalisation and anything
value-dated need this, and the alternative — back-dating — is refused elsewhere
in the system on purpose (publication is forward-only, and *reject future booking
dates* is designed).

The costs are real and accepted:

- **Two scenarios cannot run side by side.** Each is serialized behind the same
  lock `AdvanceDay` and `Reset` take.
- **Triggering one moves the world.** A second browser tab sees the date jump.
  The UI says so before it runs, rather than the surprise being discovered.
- **A scenario is not undoable.** `Reset` is the only way back, as it is for the
  day.

### The four that ship

Each is chosen for what it makes visible, not for the status it leaves behind.

| scenario | what it does | what it produces |
| --- | --- | --- |
| **A payment between two banks** | open two accounts, fund them, submit, run to settlement | the whole chain: `pacs.008` → `pacs.002` ACCP → `pacs.009` → `camt.025` and `camt.053` → the released share → the credit leg |
| **A payment that is rejected** | submit quoting a creditor address no bank resolves | a genuine `pacs.002` carrying `AC01`, and the payer's money back |
| **A settled payment returned** | the first scenario, then `Deployment.Return` | a `pacs.004`, and the second hop routed to the other bank |
| **A borrower falls behind** | open a term loan, disburse, advance past two due dates | arrears, and the accrual that ran over the days between |

**A scenario reaches its outcome naturally or it is not worth having.** The
rejection is submitted with an address that genuinely does not resolve, so the
clearing house refuses it from its own rows and the reason code is one it
decided. An operator calling `POST /payments/{id}/reject` would leave the same
status and teach nothing, and §7c's whole pitch is that the message log is the
natural home for explaining a `pacs.002` reason code *at the point it was
returned*.

### The old seed becomes a scenario

`seed`'s current build is re-homed as **"Demo network"** — the same code, behind
a trigger instead of at boot. Nothing is rewritten and nothing is lost.

This is the whole mitigation for the cost below, and it is what makes the blank
slate affordable. The `runDays` defect it would also have demoted — *the seed
closes a business day the deployment has not reached* — has since been fixed
outright rather than made optional, so nothing here rests on it.

## What it costs

**Every screen boots empty.** Holds, snapshots, account status, mandates, the
catalogue, five facility states, the GL showcase, ageing, reconciliation — the
seed is what currently makes all of them demoable. Until "Demo network" is one
click away, the app is a worse first impression than it is today. The two land
together or the sub-project is not done.

**`payment/flow` drops to one caller.** `seed/seed.go` and
`payment/system_test.go` are the two today; the seed stops being one.
The interbank-conversation design is not
reversed — the suite still needs the conversation to have an owner — but the
package becomes a test helper, and that should be recorded rather than
discovered.

**`seed_test.go` is 1032 lines against a fixture that is about to move.** Most of
it becomes the Demo network scenario's test unchanged; what it asserts about
*boot* becomes an assertion about the base state, which is a much smaller claim.

## What this does not do

- **No isolation between scenarios.** One clock, one set of databases, one lock.
  Two scenarios run in sequence and their effects accumulate, which is the same
  bargain the business day already makes.
- **No scenario authoring.** They are Go functions in a registry, not data. A
  scenario a reader can write is a different sub-project and probably a bad one.
- **No undo.** `Reset` rewinds everything.
- **`BaseDate` is not deleted.** It stays as the base state's anchor until the
  scheduled business day decides what a fresh deployment starts at — which is
  that sub-project's decision and needs this one to have landed first.

## Tasks

1. **The capital injection.** One balanced posting at provisioning, in
   `provision`, plus the storetest case. Independent of everything below and can
   land first.
2. **The base state.** `seed.Populate` reduced to banks, subscription,
   catalogues and prefunding. Its test reduced with it.
3. **The registry and the two routes.** `GET /scenarios`, `POST /scenarios/{id}`
   on the operator surface, and the lock they share with `AdvanceDay`.
4. **The four scenarios**, each with a test that asserts what it produced rather
   than only that it returned — the rejection's test asserts an `AC01` reached
   the payer's bank, not that the status is `Rejected`.
5. **"Demo network"**, the current build moved behind the trigger.
6. **The picker**, in the topbar beside the day controls, with the warning that a
   scenario moves the date.

Task 4's tests are the sub-project. A scenario that returns `nil` having produced
no file is exactly the defect this design exists to prevent, and only its test
can tell.

## Documentation layers

`README.md`'s sample-dataset description, `seed/doc.go`, and the app's empty
states — which currently cannot be reached and will now be the first thing every
reader sees. `CONTEXT.md` gains **scenario** if the word is going to be used in
the UI. No quiz chapter and no hint body changes: a scenario is a property of
this app rather than of banking, and the learner-facing layers name no repo
machinery.

## What shipped, where it differs

Three things the design could not know before it was built.

**The rejection scenario refuses on `TM01`, not `AC01`.** The table above says a
creditor address no bank resolves produces a genuine `pacs.002` carrying `AC01`.
It does not, and cannot: `AC01` maps from `ErrAccountNotInParticipant`, which is
the RECEIVING bank's judgement, made when it applies a released file — that is
after settlement, and its outcome is a `pacs.004` sent back rather than a
refusal. The submitting bank cannot make it either: it resolves the counterparty
*bank* from the address's bank code through its own directory copy, and knows
nothing about which accounts that bank holds.

The refusal a clearing house genuinely decides out of its own rows is `TM01` —
the window was shut. The scenario steps the day to the clearing house's own
cut-off, submits, and carries the file: the clearing house has no open cycle to
take it into and says so. The rule the design was defending survives intact and
is the thing to keep — **the outcome is reached naturally and nothing in the
scenario names a code** — and the scenario is renamed for what it actually
teaches: *A payment that misses the cut-off*.

**The demo network was rewritten after all.** The design says it moves behind a
trigger unchanged. It could not: the whole point of this sub-project is that a
scenario names the files that carried it, and a demo network still built on
`payment/flow` would have left the reported defect in place on the one dataset
every reader sees first. It now submits through `Deployment.Submit`, settles by
advancing the business day and returns through `Deployment.Return`, and its
payment history had to move **before** the lending months rather than after —
five months of business days would otherwise carry every payment meant to be left
in flight to settlement, and the payer's overdraft arithmetic depends on the
original order.

**Provisioning was not idempotent, and its own test claimed it was.** Found by
the capital subscription's idempotency case. A second `provision.Bank` for the
same spec re-founded the bank, building a whole new chart of accounts and
orphaning every balance on the first; the standing test asserted the ids and the
roster count and never the chart. `provision.Bank` now founds only a bank it does
not already find. This is a defect the design did not know about and would have
hit as soon as anything ran the base state twice over a live store.

## Verification

`go test ./...`, and three claims that need a test each:

- **The base state is reachable and complete.** A fresh deployment can submit a
  payment after a scenario opens the accounts, which is the whole point of
  prefunding.
- **Every scenario moves at least one file.** Asserted against the journal, and
  it is the guard against a scenario drifting back onto `payment/flow`.
- **`payment/recon` is clean after each one.** The reconciliation harness over
  the whole deployment, run at the end of every scenario's test. It is the
  instrument that would catch a scenario leaving two institutions' books
  disagreeing, which is precisely what the seed's bypass did.
