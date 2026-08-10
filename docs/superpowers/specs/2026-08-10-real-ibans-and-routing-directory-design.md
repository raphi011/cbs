# Design — Tasks 20 and 21: real IBANs, and the directory that routes them

Branch `spec/real-ibans`, based on `e3ae1cc`.

Two tasks, in order, and the split is the point: **Task 20 makes an address
real, Task 21 makes it routable.** Each overturns arguments this repository has
already written down and won, and they are separated so that each reversal is
reviewable on its own.

This supersedes parts of
[`2026-07-31-account-addressing-design.md`](2026-07-31-account-addressing-design.md)
— specifically its ruling that an identifier's format is never validated — and
finishes a sentence `2026-08-02-db-per-entity-design.md` left open when Task 18a
took the cross-bank sweep out and put nothing in its place.

## What is true today, and why it is not enough

`README.md:870` states the position exactly, and states it as a limitation:

> There is no second source: the roster is keyed by the BIC being derived and
> belongs to the clearing house rather than to a bank, and this network has no
> IBAN-to-BIC directory service — which is the thing a real originating bank
> actually derives from. SEPA is IBAN-only because every bank subscribes to such
> a table, not because routing is computable from an address.

Every clause of that is correct. The gap is that **the system has no such table
and therefore cannot be IBAN-only**, so a payer here supplies a BIC beside the
IBAN — which is SEPA before February 2016, and which `PartyDetails.Agent` records
as a deliberate and uncomfortable choice: the payer types the routing element,
and `mesh/books_test.go`'s `TestAWrongCounterpartyAgentDoesNotMisroute` measures
what that costs.

The table is the missing thing. Build it, and the assertion goes away.

## Decisions taken, and what they cost

1. **IBANs get real country codes, real lengths, and real BBAN structures**, one
   country per bank, matched to that bank's own BIC country: Aurora `DE`, Verde
   `IT`, Nordhaven `SE`, Soleil `FR`. Cost: the ~10 IBAN literals across
   `README.md`, `seed`, the web fixtures and the tests become opaque digits, and
   the repository loses the readability it deliberately bought at
   `deposit/identifier.go`'s `Validate`.

   What it buys is the reason it is not negotiable: **the bank code is
   variable-length and sits at a country-dependent offset** — 8 digits at
   position 4 in Germany, 5 at position 5 in Italy behind a CIN letter, 3 at
   position 4 in Sweden, 5 at position 4 in France. A fixed-width code at a fixed
   offset makes the entire directory implementable as `iban[4:8]`, and the
   shortcut would be *correct*. Under four countries it does not typecheck.

2. **A bank code is allocated data that travels on a message. It is not
   computed.** The settlement agent allocates it at admission and returns it on
   the `acmt.010`; the bank records what it was told, exactly as it already
   records the settlement account numbers it was told. Nothing derives it from a
   BIC, and with numeric codes in all four countries nothing could.

3. **The registry is the settlement agent's; the routing directory is the
   clearing house's.** Two tables, two questions — *who issued this address*
   versus *may this address be reached* — mirroring the Bundesbank's
   Bankleitzahl file and the EPC's Register of Participants. A bank with a code
   and no roster entry is issuing addresses nobody can pay, which is a state this
   system can now hold and a real one.

4. **Each bank holds its own copy of the routing directory**, refreshed by an
   explicit pull, and derivation at submission reads that copy — its own
   database. Cost: staleness is real. That is not a defect being tolerated; it is
   the behaviour of every real routing directory, and it is why a bank admitted
   this morning is not reachable from a bank that refreshed yesterday.

5. **A bank code is never reassigned.** This is the invariant the whole
   local-copy design rests on: a stale copy can only ever be *incomplete*, never
   *wrong*. The failure mode is "I cannot route this yet", never "I routed it to
   the wrong bank". Nothing in either task may introduce a path that reassigns
   one.

6. **The bank mints the IBAN; the caller may not supply one.** `AddIdentifier`
   refuses `scheme = IBAN`. Cost: `RemoveIdentifier` + `AddIdentifier` stops
   composing as a reissue, so reissue becomes an act of its own (§ Reissue).

