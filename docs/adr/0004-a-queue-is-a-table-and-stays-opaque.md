# ADR-0004: A download queue is a table, and the bytes in it stay opaque

**Status:** accepted
**Date:** 2026-08-12
**Sub-project:** [What an institution is holding when the process ends](../specs/2026-08-12-held-files-durability-design.md), phase 3

## Context

[ADR-0003](0003-an-institutions-obligations-live-in-its-database.md) made the
clearing house's own obligations rows and named the case it could not close: the
file transport. `ebics.Server` held every subscriber's download queue and the log
of every order uploaded to it in process memory.

That is the same defect on the far side of the release. A cut-off settles, the
reserves are final, and each receiving bank's instructions go into that bank's
download queue — where they wait, because EBICS has no push and a bank collects
when it runs its own day. A process that ended in between lost the files with the
money already moved, and **nothing refused anything**: the shares were present,
the release happened, and the queue was simply gone. ADR-0003's guard could never
have caught it, because no institution is waiting for anything at that point.

The order log is a second, smaller loss of a different kind. A subscriber that
uploads is told `EBICS_OK` and goes away; HAC is the only place it can ever learn
what became of the file. A log a restart emptied answers nothing. And the log is
also the hosting institution's **work list**, so a settlement instruction that
had arrived and not yet been worked through disappeared with it.

**The argument against storing any of this was already written down**, in
`csm/0001_init.sql`, and it is a real argument rather than a leftover:

> One institution holding bytes addressed to another is not a crossing while the
> bytes are OPAQUE to the holder; a table of them is a store this institution
> reads, and the first query anybody writes against it is a clearing house
> looking inside a file it is only carrying.

## Decision

**The queues and the order log are rows in the hosting institution's own
database, and what keeps the bytes opaque is the interface rather than the
absence of a table.**

Two tables, in `csm/0001_init.sql` and `centralbank/0001_init.sql` and in no
other schema, because those are the two institutions that are DIALLED. A member
bank hosts nothing, and cannot ask: `EBICS` is a method on
`sqlite.ClearingHouseStore` and `sqlite.CentralBankStore` and on no third type,
so there is nothing to call it on. See
[ADR-0007](0007-a-store-per-institution.md), which turned that refusal from a
runtime sentinel into a missing method.

- `ebics_queue` — files addressed to a subscriber and not yet collected
- `ebics_orders` — every order a subscriber has uploaded, and what became of it

The port they are reached through is **declared by `ebics`**, which imports
nothing from this repository. Its whole vocabulary is subscribers, order ids,
order types and opaque payloads; there is nothing in it that can name a payment,
an amount or an agent, and no method on any institution's transaction type
reaches these tables at all.
So the clearing house looking inside a file it is only carrying is not a query
somebody might write carelessly — it is a query there is nothing to write with.

**Enrolment stays in memory**, and that is the one thing here that is not
persisted. It is derived: who may dial a host is who the clearing house's roster
names, plus the clearing house itself at the settlement agent, and provisioning
admits every member again at each boot. Nothing is lost by not writing down a
fact that is rebuilt from a row that is.

## Consequences

**A deployment resumes.** `cmd/server`'s two restart tests are the only ones in
the repository that can fail for the reason this record and ADR-0003 exist:
`TestACutOffSettledAfterARestartStillReachesEveryReceivingBank` no longer
re-instructs by hand, because the `pacs.009` the cut-off uploaded is still in the
settlement agent's work list; and
`TestFilesReleasedBeforeARestartAreStillCollectedAfterIt` drops the process
between the release and the members' collection, which is the loss ADR-0003
explicitly left standing.

**`POST /cycles/{cid}/settle` becomes narrower and stays.** It was the remedy for
two states that looked alike from the console: a cut-off the settlement agent
REFUSED, and one whose instruction a restart had eaten. Only the first is
reachable now. The route is unchanged and the second reading of it is gone.

**An order id is unique at its host for the life of the database.** The counter
is a row in `id_sequences` rather than `MAX(seq)+1` over the queue, because a
queue row is deleted when it is collected and a counter derived from the rows
would hand out an id the host has already acknowledged under.

**Every act on a host is a unit of work, and none of them is the caller's.** A
file is put on a connection outside the transaction that decided to send it, so
a queue row committed alongside a decision the store then rolled back cannot
happen. The cost is that answering an order is a SECOND unit of work after the
work itself: a failure there leaves the order on the institution's list and it is
worked again on the next day. That is reported rather than swallowed, and it
cannot move money twice — the clearing house records a payment before it does
anything else with a file, and each of the settlement agent's three acts carries
an idempotency key of its own.

**A held file is discharged one share at a time, and only after its own
hand-over.** Releasing a settled cut-off writes both tables — `ebics_queue` at
the receiving bank's end, `held_files` at the clearing house's — and by the
decision above no statement can write both. So the release is two units of work
per share, and the order they run in is the whole of what makes it safe: queue
first, discharge second. A share whose queueing failed stays, because it is still
owed to the bank it names against reserves that have already moved; a share that
was queued must go, because one left behind is released again by a redelivered
answer and a bank handed the same instructions twice credits the same customer
twice. Discharging a cut-off's shares together satisfies neither. See
`payment.DropHeldFile`.

**A download is still not a receipt.** The queue row is deleted in the same
transaction that reads it, so a client that dies after the response is written
has lost the files. Real EBICS downloads in three phases and the client's receipt
is what makes that recoverable; this models none of it, and the absence is now a
statement about one transaction rather than about a slice in memory.

## Alternatives rejected

**Leave the queues in memory and have the operator re-drive.** This is what the
system did, and there is no act to re-drive with. The clearing house drops a
share once it has handed it over — it must, or a redelivered `ACSC` would hand
the same instructions to the same bank twice — so a file lost from a queue is one
nothing in this system can build again.

**Persist the queues through `payment.Tx`.** The tables are in an institution's
database, so the institution's own store interface is where they would naturally
go. It is exactly the crossing the original argument warned about: a
`payment.Network` with a method that reads a queue is an institution able to read
a file it is only carrying, and the guard would be nothing but everyone's
discipline. A port declared by `ebics` cannot express the query.

**One table with a direction column.** An uploaded order and a queued file are
both "a file at this host with an order id", and they are emptied by opposite
rules: a queue row lives until it is collected, and a log row lives for ever,
because an answer that disappears is not an answer. One table would need every
read to carry the discriminator and would make the retention rule a `WHERE`
clause.
