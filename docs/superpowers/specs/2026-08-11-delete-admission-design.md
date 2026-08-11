# Design — admission stops being a conversation, and a bank arrives provisioned

On `main`, based on `7fb11b6`.

A bank joins this system through four units of work at three institutions,
carried by an `acmt.007`, an `acmt.010` and an `acmt.011` over the mesh. This
deletes the conversation and the state it produces. What writes the three rows
instead is a **provisioner**: one component, outside the domain, that calls the
four acts directly.

The N+2 database split survives, and so does every argument that rests on it —
who may open which account in whose book, why the three rows have three owners,
why a routing directory is a copy that can be behind. What goes is the message
layer, the `Founded` state, and the teaching built on the difference between
founding and joining.

Like the subsidiary-ledger task, this overturns an argument the repository has
written down and won. §*The claim being overturned* is the part to review
hardest, and §*What becomes false* is the part that will be missed.

## The claim being overturned, and the one that survives

`README.md:1034` says:

> **This reverses a ruling this system used to make, and the reversal is the
> domain content.** Admission was once `AddParticipantTx`: one unit of work that
> wrote the bank's chart of accounts, its settlement accounts in the *central
> bank's* book and its row in the *clearing house's* roster, all together […]
> The atomic write was buying a consistency the domain does not have, and it was
> buying it by letting one institution write in two others' books. It is deleted.

That paragraph makes two claims, and they come apart.

**One institution must not write in two others' books — survives, untouched.**
The provisioner is not an institution. It has the same standing as
`payment/recon`, which already opens all N+2 databases precisely because no actor
in the system may. It calls each institution's own act against that
institution's own network, one unit of work each, and no statement spans two
databases. This is not a return to `AddParticipantTx`.

**Founding and joining are separately observable, and joining can be refused —
overturned.** Not because it is untrue of the world; it is true. Because it is
not what this repository is for, and the version built to teach it does not
work:

- `README.md:1073`, §*What Admission Is Not*, already concedes both halves are
  fictional. Scheme membership is a signed adherence agreement with no ISO
  message. A bank's account at its central bank is reference data — CRDM static
  data and `reda` in TARGET — set up by the central bank's own operations, not
  requested over a payment network. So `iso20022/acmt.go` and its test, 982
  lines, model a message that does not exist.
- The runtime path does not produce a usable bank. `cmd/server/listeners.go`'s
  `plan()` derives one port per bank from `stores.Banks()` at startup, so a bank
  admitted afterwards has a book, a reserve account and a roster entry, and no
  way to be reached over HTTP. `web/CLAUDE.md` records this in as many words:
  "A participant admitted at runtime has no listener until the server restarts —
  admission is not provisioning." `app/api/operators/route.ts` exists to probe
  operators so the lobby can tell an un-provisioned bank from a running one.
- Nothing downstream exercises a refused application. `acmt.011` is generated,
  relayed and asserted on, and no part of the system does anything with a bank
  that was turned down.

| | before | after |
|---|---|---|
| three rows, three owners, three databases | yes | **yes** |
| one institution writing in another's book | no | **no** |
| carried by ISO 20022 messages | yes | no |
| `Founded` observable as a domain state | yes | no |
| a bank creatable at runtime | nominally | no |
| the intermediate state exists at all | yes | **yes — unavoidable** |

The last row is the one to be honest about. `RecordMembership` needs the account
numbers the central bank allocated, so it cannot commit until the central bank
has. Two commits in the bank's own book with another institution's commit
between them. **The gap does not go away; nothing names it, exposes it, or
teaches it.** A crash between the two leaves a bank with rows in one book and not
the others, which is a provisioning failure to retry and a `payment/recon`
finding — not a domain state with a name.

## The replacement already exists

`store/storetest/admit.go` (176 lines) is the provisioner, written as a test
helper. It calls `FoundBank` → `OpenSettlementAccount` (once per asset, at the
central bank) → `AdmitMember` (clearing house) → `RecordMembership` (the bank) →
`Subscribe`, one unit of work each, on one goroutine, with no mesh at all. Its
doc has a section headed *What it is NOT*: "It is not what the system does."

This change makes it exactly what the system does. It moves out of `storetest`
into a package of its own, and the apology becomes the design note.

The four `payment` transactions it calls do not change:

| | |
|---|---|
| `FoundBankTx` | `payment/system.go:845` |
| `OpenSettlementAccountTx` | `payment/system.go:1077` |
| `AdmitMemberTx` | `payment/system.go:1371` |
| `RecordMembershipTx` | `payment/system.go:1495` |

They **are** the N+2 split — one institution's write each. Only what carries
between them is deleted.

## Deletions