7. **Identifiers are stored compact.** `DE20999000010000000001` in the register
   and on the wire — one form, and it is the canonical one. `MatchValue` survives
   with a changed justification: not "we store a display form" but "a customer
   types the grouped form off their statement, and input is normalised".

8. **Two check digits where a country has two.** mod-97-10 always; Italy's CIN
   and France's clé RIB additionally, computed properly. Cost: two more
   algorithms and two lookup tables. What it buys is the only place in this
   system where two independent checks run over one string and catch different
   errors — and the reason both exist is that national schemes predate the
   international one and were never retired.

9. **The clearing house publishes through the roster it already has.**
   `roster_entries` gains a `bank_code` column, learned from the same `acmt.010`
   that already writes the row; `GET /roster` gains a field. No new route, no
   new message, no new hop.

10. **The bank's copy carries bank code and BIC and nothing else.** Not the
    assets. An early refusal computed from a stale copy would refuse a payment
    the clearing house would have accepted, and the asset check already has an
    owner in `bothBanksAreMembersTx`.

11. **`ErrCounterpartyAgentNotNamed` is repurposed, not deleted**, and a third
    sentinel joins it. Three failures, three remedies (§ The three ways an
    address fails).

12. **One settlement agent stands in for four national registries, and the fudge
    is named.** In the world the registry is national — the Bundesbank runs the
    BLZ file, the Banca d'Italia the ABI/CAB — and SEPA spans them precisely
    *because* no one institution owns the mapping. That is why the real directory
    is a purchased, federated data product and not a table anyone can ask for.

## Three arguments this reverses, and where each is written

Each is currently stated as a decision with a reason, so each needs prose
written against it rather than a deletion.

**Task 20 reverses one.** `deposit/identifier.go`'s `Validate`:

> It does NOT check the format of the value. There is no mod-97 check digit on an
> IBAN here and no length rule, because enforcing one would make the seed's
> readable `SE89-AURORA-1001` illegal and replace it with opaque digits in every
> worked example in the repository.

The reason was never wrong; it was a trade, and Task 20 pays the other side of
it. What replaces the paragraph is the argument for why a check digit is worth
opaque digits: it is the only thing in the system that rejects a typo **offline,
before any lookup**, and a design whose next task is a lookup needs to show what
happens before one. The same claim appears at `README.md:1317` under *Deliberate
Simplifications* and must move out of that section.

**Task 21 reverses two.** `PartyDetails.Agent`:

> It is asserted anyway, because the row it could be derived from is the
> counterparty's own and a bank holds only its own.

and `ErrCounterpartyAgentNotNamed`:

> It is the routing element, and this system has nowhere to get it from.

Both become false in the same commit, and for the same reason: there is now
somewhere to get it from, and it is neither the counterparty's row nor a
cross-institution read. It is a table this bank holds, filled by a snapshot it
pulled. **The derivation Task 14 wanted returns; what changed is that it no
longer needs a row the deriving bank does not have.**

---

# Task 20 — an address with a check digit

## The `iban` package

New leaf package, `iban/`, importing nothing from this repository.

It exists as a package rather than as functions in `deposit` for one reason that
is not taste: **the settlement agent needs the same code.** It allocates bank
codes and must validate them against a country's structure, and a central bank
importing a member bank's deposit-register package to do so is a layering
inversion that exists nowhere else here.

```go
package iban

type Country string          // "DE", "IT", "SE", "FR"
type BankCode string         // numeric in all four; width is the country's

// IBAN is the compact, canonical form. Display grouping is a presentation
// concern and lives at the edges.
type IBAN string

func Parse(s string) (IBAN, error)        // normalises separators, then validates
func (i IBAN) Country() Country
func (i IBAN) BankCode() (BankCode, error)
func (i IBAN) Grouped() string            // groups of four, for display only
func (i IBAN) Validate() error            // mod-97, length, structure, national check

// New mints. The account serial is the caller's; every check character is this
// package's.
func New(c Country, bank BankCode, serial uint64) (IBAN, error)
```

`iso20022.IBAN` stays exactly as it is — the wire type, with the schema's
pattern. Its regex `^[A-Z]{2}[0-9]{2}[A-Za-z0-9]{1,30}$` already accepts every
IBAN this design mints, so **no message type changes in Task 20.**

