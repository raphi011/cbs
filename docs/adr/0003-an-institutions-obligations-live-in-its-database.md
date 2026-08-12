# ADR-0003: An institution's unfinished obligations live in its own database

**Status:** accepted
**Date:** 2026-08-12
**Sub-project:** [What an institution is holding when the process ends](../specs/2026-08-12-held-files-durability-design.md), phase 2

## Context

Between one file and the next, an institution in this system is holding things it
has taken on and not yet discharged:

- each receiving bank's **share** of every file the clearing house has taken in,
  which [ADR-0002](0002-settle-before-release.md) requires it to hold until the
  cut-off carrying it settles;
- a **`pacs.004`** uploaded to the settlement agent and waiting for the answer,
  which must not be queued onward before finality because a bank that posted
  against a return the agent then refused would have moved a customer's money for
  nothing.

Both were maps on the actor. A map is a fact about a process, and a process ends.
What ended with it was not a cache that could be rebuilt: the money that moved
against these obligations stays moved. A restart between a settlement and its
release left the reserves final and no receiving bank ever handed the
instructions it had to apply — a payee never paid, the amount standing in that
bank's clearing suspense, and no act in this system able to move it.

## Decision

**What an institution owes and has not yet handed over is a row in that
institution's own database.**

Three tables in `csm/0001_init.sql`, in the clearing house's schema and in no
other, because no other institution has anything of the kind — a member bank
holds no share of anybody's file and the settlement agent has no payments at all:

- `held_files` — one receiving bank's share of an uploaded file
- `held_file_transactions` — which of that file's transactions are that share's
- `held_returns` — one `pacs.004` waiting for the settlement agent's answer

What is stored is **the submitting bank's file as it arrived**, plus the
positions within it, and not the file that will travel. ADR-0002 requires the
share to be cut twice — built at ingestion, cut again at release against what the
cut-off actually settled — and a rendered output file cannot have a rejected
payment taken back out of it.

The tables carry **no foreign key to `cycles`**, though the parent is in the same
database and the constraint could be written. What this institution owes a bank
has to be readable and removable whatever became of the batch row that names it.

## Consequences

**A cut-off settled after a restart still reaches every receiving bank.** That is
the claim, and `cmd/server/restart_test.go` holds it: it drops the process
between a cut-off and its settlement, opens a second one over the same files, and
asks whether the banks were handed their instructions. Its sibling in the same
file is [ADR-0004](0004-a-queue-is-a-table-and-stays-opaque.md)'s, and between
them they are the only tests in the repository that can fail for the reason these
two records exist.

**`ErrCycleNotReleasable` becomes a guard on something that should not happen.**
It refuses a cut-off whose payments have no share behind them, which was a
restart's ordinary outcome and is now reachable only from a process that ends
between accepting a file's transactions and recording the share — two units of
work, in that order, because a share names the cut-off the acceptance chose. The
guard stays. A refusal that never fires is what a guard on a closed defect looks
like.

**The file is stored once per destination.** A file addressed to three banks is
three rows carrying the same bytes. The share is the unit that is built, cut and
released, and normalising the bytes out from under it would save space in a table
whose rows live for the length of one cut-off.

**A leaked row now outlives the process that leaked it.** The two cases where a
held return is never answered — a return the settlement agent could not process,
an answer the handler could not act on — used to disappear at the next restart.
They are rows now, so they accumulate and an operator can find them. That is the
better failure and it is still a leak.

**Nothing here is a lock any more.** The shares were guarded by a mutex because a
business day and an HTTP request both reached them; a store transaction is what
orders the two now.

## What this does not close

**The EBICS host's queues and its order log.** A file released into a receiving
bank's download queue and not yet collected is lost with the reserves already
moved, which is this defect on the far side of the release, and the order log is
an audit trail that a restart empties. This ruling is about what an INSTITUTION
owes; the transport between them is
[ADR-0004](0004-a-queue-is-a-table-and-stays-opaque.md), which closes both.

**A member bank's hub is still memory.** Instructions with committed debtor legs,
waiting for that bank's own cut-off: a payer debited, money in clearing suspense,
and no file ever uploaded. It is one member bank's obligation and belongs in that
bank's own database by exactly this ruling. Naming it here is what stops this
record reading as the whole story.

## Alternatives rejected

**Rebuild the shares from the payments rows after a restart.** A cycle knows
which payments are in it, so a share could in principle be re-derived. It cannot:
a share is a set of positions in a document the submitting bank sent, and nothing
in any institution's rows records which file a payment arrived in or where in it.
Re-deriving would mean inventing a file, and a receiving bank cannot tell an
invented instruction from a real one.

**Store the rendered output file instead of the submitter's.** Simpler to
release, and it releases transactions an operator has since rejected out of the
open cycle. This is ADR-0002's own rejected alternative and persistence does not
change it.

**Leave it in memory and refuse to settle a cut-off whose shares are gone.** This
is what `ErrCycleNotReleasable` alone buys, and it is worth having — it turns a
loss into a cycle an operator has to look at. It moves the cliff a few seconds
later in the same day rather than removing it: a restart AFTER the settlement and
before the banks collect loses the files with the reserves already moved, and
nothing refuses anything, because the shares were present and the release
happened.
