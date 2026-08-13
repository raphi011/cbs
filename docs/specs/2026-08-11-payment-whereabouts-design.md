# Design — where a payment is: the lifecycle trail and the hub that holds it

Branch `spec/payment-whereabouts`, based on `main` at `6326229`.

A reader initiated a payment, looked for it on the clearing house's payments
screen, and did not find it. They advanced the day, looked at the cycles screen,
and found the two cycles at the bottom of the table holding **0 payments** each.
Both observations were correct and the system was working. The payment was in the
payer's bank's hub, then in a file, then in the cycle that had been open since the
seed — three places, none of which any screen names.

This is a web-only sub-project. No route is added, no column, no status, no
persisted field: every fact it renders is already answerable by the listeners as
they stand. What it changes is **when** the app says a thing it already knows.

## The complaint, reproduced

The deployment that produced it, read back off its own listeners:

```
pay_NORDSESSXXX_721   NORD -> AURO   22.00 EUR   created 2026-05-18
  clearing house:  Settled, cycleId = cyc_52
  both banks:      Settled

cyc_52   sepa.ct   Settled   3 payments   opened 2025-09-18
cyc_70   sepa.ct   Open      0 payments   opened 2026-05-18
cyc_72   sepa.dd   Open      0 payments   opened 2026-05-18
```

`cyc_52` was the open `sepa.ct` cycle the seed left standing. Nothing had advanced
a day since, so it stayed open for eight months, took the payment in during phase
2 of the one advance the reader ran, and was closed and settled by phases 3–5 of
that same advance. `cyc_70` and `cyc_72` were opened by `Deployment.AdvanceDay`'s
last phase (`cmd/server/day.go:305`), after all of it, for the clearing day that
has not happened yet. They are empty **by construction** and will read 0 to every
reader who advances a day and immediately looks.

So the screen presented the reader with one cycle that was accepting payments and
looked like a relic, and two that could not have accepted anything and looked like
where a payment ought to be. Nothing on it distinguishes the three.

## What is essential, and what is opacity

The friction the reader hit is the subject matter. A payment sits in its bank's
hub until a cut-off; the clearing house learns of it from a file and not from the
customer; a cycle batches, nets, then settles; a receiving bank is handed nothing
until the money is final (settle-before-release). Removing any of that would be removing the
thing this repository exists to teach, and the answer to "it is confusing" is not
to make a payment appear instantly at the clearing house.

What is not the subject matter:

- A payment in a bank's hub is rendered **nowhere**, on any screen, in any
  persona. `GET /payments/pending` exists on every bank's listener and the
  frontend has never called it.
- The submit toast says `Payment initiated (pay_…)` and disappears. It names
  neither the institution now holding the instruction nor the act that moves it.
- The clearing house's empty state — *"No payments yet. Initiate one between two
  funded participants"* — is wrong in both halves: the reader had initiated one,
  and funding is not what gates its appearance here.
- `Initiated` is the one status that is not a fact about the network. Every other
  value describes what has happened to the payment; this one describes whose desk
  it is sitting on, and the screens render it as a bare badge like the rest.
- The cycles table's `Opened` column answers a question nobody asked, in place of
  the one everybody asks: which of these takes the payment I make next.

## The knowledge is already in the app, keyed to the wrong moment

`hint-content.ts` already answers this reader, in the voice this repository wants:

> When a customer instructs their bank, **nothing is sent**. […] The payment is
> *Initiated*, in no cycle, and the payee's bank has never heard of it. That is a
> state it rests in rather than passes through, because nothing in a bulk scheme
> happens on a timer.
> — `payment-hub`

That paragraph is the reader's whole answer and it is two clicks away behind a `?`
on a screen they had no reason to open. `payment-lifecycle` goes further and tells
them the three copies legitimately disagree. The defect is placement: the app
explains the mechanism to a reader who is browsing and says nothing to the reader
who is looking for their payment.

So no hint body is rewritten and no key is added. The work is to put the existing
explanation at the moment of confusion, and to have the screens state which of the
states those hints describe a given payment is actually in.

## Decisions taken, and what they cost

**1. The trail is read from the audit log. No new route, no new column.**
`GET /payments/audit?entity=<payment id>` is served by every institution
(`api/audit.go:41`), and the events are already there:

```
clearing house, pay_NORDSESSXXX_721:  initiated(49) accepted(50) cleared(55) settled(60)
NORDSESSXXX,    pay_NORDSESSXXX_721:  initiated(440) accepted(445) settled(448)
```

The cost is **ordering**. Every event of one business day carries that day's
timestamp — all four above read `2026-05-18T09:00:00Z` — so a trail cannot be
ordered by time. It orders by `seq`, which is a total order within **one**
database and meaningless between two. That is a constraint the next decision turns
into the design rather than working around.

**2. A console shows its own institution's trail and never assembles two.**
The clearing house's page renders the clearing house's four events; a bank's
screens render what that bank was told. Nothing merges them.

The rejected alternative is an omniscient trail stitched together in the browser
from all N+2 listeners. The browser is not a domain layer — it already dials every
operator for the lobby — so this is not forbidden the way it is forbidden of
`payment/recon`'s callers. It is refused because it would be **teaching the
opposite of the system**: `payment-lifecycle` tells the reader that three copies
in three databases legitimately disagree, and a single merged timeline would be
the app quietly asserting that they do not. It would also have to invent an order
between two `seq` counters that have none.

The cost is real and is accepted: seeing both sides means changing persona, and a
disagreement between two copies is not visible on any screen. `payment/recon` is
the instrument for that and it stays test-only.

