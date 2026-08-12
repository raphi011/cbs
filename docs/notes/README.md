# Notes

Evidence, kept out of the code.

A comment states the rule. An [ADR](../adr/README.md) states a decision and the
alternative it rejected. A [spec](../specs/) states a plan and its phasing. This
directory holds the fourth thing: the **measurement behind a claim** — the probe
output, the mutation result, the arithmetic, the state a guard was watched
producing when it was removed.

That material earns its place in the repository and does not earn twenty lines
above a function. `CLAUDE.md` gives a doc comment a budget of about ten lines, so
a comment names the rule and points here, and the evidence sits in one file per
area where a reader can find it and a reviewer can check it.

**What belongs here**

- A measured number and what was measured. Probe transcripts, before/after
  balances, timings, counts.
- What a guard was seen doing with the guard removed — the mutation result that
  makes a refusal falsifiable rather than asserted.
- A guarantee the system does NOT have, recorded so the absence is a decision
  rather than an oversight.
- A hazard that is currently unreachable, with what makes it unreachable, so the
  day that changes the note is already written.

**What does not**

- A rule. That is a comment.
- A ruling with an alternative it rejected. That is an ADR.
- A plan with phasing. That is a spec.
- A change log. That is `git log`.

**A note is written in the present tense and is not a history.** "The refusal is
reached this way, and with it removed the measurement is X" — not "we used to do
Y". If a note describes something that is no longer true, delete it; the
measurement it recorded was about a system that no longer exists.

| Area | File |
|---|---|
| `payment` | [payment.md](payment.md) |
| `cmd/server` | [cmd-server.md](cmd-server.md) |