`deposit.Identifier.MatchValue` stops hand-rolling `ibanSeparators` and
delegates. `TestMatchValueAgreesWithTheWireCompaction` **survives**, which this
section predicted wrongly: it assumed the copies collapsing to one, and there are
still two, because `iso20022` imports nothing from this repository. Two copies
means the pin is still doing its job.

**The two `Compact`s disagree about CASE, deliberately.** `iban.Compact` folds
it; `iso20022.IBAN.Compact` does not, because the schema's pattern requires an
upper-case country code and `Validate` has to be able to refuse one that is not.
The pin test therefore folds case on one side and says why.

That divergence has a consequence this section did not anticipate, and it is
where the design was actually wrong rather than merely incomplete: **a quoted
address has to be canonicalised where a person's typing arrives.** A payer types
an IBAN grouped and lower-cased; the register accepts it, because comparison is
canonical; but a quoted address for a party at ANOTHER bank never meets that
bank's register on the way out, so nothing replaces the spelling and the codec
refuses the document. `iban.Parse` is the front door, and `partyRefDTO.toDomain`
is where a request goes through it. A value that will not parse passes through
unchanged, so the refusal stays the domain's.

Two other things follow from the fold, and both bit:

- The store's `ListDepositAccountsByIdentifier` must `upper()` as well as
  `replace()`. Its Go counterpart folds case, and every direction the suite drove
  passed without it — an address is upper-cased everywhere it is minted, so the
  row that would notice is the one nothing writes.
- Browser-side validation is mod-97 **only**. The country table stays in Go;
  copying it into the client would put the rule in two places with nothing
  holding them together.

## The country table

Four entries. Offsets are zero-based into the compact IBAN, counting the country
code and check digits.

| Country | Len | BBAN structure | Bank code | Account field | National check |
|---|---|---|---|---|---|
| `DE` | 22 | `8!n 10!n` | 8 digits @ 4 | 10 digits @ 12 | — |
| `IT` | 27 | `1!a 5!n 5!n 12!c` | ABI, 5 digits @ 5 | 12 chars @ 15 | CIN letter @ 4 |
| `SE` | 24 | `3!n 17!n` | 3 digits @ 4 | 17 digits @ 7 | — |
| `FR` | 27 | `5!n 5!n 11!c 2!n` | banque, 5 digits @ 4 | guichet 5 @ 9, compte 11 @ 14 | clé RIB, 2 digits @ 25 |

Italy's CAB (branch, 5 digits @ 10) and France's guichet are carried and are
**not** part of the routing key: a branch code identifies an office within an
institution, and the directory answers at institution granularity because a BIC
does. That is worth a sentence in the table's doc, because it is the first thing
a reader will ask.

The table is `iban`'s and is a Go value, not data in a store. It changes when the
code changes.

## Two check digits, computed differently

**mod-97-10 (ISO 7064).** Move the first four characters to the end, map letters
to digits (`A`=10 … `Z`=35), take the whole thing mod 97; a valid IBAN gives 1.
To mint, substitute `00` for the check digits and take `98 − (n mod 97)`.

**Italy's CIN.** A character over the 22 BBAN characters after it, from two
lookup tables — one for odd positions, one for even — summed and taken mod 26 to
a letter.

**France's clé RIB.** `97 − ((89·banque + 15·guichet + 3·compte) mod 97)`, where
letters in the account number map through a table that is **not** IBAN's
`A`=10…`Z`=35. Two different letter-to-digit maps in one address is exactly the
detail worth having: it is what makes the two checks independent rather than one
check computed twice.

A worked example, hand-verified: Aurora is allocated German bank code
`99900001`, and its first account gets serial 1.

```
DE20 9990 0001 0000 0000 01
└┬┘ └┤ └────┬───┘ └─────┬──────┘
 │   │      │           └── Kontonummer, 10 digits, zero-padded serial
 │   │      └────────────── Bankleitzahl, 8 digits, allocated (§ Task 21)
 │   └───────────────────── mod-97-10 check digits
 └───────────────────────── ISO 3166 country, agreeing with AURODEFFXXX
```

