# Handoff: sub-project 7b, the mesh and the actors

Paste the block below into a fresh session in `~/Git/cbs-product-catalogue` —
the worktree that already has `spec/iso20022-messages` checked out. Not
`~/Git/cbs`: git refuses to check the same branch out in two worktrees, so
starting there means the first thing you do fails. If you want your own
isolated tree, create a new worktree from that branch rather than reusing the
main repo.

---

I want to build sub-project **7b** from `docs/expansion-roadmap.md`: **the mesh
and the actors**. Read the sub-project 7 section of the roadmap first — it is
the only requirements document that exists for 7b.

**7b has no spec and no plan.** Only 7a was specified. So start by writing the
**spec** with the `superpowers:brainstorming` skill, and do not write an
implementation plan until I have agreed the spec. There is no older 7b plan to
salvage; do not go looking for one.

## Settle this before anything else

The roadmap describes 7b as **one goroutine per bank exchanging marshalled
messages over channels**. Its own changelog then flags that description as
possibly stale:

> 7b already specifies one goroutine per entity over channels, which after 6a
> wants to be an HTTP POST to a counterparty's address — a better 7b, and a real
> revision to a spec on another branch.

The tension is real and it is structural, not cosmetic:

- **Go channels do not cross process boundaries.** The channel design requires
  the mesh to live inside one binary running every listener — 6a's default mode.
- **6a's `-entity` mode runs one process per entity**, and the channel mesh
  cannot span it. The roadmap states plainly that 7b must either refuse
  `-entity` or carry a second transport, and that this is 7b's decision.
- A real network's banks *are* separate processes, and the honest form of that
  is a socket. 6a already gave every entity an HTTP listener and an address.