**3. Each step names who holds it; the last step names the act that moves it.**
This is the trail's payload and the reason it is not just a timestamped list:

```
Initiated   the payer's bank posted the debtor leg and put the
            instruction in its hub                              [hub & cut-off ?]
            ── waiting here. Its bank's next cut-off sends it.
               ▸ Advance day
```

A payment that has stopped says what would move it and **whose act that is**. A
payment that is finished says nothing extra. The act named is the operator's, in
the words of the button that runs it — the topbar's `Advance day` — and never the
name of a route, a function or a phase.

**4. The hub gets a section on the bank's payments screen, from the route that
already exists.** `GET /payments/pending` on that bank's own listener, above the
table, empty on most days and therefore silent on most days.

It goes on the **bank's** screen and not the clearing house's, because the
clearing house does not know what is in a bank's hub, and a console that showed it
would be lying about who knows what — the same reason the roster lives at the
clearing house and the `banks` table does not. The cost is that the clearing
house's console, which is where an operator spends their time, still shows nothing
for a payment that has not been sent. Decision 5 answers that; weakening this one
would not.

**5. The clearing house's empty state stops asserting funding and states the
gate.** It says what is true — that this institution learns of a payment when a
member bank uploads a file, and that advancing the day is what makes one — and
links `bulk-file`. Same for the bank's own empty state, which is honest already
and gains the pending section above it.

**6. Cycles: badge the one that is accepting, and say what an empty new one is
for.** The open cycle per scheme carries an `accepting` badge; open cycles sort
first; a cycle with no payments that was opened by the day just run says it takes
the next clearing day's. `Opened` stays — an eight-month-old accepting cycle is a
true and instructive thing to be able to see, once it is labelled.

**7. No new hint keys.** `payment-hub`, `bulk-file`, `download-queue`,
`business-day`, `payment-lifecycle` and `clearing-vs-settlement` cover every step
the trail renders. Adding a key would be saying a seventh time what is said well
in six places, and `concept-links.test.ts` would be the only thing that noticed.

## The tasks

1. **`usePaymentTrail`** over the existing `paymentAudit({ entity })`, and a
   `PaymentTrail` component: the ordered steps for one institution's copy, each
   with the institution that holds it, a hint link, and — on the last step of an
   unfinished payment — the act that would move it. Rendered on the clearing
   house's payment detail page, which is the only detail page there is.
2. **The pending section** on `/bank/[pid]/payments`, from `GET /payments/pending`
   on that bank's listener. New endpoint fn, hook and query key; the key nests
   under the bank's subtree so a submission invalidates it.
3. **Wording**: the submit toast (three call sites — the clearing house's initiate
   form, the bank's, the customer's send-money dialog) names where the instruction
   now is and what sends it; the two empty states per decision 5.
4. **The cycles screen** per decision 6. Independent of 1–3 and can land first.

Task 1 is the sub-project. Tasks 2–4 are each an afternoon and each would have
answered the reader on its own.

## What this does not do

- **No message log.** Showing the `pacs.008` that actually carried the payment is
  roadmap §1 (7c), and it is the natural successor to this: the trail is the
  scaffolding a message viewer hangs off, one document per step.
- **No bank-side drill-down.** `/bank/[pid]/payments` keeps its ruling that a
  payment's detail page is the clearing house's. When that reverses, the same
  `PaymentTrail` takes the bank's audit unchanged, which is decision 2 already
  paid for.
- **Nothing about the day report.** `2 files · 5 decisions` in a toast is thin,
  and a reader wanting to know what a day did to *their* payment is better served
  by the trail than by a better toast.
- **No new persistence.** Both in-memory caveats the previous sub-project left
  behind (the clearing house's held output files, a bank's hub) are untouched, and
  the pending section will read empty after a restart for exactly that reason.

## Tests

`npm run test`, `npm run typecheck`, `npm run lint`, `npm run build`, and the
screens driven in a browser against a running backend — the frontend has no
component runner, so the trail is verified by looking at it. `nav-integrity` is
unaffected: no route is added. `concept-links` is unaffected unless a hint body is
edited, and decision 7 says none is.

## Found while writing this, and since fixed

In the deployment above:

```
pay_VERDITMMXXX_119   VERD -> AURO   1400.00 EUR   in cyc_52
  clearing house:   Settled
  VERDITMMXXX:      Settled
  AURODEFFXXX:      Initiated      <- credit leg never applied
```

Aurora's audit held `payment.initiated` for that payment and nothing after it,
against a cycle that settled: reserves moved and the payee was never credited.
Three payments on every fresh deployment, silently, on the first advance.

The cause was the seed. Release hands over the share the clearing house BUILT
when it took an uploaded file in, and the seed uploads no files — it plays every
institution itself — so a payment it left in the OPEN cycle was settled by the
first day and delivered to nobody. Fixed by closing every cycle the build opens
and leaving two empty ones for the day to use, which costs the dataset its
`Accepted` status at the clearing house; the submitting banks' copies still carry
it. Three guards now stand where there were none: `ClearingHouse.unhanded`
reports any payment a cut-off settled and released to nobody, `payment/recon`
calls a receiving copy still `Initiated` under a settled cycle a break, and
`seed`'s `TestTheBuildLeavesNoPaymentInAnOpenCycle` refuses the state at source.

It stays recorded here because it is the sharpest argument for decision 2's cost:
no screen in this app could show that disagreement, and the only instruments that
find it are a test-only harness and a line in a day's report.