Rearranged to `999000010000000001` + `DE20` → `999000010000000001131420`, which
is ≡ 1 (mod 97). The seed's golden values for the other three countries are
whatever the minter produces; this spec does not hand-compute them.

## Minting, and where the serial comes from

`Register.OpenAccountTx` mints the account's IBAN. The variadic `identifiers`
parameter survives for the schemes a caller genuinely holds (a card PAN is issued
by a scheme elsewhere and quoted) and refuses `IdentifierIBAN`.

The serial comes from a **dedicated counter row** in the bank's own
`id_sequences`, `name = 'iban'`. Not the shared `NextID` counter: that one is
shared by every prefix, so account addresses would come out `…0001`, `…0007`,
`…0019` as they interleave with transaction and event ids. The primary key
`(book_id, name)` already permits a second row, and the counter follows the row —
the rule `bank/0001_init.sql` states — so a rolled-back account open does not
burn an address.

Serials are zero-padded into the country's account field. Aurora's field is 10
digits, Nordhaven's is 17.

**The seed's thousands prefix disappears.** `SE89-AURORA-1001` … `-1005` and
`IT60-VERDE-2001` … `-2003` used the leading digit to say which bank; the bank
code says it now, properly, so Aurora's five accounts are serials 1–5 and Verde's
three are 1–3.

## Reissue

`README.md:893` leans on reissue being "a remove plus an add, which moves neither
balance nor history", and it is the worked example for why a mandate compares its
parties by `(participant, account)` and survives one. With the add refused, that
pair no longer composes.

`Register.ReissueIdentifier(ctx, id AccountID) (Identifier, error)` — mints a new
IBAN and withdraws the old one in one transaction. Closer to what a bank does,
and it **preserves the mandate lesson unchanged**: the mandate still survives,
and the test that proves it still proves it.

`RemoveIdentifier` survives for non-IBAN schemes.

## Schema

`deposit_account_identifiers` needs **no structural change**. Its value column is
already unconstrained, and `bank/0001_init.sql` argues that the format rule lives
in Go and that a `CHECK` would state it a second time in the place least able to
change. That argument is now *stronger*, not weaker: the rule has grown a country
table and two national algorithms, and none of them is expressible in SQLite.

What does change is the comment. The current one says the format is unvalidated;
the new one says where the validation is and why it is not here.

---

# Task 21 — the registry, the directory, and IBAN-only

## Three tables, three institutions

**Settlement agent** — a new `bank_codes` registry:

```
country       TEXT      -- with the bank code, the primary key
code          TEXT
bic           TEXT NOT NULL
allocated_at  TEXT
seq           INTEGER NOT NULL
```

Keyed `(country, code)` because the code is national and only unique within one.
Its refusal is a duplicate. It is the issuer's own record and no bank ever reads
it.

**Clearing house** — `roster_entries` gains `bank_code TEXT NOT NULL DEFAULT ''`
and the country beside it. It learns both for free: the `acmt.010` that already
writes this row now carries them.

Its refusal mirrors the existing BIC one — a code already in the roster under a
*different* admission is rejected. That is belt-and-braces given the settlement
agent guarantees uniqueness, and it earns its place: the roster is what every
member **copies**, so a duplicate here would make one address ambiguous for the
whole scheme, and the clearing house cannot see the registry to check.

**Each bank** — a new `routing_directory`:

```
country       TEXT      -- with the bank code, the primary key
bank_code     TEXT
bic           TEXT NOT NULL
refreshed_at  TEXT NOT NULL
```

Bank code to BIC, and nothing else. `refreshed_at` is per row and is the whole of
the staleness story: a console showing "14 banks, refreshed 3 days ago" teaches
the subscription model in one line.

Every one of these comments goes **inside** the statement's parentheses, per
`CLAUDE.md` — and the absences here are substantial enough to need it: no name on
the bank's copy, no assets on it, no account of any kind on the roster.

## The code travels: `Org/OrgId/Othr`

`OrganisationIdentification29` has an `Othr` arm —
`GenericOrganisationIdentification1{Id, SchmeNm, Issr}` — which is exactly "an
identifier issued to this organisation by that issuer under that scheme".
`iso20022/party.go:100` already records the omission in as many words:

> Only AnyBIC is carried. The standard also allows an LEI and a list of generic
> identifiers.