| Path | Lines | Note |
|---|---|---|
| `iso20022/acmt.go` | 533 | |
| `iso20022/acmt_test.go` | 449 | plus the acmt goldens in `iso20022/testdata` |
| `mesh/admission_test.go` | 1166 | |
| `mesh/doors.go` `Mesh.Admit` | ~60 | at :309 |
| `mesh/bank.go` | ~150 | `receiveAdmission`, `receiveAdmissionRejection`, the Acmt cases at :156 |
| `mesh/csm.go` | ~200 | `relayAdmission`, `receiveAdmissionStatus`, `relayAdmissionRejection`, `refuseAdmission` |
| `mesh/centralbank.go` | ~220 | `receiveAdmission` :443, `refuseAdmission` :641 |
| `mesh/ops.go` | ~40 | the admission arm of the three narrowed interfaces |
| `api/handlers_participant.go` | part of 373 | `POST /members` |
| `web/src/components/create-participant-dialog.tsx` | all | |

≈2,800 lines.

## Rewrites, which are the real cost

- `mesh/harness_test.go` (1532 lines, 28 admission mentions). Every mesh suite
  gets its member banks by driving `Mesh.Admit` and draining the bus; all switch
  to the provisioner. Mechanical, but it touches every suite in the package.
- `store/storetest/races.go` (39 mentions) races the admission acts against each
  other. The acts survive, so most of it does; what changes is the setup.
- `seed/seed.go` (30 mentions) — `admit()` at :290 becomes a provisioner call,
  and its doc comment, which narrates the whole conversation, goes with it.
- `mesh/doc.go` (20), `mesh/books_test.go` (15), `mesh/roster_test.go` (15),
  `mesh/operator_test.go` (11), `mesh/directory_test.go` (10) — prose and
  fixtures.
- `payment/roster.go` (292). The rows survive. `AdmissionRequest` and
  `AdmissionAcknowledgement` stop being message payloads and become the
  provisioner's arguments.
- `payment/recon/recon.go` gains the half-provisioned-bank invariant.

## Schema

No migration. Every database this repository meets is ephemeral or a throwaway
file, one `0001` apiece, and both migrate from empty.

- `banks.status` — **drop**. `bank_assets.settlement` being empty already says
  the same thing (`payment/bank.go:112`), so the column is redundant once the
  state is not a domain concept.
- `banks.admission_ref` — **keep**. The clearing house compares it for
  `ErrBICAlreadyAdmitted`, which is still a real refusal.
- `bank_assets.settlement` — **keep**. A bank's note of the central bank's
  account number is the whole nostro/vostro point.
- `bank/0001_init.sql:1060-1085`, the `banks` comment, rewrites. Its headline
  claim — ADMISSION WRITES THREE ROWS, ONE PER INSTITUTION — stays **true**;
  only the mechanism changes. Separately, that comment currently says "the
  difference Task 18 makes to this paragraph", which `CLAUDE.md` forbids
  outright; fix it regardless of what happens here.
- Comments stay INSIDE the statement parentheses.
  `TestSchemaArgumentsReachSqliteMaster` enforces it, three cases, one per shape.

## The four refusals

| Refusal | Fate |
|---|---|
| `deposit.ErrNoIssuer` | survives — a bank still needs an allocated code to mint IBANs |
| `ErrSettlementMemberNotFound` | survives — now only reachable on a half-provisioned bank |
| `ErrBankCodeUnknown` | survives — it is about directory staleness, not admission |
| `ErrBankNotAdmitted` | **at risk, and keep it anyway** |

`README.md:1063` records what its absence cost: one non-member in a cycle took
the *whole settlement* down — every other member's payments stuck at `Cleared`,
payees unpaid, payers' money in suspense. Provisioning-only makes that
unreachable by construction, but only for as long as provisioning is genuinely
the only path. Keep the refusal, and add the recon check; "unreachable by
construction" is a claim about today's callers, not an invariant.

## Learner-facing layers

`CLAUDE.md` requires these to move with the code. They carry **no repo symbols** —
no Go names, no routes, no file names — but they do carry schema table and
column names.

- `README.md:1032-1080`, §*Admission: A Bank Exists Before It Joins a Scheme* —
  fourteen dense paragraphs, most of which delete. Three arguments are worth
  relocating rather than losing: who may open which account in whose book; the
  order (settlement account before routing entry, because a scheme will not route
  to a member that cannot settle); and admission-fills-nobody's-directory.
- `web/src/components/hint-content.ts`: `bank-founding` (:1359) and
  `bank-admission` (:1429). **Trap:** a `[[wiki-link]]` to a deleted key throws
  under `RootLayout` and takes every dev route down, while `next build` stays
  green. `npm run test` catches it in hint bodies *and* quiz explanations
  (`concept-links.test.ts`). Grep for both keys before deleting either.
