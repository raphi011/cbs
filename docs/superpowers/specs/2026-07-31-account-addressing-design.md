# Account Addressing — Design

A prerequisite, discovered rather than planned. The role-scoped web UI
(`2026-07-31-role-scoped-web-ui-design.md`) gives a customer a "send money"
screen, and a customer sends money to an **IBAN** — not to a UUID. The system
cannot do that today:

> **No IBAN or BIC validation.** Routing is by explicit `ParticipantID`; IBANs
> are free-form labels. (`README.md:1002`, `payment/doc.go:64`)

`PartyRef.IBAN` is a string carried on a payment for display
(`payment/types.go:166`); `deposit.Account` has no IBAN at all
(`deposit/types.go:71-76`). There is nothing to look an IBAN *up* in.

This design gives a deposit account a set of **external identifiers** and gives
the network a way to resolve one to an account.

## Goal

Make "who is this payment addressed to?" a question the system can answer from
stored data, and make the answer scheme-shaped rather than SEPA-shaped.

The naive fix — an `iban` column on `deposit.Account` — is rejected below. It
would put a euro-area retail payment standard inside the CASA layer, where
nothing else knows what SEPA is, and it is not how account addressing works:

- An account has an **internal number** (this repo's `deposit.AccountID`), which
  is the bank's own key and is never quoted to a counterparty.
- Addressing is a separate, **plural** set of external identifiers pointing at
  it: an IBAN in SEPA, a sort code plus account number in the UK, a routing
  number plus account number in US ACH, a proxy alias (phone, email, Pix key,
  UPI VPA) in the newer instant schemes — and a **card PAN**, which is precisely
  an alias for a funding account.
- The **scheme** decides which of them addresses it. ISO 20022 encodes exactly
  this: `CashAccountIdentification` is a *choice* between `IBAN` and `Othr`, a
  generic (identification, scheme name, issuer) triple.

The last point is what makes this a small design rather than a speculative one.
`payment.Scheme` already declares its `Asset()` (`payment/scheme.go:21-27`) and
initiation already refuses a payment whose legs disagree with it
(`payment/system.go:993`). Addressing gets the identical treatment, in the
identical place, for the identical reason.

### Out of scope, deliberately

- **Format validation, including the IBAN check digit.** No ISO 7064 mod-97.
  Enforcing it would make the seed's readable `SE89-AURORA-1001` illegal and
  replace it with opaque digits in every screenshot, worked example and quiz
  answer in the repository. That costs more teaching than the check buys. Text
  validation stays what it is for every other string: `ledger.ValidateText`.
- **A proxy-alias registry.** Aliases that are *not* bank-issued — a phone
  number, an email address — genuinely cannot live with the account, because no
  single bank can guarantee they are unique across the network. SEPA's Proxy
  Lookup Service and UPI are separate central services for exactly this reason.
  Modelling one is a second store with no v1 consumer. The *reason* it is
  deferred is recorded in the uniqueness decision below rather than omitted.
- **A second identifier scheme.** `IdentifierIBAN` is the only member shipped.
  A card PAN arrives with the card scheme it addresses, not before it.
- **BIC / bank-level addressing.** Participants keep being addressed by
  `ParticipantID`. Resolving an IBAN yields the participant, which is the only
  thing the routing needs.
- **Lending facilities.** A facility is not addressed by a counterparty; nothing
  pays a loan account directly. `lending` is untouched.

## Decisions

### Identifiers are a set on the account, not a field

`deposit.Account` gains `Identifiers []Identifier`, not `IBAN string`.

A field cannot express the thing that makes this design worth doing: a customer
keeps their IBAN *and* gains a card PAN. It also cannot express an account with
no external address at all, which every internal plumbing account is — clearing
suspense is a GL account, but the modelling instinct that produced a non-null
column would have produced a lie somewhere.

Identifiers are supplied at `OpenAccount` (which already takes five domain
arguments — `deposit/register.go:152`) and are mutable afterwards through
`AddIdentifier` / `RemoveIdentifier`. Mutability is the point: reissuing a card
is removing one identifier and adding another, against an account whose balance
and history do not move.

### The vocabulary lives in `deposit`

`deposit.IdentifierScheme` (a defined string type, with `IdentifierIBAN` its one
member) and `deposit.Identifier{Scheme, Value}`.

`payment.Scheme.AddressedBy()` therefore returns a `deposit`-layer type. That
looks upside-down for about one second, and then stops: `Scheme.Asset()` returns
a `ledger`-layer type for the same reason, and `payment` already imports
`deposit` (`payment/types.go:6`, `payment/system.go:10`). A new `addressing`
package would exist to hold two type declarations and would earn its place only
if format validation lived in it — and format validation is explicitly not
shipped.

### Uniqueness stops at the bank, is a domain rule, and gets no constraint

`Register` refuses a duplicate `(scheme, value)` within its own bank with
`ErrIdentifierTaken`. It does not — cannot — check the network: a register spans
one participant's book and has nothing to compare against.

This is not a shortcut being tolerated. It is the right line, and the line
explains the deferred alias registry:

- A **bank-issued** identifier is globally unique *by construction*. An IBAN
  carries its own bank code; a PAN carries its BIN. Two banks cannot collide
  without one of them issuing identifiers it was not allocated, which is a fact
  about the issuing authority, not about this system's storage.
- A **proxy** alias carries no issuer. `alice@example.com` at Aurora and
  `alice@example.com` at Banca Verde is a collision no register can see, which
  is why a central registry is the only place that kind of alias can live.

So the enforcement boundary and the scope boundary are the same boundary, which
is the sign it is in the right place.

**And the rule gets no `UNIQUE` constraint in `store/pg`.** The schema states
the doctrine at `store/pg/schema/0001_init.sql:83-105` and applies it to
`UNIQUE (book_id, name)` at `:38-61`: a constraint Postgres holds and a Go map
does not makes `store/pg` reject writes `store/mem` accepts, and that divergence
is the single thing the store layer must never introduce. The exemption granted
there — "the race is already closed, one layer up" — does **not** apply here.
`AddIdentifier` reads before it writes, the way `CreateSubledgerTx` reads its
ledger, and nothing serializes two concurrent adds the way `NextID`'s row lock
serializes `AddParticipantTx`. Under READ COMMITTED both would pass the read, so
a constraint would fire in Postgres and not in memory.

The residual race is closed at the *read* instead, and closed by a rule the
design already has: if a register somehow holds two accounts under one
`(scheme, value)`, `ResolveIdentifier` returns `ErrIdentifierAmbiguous` — the
same answer it gives a cross-bank collision. Ambiguity is an error wherever it
comes from, and an address that resolves to two accounts is not an address.

### Resolution is a network concern, and ambiguity is an error

`payment.Network.ResolveIdentifier(ctx, scheme, value) (PartyRef, error)` sweeps
member registers. A miss is `deposit.ErrIdentifierNotFound`, returned unwrapped
so the API's error mapping has one code to match.

**Two hits are `ErrIdentifierAmbiguous`, not the first hit.** Per-bank
uniqueness makes a cross-bank collision possible in principle, and the system
already has a rule for this situation: settlement resolves a participant's
accounts from the cycle's asset and *fails the whole batch* rather than falling
back to euro, because "defaulting would settle a dollar cycle in the wrong
money, and doing so quietly, in the one place in the system where money becomes
final" (`README.md`, *The Multi-Bank Model*). Silently picking a bank for a
payment is the same mistake one layer earlier. There is no fallback.

### `PartyRef.IBAN` becomes `PartyRef.Identifier`

`PartyRef` (`payment/types.go:163-167`) currently carries `IBAN string //
free-form label; no validation in this model`. It becomes
`Identifier deposit.Identifier`.

A payment keeps storing the address that was actually used rather than
re-deriving it, because identifiers are mutable: an account that has since had
its IBAN removed must not retroactively change what a settled payment says it
was sent to. What changes is that the stored value is now *checked* at
initiation instead of being accepted as a label.

`validateParty` (`payment/system.go:1363-1375`) validates
`field + ".identifier.value"` in place of `field + ".iban"`, for the same reason
it validates the rest: a control character reaching `store/pg` raises a SQLSTATE
rather than answering "no such row".

### The scheme declares what addresses it, and initiation enforces it

`Scheme.AddressedBy() deposit.IdentifierScheme`. SEPA CT and SEPA DD both answer
`IdentifierIBAN`.

`InitiatePaymentTx` (`payment/system.go:973`) refuses a payment whose debtor or
creditor account carries no identifier in the scheme's scheme, with
`ErrUnaddressableAccount`, and refuses one whose `PartyRef.Identifier` is not
among the named account's identifiers, with `ErrIdentifierMismatch`.

Both checks go where the cross-asset check went (`:993`) and the roadmap records
why that location is right: it is "the one moment both ends are in view and
neither is written", and putting it in `InitiatePaymentTx` rather than in a
scheme's `Validate` runs it for every scheme rather than only those that check
funds (`docs/expansion-roadmap.md`, sub-project 1 log entry).

Without this check `AddressedBy()` would be decorative — a declaration nothing
reads. With it, "the IBAN is bound to the scheme, not to the account" is a
property the tests can hold.

## Data model

```go
// deposit
type IdentifierScheme string

const IdentifierIBAN IdentifierScheme = "IBAN"

// Identifier is one external address for an account: the thing a counterparty
// quotes. The account's own AccountID is never one of these.
type Identifier struct {
    Scheme IdentifierScheme
    Value  string
}

type Account struct {
    // … existing fields …
    Identifiers []Identifier
}
```

```go
// payment
type PartyRef struct {
    Participant ParticipantID
    Account     deposit.AccountID
    Identifier  deposit.Identifier // the address quoted; checked against the account
}
```

New errors: `deposit.ErrIdentifierTaken`, `deposit.ErrIdentifierNotFound`,
`deposit.ErrIdentifierAmbiguous` (raised by both the register's resolve and the
network's, which is why it lives with the type rather than in `payment`),
`payment.ErrUnaddressableAccount`, `payment.ErrIdentifierMismatch`.

## Register interface

```go
func (r *Register) AddIdentifier(ctx, id AccountID, ident Identifier) error
func (r *Register) AddIdentifierTx(ctx, tx Tx, id AccountID, ident Identifier) error
func (r *Register) RemoveIdentifier(ctx, id AccountID, ident Identifier) error
func (r *Register) RemoveIdentifierTx(ctx, tx Tx, id AccountID, ident Identifier) error
func (r *Register) ResolveIdentifier(ctx, ident Identifier) (Account, error)
```

`OpenAccount` / `OpenAccountTx` take a trailing `identifiers []Identifier`.

Every mutation appends to the deposit audit trail (`appendAuditTx`,
`deposit/register.go:102`) with event types `identifier.added` /
`identifier.removed` — an address change is exactly the kind of event a
back-office user needs to see who made and when, and the audit table is already
the answer to that question.

## Store interface

Identifiers are part of the account aggregate, not a sibling entity. The
register mutates `Account.Identifiers` and calls the existing
`PutDepositAccount`; the store writes both sides itself. There is no
`AddDepositAccountIdentifier` — one new method, a read:

```go
ListDepositAccountsByIdentifier(ctx, book, Identifier) ([]Account, error)
```

It returns **every** match rather than one account and a not-found sentinel,
because "an address resolves to exactly one account" is a domain rule and the
store is a key/value layer that holds no domain rules — the same division that
keeps parent-existence out of the schema. Zero matches, one, and more than one
become `ErrIdentifierNotFound`, the account, and `ErrIdentifierAmbiguous` in
`Register.ResolveIdentifier`, which is also where the network's sweep applies
the identical rule across banks.

`GetDepositAccount` and `ListDepositAccounts` return accounts with
`Identifiers` populated. The list path must not become N+1 — the same finding
the multi-asset rework produced in two listing endpoints
(`docs/expansion-roadmap.md`, 2026-07-27 log) — so `store/pg` fetches
identifiers for the whole page in one query and joins them in memory.

### Schema

```sql
CREATE TABLE deposit_account_identifiers (
    participant_id     TEXT NOT NULL,
    deposit_account_id TEXT NOT NULL,
    scheme             TEXT NOT NULL,
    value              TEXT NOT NULL,
    PRIMARY KEY (participant_id, deposit_account_id, scheme, value),
    FOREIGN KEY (participant_id, deposit_account_id)
        REFERENCES deposit_accounts (participant_id, id) ON DELETE CASCADE
);

CREATE INDEX ON deposit_account_identifiers (participant_id, scheme, value);
```

Two things this schema deliberately does **not** say, both recorded in the file
with `COMMENT ON TABLE`, because an absent constraint is invisible in a schema
dump:

- **There is no `UNIQUE (participant_id, scheme, value)`,** for the reason
  argued above. The primary key is deliberately widened with
  `deposit_account_id` so that it is a row identity rather than the domain rule
  in disguise; the domain rule lives in `Register`, and the index that makes
  resolution fast is a plain index, not a unique one.
- **The parent reference is a real `FOREIGN KEY`,** which looks like it
  contradicts `:83-105` until the exemption stated there is read: the
  child-table FKs that stay (`entries → transactions`,
  `cycle_payments → cycles`, `settlement_positions → settlements`) are the ones
  where "the store writes both sides itself, within one statement sequence, so
  there is no caller who could ever produce an orphan". Identifiers are written
  by `PutDepositAccount` alongside the account, which is exactly that case, and
  it is why they are modelled as part of the aggregate rather than as a
  separately-writable entity.

Folded into `store/pg/schema/0001_init.sql`, per the standing rule that there is
one migration because no database is deployed.

## Consumers

- **`api`** — `partyRefDTO` (`api/dto_payment.go:79-91`) replaces `iban` with
  `identifier: {scheme, value}`. New:
  `POST/GET /participants/{pid}/deposit-accounts/{did}/identifiers`,
  `DELETE …/identifiers/{scheme}/{value}`, and
  `GET /directory?scheme=IBAN&value=…` → `{participant, account, name, asset}`.
  `ErrIdentifierNotFound` → 404, `ErrIdentifierTaken` → 409,
  `ErrIdentifierAmbiguous` → 409, `ErrUnaddressableAccount` and
  `ErrIdentifierMismatch` → 422.
- **`seed`** — the builder's `ibans` map (`seed/seed.go:292-293`) stops being a
  side table and becomes identifiers on the accounts it opens. The values are
  unchanged, so every existing worked example, screenshot and doc reference
  still reads the same.
- **`web`** — five `iban` references move to the new DTO shape. The customer
  send form in the UI sub-project is the first real consumer of `/directory`.

## Documentation, across every layer

- **`README.md`** — the *Deliberate Simplifications* bullet at `:1002` is
  replaced: routing is by identifier lookup; identifier *format* is unvalidated
  per scheme, and why. A short **Addressing** subsection under *The Multi-Bank
  Model* explains internal number vs external identifier, the ISO 20022 choice,
  why the scheme declares the identifier, and why uniqueness stops at the bank.
- **`payment/doc.go:64`** — the same claim, same correction.
- **`web/src/components/hint-content.ts`** — a new `account-addressing` key
  carrying the short version, linked from the payment and deposit-account hints.
- **`store/pg/schema/0001_init.sql`** — table and column comments as above.
- **Quiz** — no change required. Nothing an existing chapter asserts becomes
  false; the IBAN mentions at `README.md:132` and `:337` are about assets and
  stay true. A chapter-9 question on addressing is optional and constrained by
  `diversity.test.ts` (18–22 questions, ≥8 concept tags, no tag more than 3×).

## Testing

- `deposit` — open with identifiers; add and remove; duplicate within a bank
  refused; the same `(scheme, value)` in two different banks accepted; resolve
  hits and misses; audit rows appended.
- `payment` — `ResolveIdentifier` across members; not-found; **ambiguous
  refused rather than resolved to the first hit** (this is the claim most likely
  to rot into a fallback, so it is pinned by a test, not by a comment — the
  lesson the roadmap records twice from the multi-asset and lending branches);
  an SCT to an account with no IBAN refused; a `PartyRef` quoting an identifier
  the account does not hold refused.
- `store/storetest` — `IdentifiersSurviveAccountRead`,
  `IdentifiersAreScopedToParticipant`, `FindByIdentifierIsExact`, and
  `IdentifierUniquenessIsNotEnforced` — the last named after, and doing the same
  job as, `ParentReferencesAreNotEnforced`: it writes a duplicate through the
  *store* and asserts both implementations accept it, which is what stops a
  well-meaning `UNIQUE` from being added to Postgres later. Both stores, per the
  standing conformance rule.
- `api` — the directory endpoint's status codes; the DTO shape change.
- `go test ./...` with no `DATABASE_URL` and `TEST_DATABASE_URL=… go test ./...`
  must both stay green.

## Failure modes

- **An account with no identifier becomes unpayable.** By design, and the error
  says so (`ErrUnaddressableAccount`). The seed gives every customer account an
  IBAN, so no seeded scenario changes.
- **A removed identifier orphans historical payments.** It does not: the payment
  stores the address it used. This is why `PartyRef.Identifier` is stored rather
  than derived.
- **A reissued identifier kills the mandates on the account.** *Added after the
  whole-branch review; this mode was missed, and the code shipped with it.*
  `PartyRef` is stored on **mandates** as well as payments, and `SDD.Validate`
  matched a payment to its mandate with whole-struct equality. Once the ref
  carried an address, a remove-then-add reissue — the operation the design
  advertises as harmless, "an account whose balance and history do not move" —
  left no quoting that worked: the new address failed the mandate comparison,
  the old one was refused because the account no longer held it, and quoting
  nothing failed the comparison again. There is no `UpdateMandate`, so the
  mandate was dead permanently, and it was reachable over the shipped API.

  **Resolution:** identity and address are separated. `PartyRef.SameParty`
  compares `(participant, account)` and nothing else, and the mandate check uses
  it. A mandate authorises debits from an *account*; the quoted address is a
  record of how one payment reached it, which is precisely what "stored rather
  than derived" already implied and what whole-struct equality contradicted.
  Pinned by `TestMandateSurvivesAReissuedDebtorIdentifier`.

  The general lesson, recorded in `docs/expansion-roadmap.md`: the compiler
  found the unanticipated consumer (`PartyRef` on mandates), and only its
  *schema* consequence was chased. A type that gains a field needs the design's
  failure analysis re-run against every consumer the compiler turns up, not just
  a column added for it.
- **A payment records no address at all.** The identifier is optional in the
  request DTO, so before back-filling this was the *default*: every
  payment-creating test in `api` omitted it and settled with both legs'
  addresses empty, and only `seed` populated them. "A payment records the address
  it was sent to" was therefore a property of well-behaved callers rather than
  of the system. `InitiatePaymentTx` now writes the account's single identifier
  in the scheme's scheme onto the stored ref when the caller quoted none, and
  refuses with `ErrAmbiguousAddress` when there is more than one candidate —
  the same refusal-rather-than-guess rule as the ambiguous resolution.
- **Cross-bank collision.** Refused at resolution with
  `ErrIdentifierAmbiguous`. Reachable only by deliberately issuing the same
  value at two banks, which the tests do on purpose.
- **Within-bank collision under a concurrent add.** Possible, because the
  uniqueness check is a domain read-then-write with no constraint behind it and
  no lock above it. It surfaces as `ErrIdentifierAmbiguous` at resolution rather
  than as a wrong answer, which is the acceptable failure; the schema comment
  says so, so that the next reader who reaches for a `UNIQUE` finds the argument
  instead of the gap. **At resolution and only there** — `InitiatePaymentTx`
  takes an explicit account id and never resolves, so both colliding accounts
  stay payable by id. That is the right answer, not a second gap: the accounts
  are distinct and real, and what is ambiguous is the address.
- **The directory sweep is O(banks).** With four members this is not worth an
  index-per-network. It is worth a comment saying that a real network's
  directory is a service, not a sweep, and that this is the boundary at which
  the deferred alias registry would arrive.