So the extension is filling in a gap the package has already named:

```go
type GenericOrganisationIdentification struct {
    Id      string     `xml:"Id"`      // the bank code
    SchmeNm SchemeName `xml:"SchmeNm"` // the national clearing-system code
    Issr    string     `xml:"Issr"`    // the allocating institution's BIC
}
```

`SchmeNm` carries the ISO external clearing-system identification code where the
country has one — `DEBLZ`, `ITNCC`, `SESBA` are all on that list. **France is
the interesting case**: French domestic routing is IBAN-only and there is no
equivalent entry to reach for, so `FR` uses the proprietary arm. Verify that
against the current external code list before writing the comment; if a code
exists, use it. Either way the asymmetry is worth a sentence, because it is the
same fact this whole task is about, seen from the standard's side.

The **wrong** element, for the record, is `ClrSysMmbId` on
`FinancialInstitutionIdentification` — canonically the national clearing code,
but in an `acmt.007`/`acmt.010` `FinInstnId` identifies the account *servicer*,
which is the central bank, not the applicant. It becomes right again in a
`pacs.008`'s agents, which is out of scope here.

## The subscription is a snapshot pull

`POST /directory/banks/refresh` on a bank's own port: the bank fetches the
clearing house's roster and **replaces** its copy wholesale. A snapshot, because
that is what a directory file is — not a delta feed.

Not a timer, because a background poller in a repository whose suites run on a
fake clock buys realism and pays in flaky tests. Not a push, because a clearing
house holding a subscriber list and a retry policy is a delivery system rather
than a publisher, and the real vendor does not know who is listening.

A refresh appends an audit event in that bank's own log.

## Derivation, and the three ways an address fails

`SubmitPaymentTx` derives the counterparty's agent from the counterparty's IBAN,
through this bank's own `routing_directory`. `InitiatePaymentRequest` loses
`creditorAgent`/`debtorAgent`. The name stays asserted — no lookup can supply it,
and nothing in this task changes that.

Three sentinels, three remedies:

| Sentinel | Means | Remedy |
|---|---|---|
| `ErrCounterpartyNotNamed` | no name on the instruction | type a name |
| `ErrCounterpartyAgentNotNamed` | *repurposed*: this address's scheme has no directory here, and no agent was named | supply a BIC |
| `ErrBankCodeUnknown` | *new*: the bank code resolves to nothing in this bank's copy | refresh, or the payee's bank is not in this scheme |

The middle one keeps a door the README already says is real open — a card PAN, a
proxy alias, a cross-border transfer where the BIC genuinely is the payer's to
supply. Its own doc already anticipates this: *"without it an address is an IBAN
and a BIC, as SEPA's was before 2016 and a cross-border transfer's still is."*

`ErrBankCodeUnknown` is **422**, like every other domain refusal here. And its
doc has to state the thing this design makes genuinely unknowable: **the bank
cannot tell which of two situations it is in** — no such bank exists in this
scheme, or its own copy is behind. Those have different remedies and the refusing
bank has no way to distinguish them. That is a direct consequence of holding a
copy rather than asking, and a status code claiming to know which failure it is
would be lying about it.

## Mandates

`Mandate.DebtorAgent` is derived at **creation**, from the debtor's IBAN, and
stored. It is never re-derived at collection.

A mandate authorises debits from an account at the bank the debtor signed up
against; an authorisation that silently followed a directory to a different
institution is a behaviour no real scheme has. This is also consistent with the
existing rule that a mandate compares parties by `(participant, account)` and
survives a reissued IBAN.

`README.md:1818`'s consequence survives verbatim: the debtor is recorded and not
checked, so a mandate naming an account that does not exist is still created and
still fails its first collection.

## API surface

| Route | Port | Answers |
|---|---|---|
| `GET /directory/accounts?scheme=&value=` | bank | which of **my** accounts holds this address |
| `GET /directory/banks?country=&bankCode=` | bank | which bank holds this code, **from my copy** |
| `POST /directory/banks/refresh` | bank | pull a fresh snapshot |
| `GET /roster` | clearing house | the published directory, now with `bankCode` |