- Quiz chapters 09, 11, 12, 15 carry admission questions.
  `web/src/lib/quiz/diversity.test.ts` holds every chapter to 18–22 questions, ≥8
  distinct `concept` tags, no tag more than 3×, and all three difficulty tiers —
  so questions must be **replaced**, not removed. This is the slowest part of the
  change and the easiest to underestimate.
- `web/src/app/clearing-house/page.tsx:38-147` — the member/founded stat, the
  status column, and the copy explaining what a founded bank cannot do.
- `web/CLAUDE.md` — the admission-is-not-provisioning paragraph, and the intro
  loop ("create participant → open deposit account → fund → lodge"), which loses
  its first step.
- `CONTEXT.md` — `member` retires with the status, leaving `participant` alone.
  Delete the `member / participant` line from §*Unresolved*.

## What becomes false

Written claims that stop being true, beyond the prose being deleted:

- `payment/bank.go:29-65` — the whole `BankStatus` doc, including "a bank exists
  before it joins a scheme" and "what an interrupted admission leaves behind".
- `store/storetest/admit.go`'s §*What it is NOT*, which inverts.
- `mesh/ops.go:38-46` — "Admission's four acts land three-and-one, and the one
  that is missing says something about the flow."
- `web/CLAUDE.md`'s "admission is not provisioning" — moot once they are the
  same act.
- `README.md:1063`'s account of what the missing refusal cost stays true as
  history, but the refusal it justifies is no longer reachable from a submission.

## Decided before task 1

1. **The web UI keeps no way to add a bank.** The dialog and `POST /members` go.
   A bank added at runtime never had a listener, so the affordance was a lie
   whichever way it was plumbed.
2. **`Subscribe` is a separate act and the provisioner does not call it.**
   Provisioning writes three rows and stops; a caller that wants members able to
   address each other asks for the refresh itself. This is what keeps
   `ErrBankCodeUnknown` reachable and
   `TestABankAdmittedAfterTheLastRefreshCannotBePaidUntilTheNextOne` writable —
   a fixed set of banks provisioned at boot is never stale at runtime, but the
   window between the roster and a member's copy is still real and still the
   thing a routing directory is for.
3. **`banks.status` is dropped**, and `BankStatus` with it.
   `bank_assets.settlement` being empty already says the same thing, and
   `payment/recon` gains the half-provisioned check.

## Deferred, on purpose

A **YAML deployment config** — banks, BICs, countries, assets, ports — was
considered and is not part of this. Today there is no config at all: the shape is
hardcoded Go in `seed/seed.go`, with ports derived from `-base-port`. The
provisioner this change leaves behind takes a list of bank specs, and a YAML
decoder later reads into that same list. Same seam, no rework. Doing both at once
means debugging a config format while also judging whether the domain still
teaches what it should.

## Task order

Green at every step.

1. Provisioner out of `storetest` into its own package; every suite and `seed`
   onto it. Nothing deleted yet.

   One obstacle this surfaces, and it lands on task 2. `mesh/books_test.go`'s
   `TestWhichBooksAdmissionReaches` measures the invariant that SURVIVES all of
   this — no institution reaches another's book — but it attributes each access
   to an actor read off the context (`wire.ActorOf`), and a provisioner-driven
   act has no actor, so every set comes back empty. Either the test drives the
   four acts itself under explicit actor contexts, or the recorder tracks books
   per `Update` and the test asserts the sharper claim: no single unit of work
   touched two books. `mesh/roster_test.go`'s `TestStartGivesAFoundedBankNoActor`
   is the other straggler and belongs with task 3, being about `Founded`.
2. Delete the `acmt` layer and the mesh handlers that carry it.
3. Drop `BankStatus`, `banks.status`, `POST /members`, the dialog.
4. README, hints, quiz, schema comments, `CONTEXT.md`, `web/CLAUDE.md` — the
   layers `CLAUDE.md` requires to move together.
5. The `payment/recon` invariant for a half-provisioned bank.

## Green means

```
go build ./... && gofmt -l . && go test ./...        # 14 packages
cd web && npx tsc --noEmit && npm run lint && npm run test && npm run build
```

`npm run test` is 188 tests today and is **not** optional — it is what catches a
dangling `[[wiki-link]]` and a quiz chapter that has fallen out of the diversity
bounds. `TestSchemaArgumentsReachSqliteMaster` must pass for all three schemas.
No database setup is needed for any of it.

## Repo state at the time of writing

`main`, four commits ahead of `origin/main`, nothing pushed. The three most
recent are the subsidiary-ledger vocabulary work, `CONTEXT.md`, and
`docs/agents/`. `docs/sepa-real-world.md` is untracked and unrelated to any of
this.