So the first question the spec must answer is **channels, HTTP, or a transport
interface with two implementors** — and the roadmap's own argument against the
interface ("an abstraction with one implementor, kept open for a topology that
is not the default, would not earn its place") stops applying the moment there
are two. Bring me a recommendation with the reasoning; do not just adopt the
roadmap's original wording because it is written down.

## What just landed that you build on

**Sub-project 7a, the `iso20022` package, is done**: the whole of
`spec/iso20022-messages` (based on `spec/operator-split-api`), **unmerged and
unpushed**. The commit count moves every time a review round lands — it was
written here as "31" and was already 32 by the time the branch was reviewed —
so count it rather than trusting a number in a document:

    git rev-list --count $(git merge-base spec/operator-split-api HEAD)..HEAD

Read
`docs/superpowers/specs/2026-07-31-iso20022-messages-design.md` for the
reasoning. The facts you need:

- **Four messages, all EPC-conformant and schema-validated**: `pacs.008.001.08`
  (credit transfer), `pacs.003.001.08` (direct debit), `pacs.002.001.10` (status
  report), `pacs.004.001.09` (return). Plus the `head.001.001.02` business
  application header.
- **The codec is `Marshal(Envelope) ([]byte, error)` and
  `Unmarshal([]byte) (Envelope, error)`.** `Envelope` is `{AppHdr, Document}`.
  `Document` is an interface with an unexported `validate()`, so message types
  can only be added inside the package. Unmarshal dispatches on the header's
  `MsgDefIdr` via a registry each message file populates in `init()`.
- **The `<Envelope>` wrapper is this repository's own invention**, standing in
  for a clearing-house-specific framing. The two elements inside it are
  standard. If 7b sends over HTTP, the wrapper is the natural request body; if
  over channels, it is the natural channel element.
- **`Marshal` validates both halves and `Unmarshal` now does too.** A message
  that round-trips is EPC-valid, so 7b does not need its own validation layer —
  but note that means `Unmarshal` *rejects* a structurally-fine message with,
  say, a malformed BIC. Decide deliberately whether the mesh wants that at the
  receive boundary or wants to answer it with a `pacs.002` rejection.
- **`BIC` and `IBAN` types exist, with `IBAN.Compact()`.** Neither verifies a
  check digit, on purpose — see the `IBAN` doc comment.
- **The package imports nothing from this repository**, and a test would fail if
  that changed. **The translator does not exist yet — building it is 7b's job**,
  and it belongs on the `payment` side, not inside `iso20022`.

### What 7b has to add, from the roadmap

- **`Participant.BIC`** — `payment/participant.go:59` has no BIC field today.
  Adding it means columns in **both** stores and a `store/storetest` case, per
  `CLAUDE.md`'s conformance rule.
- **Routing by `BICFI`.**
- **The creditor-account check moves out of `InitiatePaymentTx`**
  (`payment/system.go:973`) and comes back as a `pacs.002` rejection. This is
  the change that makes clearing genuinely asynchronous.
- **`api/` moves to `202 Accepted` + a status query.** 6a already shaped the
  customer send form around that response, so the web layer is waiting for it.
- **Settlement stays one atomic `Store.Update` at the central bank.** That is
  what a settlement agent is; do not distribute it.

`payment.Network.ReturnPayment` (`payment/system.go:1197`) already implements
the return operation — 7b wires `pacs.004` to it rather than writing it.

## Decisions 7a left open for you

Four, none of them blocking, all recorded in
`.superpowers/sdd/2026-07-31-iso20022-messages/progress.md`:

1. **`OrgnlTxRef` is deliberately not modelled**, on either `pacs.002` or
   `pacs.004`, though the EPC guidelines make it mandatory on both. It is a
   ~20-field echo of the original instruction. This is the one identified
   respect in which the samples are an EPC subset, and it is disclosed in
   `envelope.go` and `testdata/README.md`. **If 7b needs a receiving bank to
   reconstruct a rejected payment without having kept state, this is the field
   that would do it** — revisit the decision then, with a real consumer to
   justify the size.
2. **`StsRsnInf/Orgtr` given as `PrvtId` is representable and not refused.** ISO
   allows it, EPC does not — `PrvtId` appears nowhere under `Orgtr` in either
   IG — and `xmllint` accepts it. Do **not** generalise from "a PSP is not a
   natural person": that was this document's original reason and it is wrong.
   The SCT Inter-PSP IG idx 3.9 limits `Orgtr` to "'AnyBIC' to identify the PSP
   or CSM originating the status or 'Name' to indicate the CSM when it has no
   BIC", and the SDD Core IG idx 3.9 is broader still — "or 'Name' to indicate
   the **Debtor** or CSM when it has no BIC". So `Orgtr` is not restricted to
   financial institutions, and under SDD it may name a natural person; what it
   may never do is identify one via `PrvtId`. Enforcing that needs either a
   widened `ErrElementNotAllowed` or a new sentinel. Unresolved.
3. **`OrgnlEndToEndId` and `OrgnlTxId` are each EPC-mandatory (`1..1`)** on both
   `pacs.002` and `pacs.004`, but both messages model them as a one-of. Tighten
   together or not at all.
4. **Group-header `TtlRtrdIntrBkSttlmAmt` / `TtlIntrBkSttlmAmt` and
   `IntrBkSttlmDt` are EPC-mandatory** where ISO leaves them optional; their
   format is checked when present, their presence is not required. Same shape in
   all four messages.

## Things about this repo that will bite you

- **`go test ./...` must stay green with no `DATABASE_URL` and against
  Postgres.** Absolute rule (`CLAUDE.md`). `store/pg` must never accept or
  refuse a write differently from `store/mem`; `store/storetest` enforces it,
  and `Participant.BIC` will need a case there.
- **Prose that asserts what code does needs a test, not a careful reading.**
  This project has now hit that **five** times — the fifth was 7a's own package
  doc claiming `payment/translate.go` existed when it did not. If you write a
  doc comment describing behaviour, pin the behaviour.
- **The fuzz target earned its keep in under a minute.** `FuzzUnmarshal` found
  a real decode/encode asymmetry within ~24 seconds of first running. If 7b adds
  a parsing or transport boundary, fuzz it.
- **A skip is not a pass.** `TestGoldenFilesValidateAgainstTheSchema` skips
  unless `xmllint` and `testdata/xsd/*.xsd` are both present; the schemas are
  not committed (not this repo's to redistribute). `testdata/README.md` says
  where to get them. It validates both the `Document` and the `AppHdr`. Once
  you have the schemas, run `make test-schemas` — it sets
  `ISO20022_REQUIRE_SCHEMAS=1`, which turns every skip in that test into a
  failure. A reviewer has run it against the official XSDs: 8/8 pass.
- **Domain facts are duplicated across four layers on purpose** (`CLAUDE.md`):
  `README.md` is authoritative, then `web/src/components/hint-content.ts`, the
  quiz chapters, then `store/pg/schema/0001_init.sql`. 7b makes
  `README.md:1016` and `payment/doc.go:61` false — both currently say this repo
  has **no** ISO 20022 message parsing. Fixing them is 7b's, and 7c owns the
  README/hint/quiz teaching layer for the message log.
- **A `[[wiki-link]]` to a key not in `hint-content.ts` throws at runtime under
  `RootLayout` and takes every dev route down** while `next build` stays green.
  `cd web && npm run test` catches it.
- **A scripted edit that matches nothing must fail, not pass silently.** Assert
  on every scripted replacement — this has cost rework twice.

## Environment

- **Postgres is a local Homebrew `postgresql@18` service** (18.4), not the
  Docker container: `postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable`.
  The `cbs` role and database already exist. **Docker/colima is unavailable**,
  so `make test-pg` will not work — set `TEST_DATABASE_URL` directly:

      TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...

- The working tree is a git worktree at `~/Git/cbs-product-catalogue` on branch
  `spec/iso20022-messages`; the main repo is `~/Git/cbs`.

## The state 7a is actually in

Be clear-eyed about this before you build on it:

- **Thirty-odd commits, unmerged and unpushed** (count them with the command
  above). Every earlier task got at least one review round, and those rounds
  found real defects in Tasks 4, 7, 8, 9 and 10 — including one that only
  surfaced because a reviewer fetched the genuine EPC guidelines and read them.
  The last three commits were reviewed after this document was written; that
  round found two Critical defects on the receive path, both now fixed, and its
  report is
  `.superpowers/sdd/2026-08-01-iso20022-mesh/p1-review-report.md`.
- **The plan says to hold the merge until 7b's plan exists.** Nothing imports
  `iso20022`, which is the spec's stated main cost; 7b following immediately is
  the mitigation the whole arrangement depends on. That is why you are here.
- Verified green at `e94c9b0`: `gofmt`, `go vet ./...`, `go build ./...`,
  `go test ./...` on **both** stores, no repository imports, `go.mod`
  untouched, 3.3M fuzz executions clean.

Ask me before starting if anything in the roadmap reads as wrong rather than
merely unexpected — particularly the channels-versus-HTTP question, which I
already suspect the roadmap gets wrong.