`GET /directory` is renamed to `GET /directory/accounts`, which breaks a
documented surface — `README.md:1865`'s paragraph, the curl walkthrough, the web
client and the api tests all move. It is worth it: the two questions are
siblings, one answered from a register this bank owns and one from a copy of a
table it does not, and the URL should say so. The old flat name is what made
"directory" ambiguous the moment a second directory arrived.

`GET /directory/banks` answers a **BIC and no name**, because the copy has none,
because the roster has none, because the `acmt.010` delivers none. Three
documented decisions, reused rather than a fourth invented.

## Web

**Task 20** — mod-97 in the browser, rejecting a typo before any request is sent.
That is the entire point of a check digit and it should be visible where a user
would meet it.

**Task 21** —

- The send form loses its BIC field.
- Once the IBAN passes mod-97, the form shows **which bank it routes to**,
  resolved through `GET /directory/banks`. This is the beat that makes IBAN-only
  visible rather than merely true. It shows a BIC and cannot show "Banca Verde" —
  the documented absence, arriving at exactly the moment a user expects a name.
- A bank's console lists its routing copy with `refreshed_at`, and offers the
  refresh.

## Reconciliation

`payment/recon` opens all N+2 databases precisely because no institution may, so
it is where an addressing disagreement gets caught. Two new invariants and one
new **report**:

1. *Invariant.* Every deposit account's IBAN bank code resolves, in the
   settlement agent's registry, to the BIC of the bank whose database holds the
   account. Catches an account addressed under someone else's code — the defect
   that would make a payment routable to the wrong institution.
2. *Invariant.* Every roster entry's `bank_code` equals the registry's code for
   that BIC. Catches the clearing house's copy having drifted from the issuer's.
3. *Report, and never a failure.* Each bank's `routing_directory` against the
   published roster. **A stale copy is legal by construction** — it is the
   behaviour decision 4 was chosen for — so a recon that failed on it would
   assert the opposite of the design. It says "Aurora's directory is 2 entries
   behind" and passes.

Getting (3) wrong is the most likely way this design is quietly undone later, so
it belongs in prose in `payment/recon`'s package doc, not only in a test.

## Testing

Task 20:

- `iban`: mint-then-validate round trip per country; every seeded address
  verified against a second, independently written mod-97; CIN and clé RIB
  against known-good vectors.
- **A measured number, per `CLAUDE.md`**, and it is now measured rather than
  predicted. Over the four published addresses, exhaustively: **810 of 810**
  single-digit substitutions caught, and **787 of 787** transpositions of two
  different digits — at every distance, not only adjacent ones. The second is
  stronger than the property usually quoted, and it is not luck: transposing at
  distance *d* changes the value by a multiple of 10^d − 1, and 10 has
  multiplicative order 96 modulo 97, so no address short of 96 characters can
  hide one.

  What it misses is the honest half: over `DE89370400440532013000`, every pair
  of digit positions and every pair of replacement digits — **141 of 15,390
  two-character errors undetected, 0.92%**, near the 1.03% a uniform residue
  would give. That figure is the argument for the national checks and for
  everything downstream: a check digit says an address was probably typed
  correctly, never that it exists.
- The French case where the clé RIB catches what mod-97 does not.
- `AddIdentifier` refuses an IBAN; `ReissueIdentifier` mints, withdraws, and
  leaves the mandate working.
- `storetest`: `ListDepositAccountsByIdentifier` still matches a grouped input
  against a compact stored value.

Task 21:

- **The staleness case first.** Admit a bank; a second bank that has not
  refreshed refuses a payment to it with `ErrBankCodeUnknown`; it refreshes; the
  same payment succeeds. This is the test that says what the design *is*.
- A bank code is never reassigned — asserted where allocation happens.
- Two banks cannot hold one code, refused at the registry, and refused again at
  the roster.
- Derivation puts the same BIC on the message that the payer used to type, and
  `TestAWrongCounterpartyAgentDoesNotMisroute` becomes unreachable-by-
  construction rather than defended: there is no field to put a wrong agent in.
- `mesh/recon_test.go` gains broken states for invariants 1 and 2, and a stale
  state that must **pass**.

## Documentation layers

`CLAUDE.md`'s rule: a domain fact corrected in one layer is corrected in all.

- `README.md` — *Account Addressing* rewritten in Task 20 (the mod-97 refusal
  leaves *Deliberate Simplifications*); *Payments* rewritten in Task 21
  (`README.md:866–899`, which currently argues at length that no such directory
  can exist here). The curl walkthrough loses `creditorAgent` and gains a
  refresh.
- `web/src/components/hint-content.ts` — new keys for the bank code, the
  directory and the check digit. Every `[[wiki-link]]` must resolve or **every**
  dev route dies at runtime; `npm run test` catches it in hint bodies and quiz
  explanations both.
- `web/src/lib/quiz/chapters/*.ts` — the addressing chapter and the payments
  chapter. `diversity.test.ts` holds each to 18–22 questions, ≥8 distinct
  `concept` tags, no tag more than 3×, and all three difficulty tiers, so
  additions are swaps, not appends.
- The three `0001_init.sql` files — one new table in `centralbank`, one new table
  in `bank`, one new column in `csm`, and the arguments go **inside** the
  parentheses.
- `docs/expansion-roadmap.md` — mark shipped, and add what § below defers.

## Tasks

**Task 20 — shipped.**

1. `iban` package: country table, mod-97, CIN, clé RIB, `Parse`/`New`/`Grouped`.
2. `deposit`: mint in `OpenAccountTx`; `AddIdentifier` refuses IBANs;
   `ReissueIdentifier`; `MatchValue` delegates; the pin test survives (§ above).
3. `id_sequences`: the `iban` counter.
4. Regenerate every literal — `seed`, `storetest`, api and payment tests, the
   `iso20022` goldens, web fixtures.
5. `api`: canonicalise a quoted address at the door (§ above).
6. `store/sqlite`: `upper()` in the identifier lookup, and a `storetest` case.
7. Docs: README addressing section, hint keys, quiz, schema comments.
8. Web: client-side mod-97.

**Task 21**

7. `iso20022`: `Othr` on `OrganisationIdentification`; read and write it in
   `acmt.007`/`acmt.010`.
8. Settlement agent: `bank_codes`, allocation at admission, the duplicate
   refusal.
9. Clearing house: `bank_code` on the roster, the second refusal, the field on
   `GET /roster`.
10. Bank: `routing_directory`, `POST /directory/banks/refresh`,
    `GET /directory/banks`, `GET /directory` renamed.
11. `payment`: derivation in `SubmitPaymentTx`, `ErrBankCodeUnknown`, the
    repurposed sentinel, mandate derivation, the DTO fields removed.
12. `payment/recon`: two invariants and the staleness report.
13. `seed` and `storetest.Admit`: allocate codes.
14. Docs: README payments section, hints, quiz, schema comments.
15. Web: send form, live resolution, the console's directory view.

## What this does NOT do

**Virtual IBANs.** A PSP issuing addresses under another institution's bank-code
range is the case that breaks "the bank code identifies the account holder's
bank" — and it is a live regulatory argument in SEPA, not a hypothetical. The
whole of Task 21 rests on that assumption holding. Named here so the next reader
knows it was seen, not missed.

**Verification of Payee.** The natural sequel, and the reason it is a task and
not a paragraph: once the payer stops typing a BIC, the name is the only thing
they assert, which is exactly why the Instant Payments Regulation made
name-checking mandatory across SEPA in October 2025. It has a real message pair —
`acmt.023` IdentificationVerificationRequest and `acmt.024`
IdentificationVerificationReport — and it would be **the first sanctioned crack
in "no bank reads another bank's register"**: a bounded question with a bounded
answer. That is a rule reversal, and rule reversals get their own commit.

**The full ISO 13616 registry.** Four countries, not eighty. The registry is
licensed reference data in the real world, which is the more interesting fact and
belongs in prose rather than in a table nobody opens an account against.

**Reachability in the bank's copy.** Decision 10. The asset check stays the
clearing house's.

**Branch-level routing.** Italy's CAB and France's guichet are carried in the
address and are not part of the key, because a BIC identifies an institution.

**Bank codes that cannot collide with real ones.** The seed's values are
fictional and chosen to look implausible, but nothing here can guarantee that an
8-digit Bankleitzahl is unissued. An elision, and it is the same kind as the BICs
already in the fixtures.
