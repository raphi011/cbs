# ISO 20022 Messages — Design

Sub-project 7a in `docs/expansion-roadmap.md`, the first of three that replace
the repository's largest remaining payments fiction:

> **No ISO 20022 message parsing.** The `Payment` struct stands in for
> `pain.001`/`pacs.008`/`pacs.003`; the schemes only *name* the messages they
> correspond to. (`README.md:1016`, `payment/doc.go:61`)

The arc, so this spec can be read knowing where it stops:

- **7a — this spec, the `iso20022` package.** Message types, the `head.001`
  envelope, the XML codec, the external code sets. No domain imports, no
  behaviour change anywhere else in the repository.
- **7b — the mesh and the actors.** One goroutine per participant bank, one for
  the clearing house, one for the central bank, exchanging marshalled bytes over
  channels. `api/` moves to `202 Accepted` plus a status query, because a real
  CSM answers with a `pacs.002` later and not with a return value.
- **7c — the message log, the UI and the teaching layers.** Envelopes persisted
  in both stores so a payment screen can show the XML that actually moved, plus
  `README.md`, `hint-content.ts` and the quiz.

Each gets its own spec, plan and branch. This one is deliberately the piece with
no consumer, because the alternative — designing the wire format and the actors
that speak it in one pass — decides the hard questions (what a `pacs.002`
carries, what a rejection is called) as a side effect of writing the transport.

## Goal

Make the messages real. A `pacs.008` produced by this package must be a
`pacs.008`: correct namespace, correct element names in the correct order,
correct code values — the kind of document that could be pasted into a
counterparty's parser and understood.

The point is not interoperability, which this repository has no use for. It is
that *the message is the interface between two banks*, and a reader cannot learn
that from a Go struct someone invented. Every field in this package exists in
the standard, and the ones that are absent are absent on purpose and listed.

### What "real" is bounded by

The base ISO 20022 `pacs.008` allows a great deal that SEPA forbids. The EPC
Implementation Guidelines are a **constrained subset**: IBAN-only accounts, EUR
only, `SLEV` charge bearer, one `Ustrd` remittance line of 140 characters. This
package implements the EPC subset, and says so in the package doc, because the
relationship between a standard and a scheme's profile of it is itself one of
the things worth teaching: the standard is a superset, and the scheme narrows
it until only one thing can be meant.

## Out of scope, deliberately

- **`pain.001` / `pain.008`.** The customer-to-bank layer. This repository's
  `Network` sits at the interbank boundary, and a customer instruction reaching
  a bank is currently an HTTP call — which is not wrong, merely a different
  channel. Adding the `pain` → `pacs` translation is a genuine lesson, and it is
  a second one; it does not belong in the sub-project that establishes the
  vocabulary.
- **`camt.052` / `camt.053` / `camt.054`.** Reporting back from the central bank
  after settlement. Wanted, but the actor that would send them does not exist
  until 7b.
- **`camt.056` recall, `pacs.007` reversal.** The `payment` package models
  returns (`Network.ReturnPayment`, `payment/system.go:1197`) and nothing else
  in the R-transaction family. A message with no domain operation behind it
  would be a struct nobody constructs.
- **XSD validation at runtime.** There is no usable pure-Go XSD validator, and
  taking a cgo dependency on libxml2 to validate documents this repository
  generates itself would cost the "no dependencies beyond pgx" property for very
  little. The substitute is stated under *Testing*: round-trip against committed
  EPC sample documents, plus an optional `xmllint` check that skips when the
  binary is absent.
- **Digital signatures, `Sgntr`, and any transport security.** A real CSM
  connection is mutually authenticated and the messages are signed. Nothing in
  this repository models an untrusted counterparty.
- **IBAN check digits.** See the decision below; this is inherited, not new.

## Decisions

### The package imports nothing from this repository

`iso20022` depends on the standard library alone. Not `ledger`, not `deposit`,
not `payment`.

This is not tidiness. The package's whole claim is that these types are the
*standard's* types rather than this system's types dressed up, and an import of
`ledger.Amount` would quietly make that false — the next reader could no longer
tell which fields came from ISO 20022 and which came from here. It also means
the package can be read, and its tests understood, by someone who knows the
standard and nothing about this codebase.

The cost is one conversion boundary, which 7b owns as
`payment/translate.go`. That is the correct place for it: mapping a domain type
onto a wire type is exactly the work a translator does, and it is easier to
review when it is a file rather than a scattering of methods.

### Amounts convert through an explicit scale, and refuse to round

`ActiveCurrencyAndAmount` is a decimal string with a `Ccy` attribute:
`<IntrBkSttlmAmt Ccy="EUR">1234.56</IntrBkSttlmAmt>`. This repository holds
money as `ledger.Amount`, an `int64` of minor units, with the number of decimal
places on the asset (`ledger.AssetDef.Scale` — 2 for EUR, 8 for BTC,
`ledger/types.go:135`).

Because the package may not import `ledger`, the scale is a parameter:

```go
func NewAmount(minor int64, scale uint8, ccy string) (ActiveCurrencyAndAmount, error)
func (a ActiveCurrencyAndAmount) Minor(scale uint8) (int64, error)
```

`Minor` **fails** on a decimal carrying more places than the scale allows,
rather than rounding. `0.005` in a EUR message is not half a cent that should
become one cent or zero; it is a message that means something this system cannot
represent, and quietly picking an answer would put a rounding error into a
ledger that is otherwise exact by construction. The same rule the `ledger`
package already applies to itself.

This is a real ISO 20022 hazard rather than an invented one: the type permits
five fraction digits for any currency, and the currency's own exponent is the
only thing that narrows it.

### IBANs are compacted, not validated — and that turns out to be free

Sub-project 5, account addressing, shipped a refusal of ISO 7064 mod-97
validation, on the grounds that it would make the seed's readable
`SE89-AURORA-1001` illegal and replace it with opaque digits in every
screenshot, worked example and quiz answer. It is now a stated property of the
system (`README.md:1017`), not merely a spec decision. That argument stands and
is not reopened here.

It also costs nothing, which is worth recording because it looks like it should.
The `pacs.008` XSD constrains an IBAN by **pattern** —
`[A-Z]{2,2}[0-9]{2,2}[a-zA-Z0-9]{1,30}` — and not by check digit. An IBAN is
canonically *stored* without separators and *displayed* in groups of four, so
`SE89-AURORA-1001` goes on the wire as `SE89AURORA1001`, which matches the
pattern exactly. Readable identifiers survive, the document is structurally
valid, and mod-97 remains unenforced precisely as decided.

`iso20022.IBAN` is therefore a defined string type with `Compact()` and
`Formatted()` and a **pattern** check (`ErrIBANPattern`), not a checksum check.
The distinction is documented on the type, because "we validate IBANs" is the
claim a future reader will otherwise assume.

### `Participant` gains a BIC, superseding a deferral

Sub-project 5 put bank-level addressing out of scope — *"Participants keep being
addressed by `ParticipantID`. Resolving an IBAN yields the participant, which is
the only thing the routing needs."* — and shipped the consequence as a stated
simplification: *"a BIC is not modelled at all"* (`README.md:1017`,
`payment/doc.go:65`).

That was true when nothing needed a BIC. `DbtrAgt` and `CdtrAgt`, each a
`BranchAndFinancialInstitutionIdentification6` carrying a `BICFI`, are
**mandatory** in the EPC `pacs.008` — a message cannot be written without them.
So the deferral is now spent rather than wrong, and the addressing spec gains a
one-line supersession note pointing here.

This spec ships `iso20022.BIC`: a defined string type with the ISO 9362
structural check (4 alphabetic institution + 2 alphabetic country + 2
alphanumeric location, optionally 3 alphanumeric branch; 8 or 11 characters, no
9 or 10). Unlike the IBAN case there is no readability cost — nothing in the
seed, the screenshots or the quiz quotes a BIC today, so the seeded BICs can be
well-formed from the start.

`Participant.BIC` itself, and routing by it, land in **7b**. This
spec ships only the type, so that the field added there has somewhere to be
validated.

### Actors will exchange bytes, so the codec is the package's real surface

Recorded here because it constrains this package even though the actors are
7b's: the mesh will pass **marshalled XML**, not structs. If two
actors exchanged `*Pacs008` the message format would be decoration on a function
call, malformed input would stop being a reachable failure mode, and the
`FF01` rejection path would be untestable.

So `Marshal`/`Unmarshal` are load-bearing, not conveniences, and `Unmarshal`
must be robust to hostile input: unknown elements ignored, missing mandatory
elements reported as a named error, the wrong `MsgDefIdr` for the enclosed
`Document` reported as a named error.

### The envelope is `head.001` plus a wrapper this repository names

There is no single "ISO 20022 envelope", and pretending otherwise would teach
something false. What is standard is the **Business Application Header**,
`head.001.001.02` — `Fr`, `To`, `BizMsgIdr`, `MsgDefIdr`, `CreDt` — which
carries the routing identity and the message-definition identifier. What is
*not* standard is how the header and the document are packaged together for a
particular network: SWIFT CBPR+ wraps them one way, EBA CLEARING STEP2 files
another, and each CSM's file/bulk framing is its own.

The package therefore defines:

```xml
<Envelope>
  <AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02"> … </AppHdr>
  <Document xmlns="urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08"> … </Document>
</Envelope>
```

and the package doc states plainly that the two inner elements are the standard
and the outer one is this repository's stand-in for a CSM-specific wrapper. A
reader who later meets a real STEP2 file finds the difference explained rather
than surprising.

`MsgDefIdr` in the header is what `Unmarshal` dispatches on, which is the same
thing a real receiver does — and is why the header is not optional here.

### Code sets are typed constants, and the mapping from `payment` errors is fixed now

ISO 20022 rejection reasons are *external code sets*: four-character codes
maintained outside the schema. They are the difference between a machine-
actionable rejection and `RejectReason string` (`payment/types.go:210`).

The package ships them as defined types with named constants — `StatusReason`,
`ReturnReason`, `TransactionStatus`, `SettlementMethod`, `ChargeBearer` — each
constant carrying the standard's own definition as its doc comment.

The mapping is decided here rather than in 7b, because it is a
statement about what this system's errors *mean* and that is a domain question,
not a transport one:

| `payment` error | Code | Standard's meaning |
| --- | --- | --- |
| `ErrAccountNotInParticipant` (creditor side) | `AC01` | IncorrectAccountNumber |
| creditor account closed | `AC04` | ClosedAccountNumber |
| `deposit.ErrInsufficientAvailable` | `AM04` | InsufficientFunds |
| `ErrDuplicateEndToEndID` | `AM05` | Duplication |
| `ErrMandateRequired`, `ErrMandateNotFound`, `ErrMandateRevoked` | `MD01` | NoMandate |
| `ErrMandateMismatch`, `ErrMandateExceeded` | `MS03` | NotSpecifiedReasonAgentGenerated |
| `ErrParticipantNotFound`, unresolvable `BICFI` | `RC01` | BankIdentifierIncorrect |
| `ErrUnaddressableAccount`, `ErrIdentifierMismatch` | `RR01` | MissingDebtorAccountOrIdentification |
| `ErrCycleNotOpen` | `TM01` | InvalidCutOffTime |
| malformed XML, failed `Unmarshal` | `FF01` | InvalidFileFormat |
| `ErrAssetMismatch`, `ErrSchemeNotFound`, `ErrInvalidPaymentAmount` | `MS03` | NotSpecifiedReasonAgentGenerated |

Two of these deserve their reasoning on the record.

`ErrMandateExceeded` and `ErrMandateMismatch` map to `MS03` and not to a
mandate-specific code because the external set has no code for "a valid mandate
exists, but this collection is outside it". `MD01` means there is **no**
mandate, which is a different and more serious claim — and the right one for
`ErrMandateRevoked`, since a revoked mandate is precisely the absence of a valid
one. Reaching for the nearest-looking code would put a false statement on the
wire; `MS03` plus `AddtlInf` says less, accurately.

`ErrAssetMismatch` maps to `MS03` for a stronger reason: in SEPA it cannot
happen. The scheme is euro-only, so a currency mismatch is not a rejection
reason the code set ever needed. That this repository can produce the error at
all is a consequence of its multi-asset ledger, and the honest wire
representation of a condition the scheme does not contemplate is "unspecified".
The comment on that mapping says so, so it does not read as laziness.

The table lives in `payment/translate.go` in 7b. It is decided here.

### Optional elements are pointers, and choices are validated rather than typed

Two `encoding/xml` limitations shape the structs, and both are documented in
`doc.go` rather than discovered:

- `omitempty` does not suppress an empty **struct**, so every optional composite
  element (`PmtTpInf`, `RmtInf`, `StsRsnInf`, `SttlmInf.ClrSys`) is a pointer.
  A non-pointer optional field would emit `<RmtInf></RmtInf>` into every
  message, which is not merely ugly — it is invalid against the schema, whose
  child of `RmtInf` is mandatory when `RmtInf` is present.
- `encoding/xml` cannot express `xsd:choice`. `AccountIdentification4Choice`
  (`IBAN` xor `Othr`) and `StatusReason6Choice` (`Cd` xor `Prtry`) are structs
  of pointers with a `validate() error` that enforces exactly-one. `Marshal`
  calls `validate` on the whole tree before emitting, so an invalid choice is a
  Go error rather than a document a counterparty rejects.

This is the one place the package is shaped by Go rather than by the standard,
which is why it is a decision and not an implementation note.

## The message set

Four messages, each in its own file. Fields listed are those the EPC subset
makes mandatory plus the optional ones this system will actually populate;
anything else is omitted and the file's doc comment says what was omitted.

### `pacs.008.001.08` — `FIToFICstmrCdtTrf`

The SEPA Credit Transfer interbank message. `SCT` (`payment/scheme.go:108`)
already names it.

- `GrpHdr`: `MsgId`, `CreDtTm`, `NbOfTxs`, `TtlIntrBkSttlmAmt`,
  `IntrBkSttlmDt`, `SttlmInf{SttlmMtd: CLRG, ClrSys{Prtry}}`
- `CdtTrfTxInf[]`: `PmtId{InstrId, EndToEndId, TxId}`,
  `PmtTpInf{SvcLvl{Cd: SEPA}}`, `IntrBkSttlmAmt`, `ChrgBr: SLEV`,
  `Dbtr{Nm}`, `DbtrAcct{Id{IBAN}}`, `DbtrAgt{FinInstnId{BICFI}}`,
  `CdtrAgt{FinInstnId{BICFI}}`, `Cdtr{Nm}`, `CdtrAcct{Id{IBAN}}`,
  `RmtInf{Ustrd}`

`CdtTrfTxInf` is a slice because the message is inherently a **bulk** — which is
why STEP2 answers with a status *file*, and why 7b's `pacs.002`
distinguishes `GrpSts` from per-transaction `TxSts`. 7b will send
one transaction per message at first; the slice is not speculative generality,
it is the message's actual cardinality, and flattening it would misteach the
thing that makes retail clearing batch-shaped.

### `pacs.003.001.08` — `FIToFICstmrDrctDbt`

SEPA Direct Debit collection; `SDD` (`payment/scheme.go:132`) names it.

Same envelope and group header. `DrctDbtTxInf[]` adds
`DrctDbtTx{MndtRltdInf{MndtId, DtOfSgntr, AmdmntInd}}` and `CdtrSchmeId`, and
reverses the agent roles — the creditor's bank sends. `MndtRltdInf.MndtId` is
where `Payment.MandateID` (`payment/types.go:206`) goes, which makes the
mandate a thing that travels with the collection rather than a foreign key the
network happens to hold.

`DtOfSgntr` is mandatory. `payment.Mandate` (`payment/types.go:227`) has
`CreatedAt` and no signature date, so 7b either adds one or maps
`CreatedAt` and documents the elision. Flagged here; decided there.

### `pacs.002.001.10` — `FIToFIPmtStsRpt`

The status report. This is the message that makes clearing asynchronous, and it
has no counterpart in the current model at all.

- `GrpHdr{MsgId, CreDtTm}`
- `OrgnlGrpInfAndSts{OrgnlMsgId, OrgnlMsgNmId, OrgnlCreDtTm, GrpSts}`
- `TxInfAndSts[]{OrgnlEndToEndId, OrgnlTxId, TxSts, StsRsnInf{Rsn{Cd}, AddtlInf}}`

`TxSts` values shipped: `ACCP` accepted, `ACSP` accepted-settlement-in-process,
`ACSC` accepted-settlement-completed, `RJCT` rejected. Those four map exactly
onto `PaymentStatus` (`payment/types.go:86`) — `Accepted`, `Cleared`, `Settled`,
`Rejected` — which is a pleasant confirmation that the existing lifecycle was
modelled on the right thing. `Returned` has no `TxSts`; a return is a
`pacs.004`, not a status.

`GrpSts` and `TxSts` are separate because a bulk can be partly rejected
(`GrpSts: PART`). 7b needs that distinction the first time a cycle
contains one bad payment.

### `pacs.004.001.09` — `PmtRtr`

The R-transaction. `Network.ReturnPayment` (`payment/system.go:1197`) already
implements the operation.

- `GrpHdr{MsgId, CreDtTm, NbOfTxs, TtlRtrdIntrBkSttlmAmt, IntrBkSttlmDt, SttlmInf}`
- `TxInf[]{RtrId, OrgnlGrpInf{OrgnlMsgId, OrgnlMsgNmId}, OrgnlEndToEndId,
  OrgnlTxId, OrgnlIntrBkSttlmAmt, RtrdIntrBkSttlmAmt, ChrgBr,
  RtrRsnInf{Rsn{Cd}, AddtlInf}, OrgnlTxRef}`

`RtrdIntrBkSttlmAmt` is separate from `OrgnlIntrBkSttlmAmt` because a return may
be partial. This system's returns are always whole (`ReturnPayment` takes no
amount), so the two are equal and the file says why the field is nonetheless
present: dropping it would make the message unable to express something the
standard is specifically shaped to express, and a reader comparing against the
real schema would find a hole with no explanation.

## Package layout

```
iso20022/
  doc.go        the standard, the EPC subset, the encoding/xml constraints
  envelope.go   Envelope, AppHdr, Party44Choice
  codec.go      Marshal, Unmarshal, MsgDefIdr dispatch, errors
  party.go      BIC, IBAN, PartyIdentification, CashAccount,
                AccountIdentification4Choice, BranchAndFinancialInstitutionIdentification6
  amount.go     ActiveCurrencyAndAmount, minor-unit conversion
  codes.go      SettlementMethod, ChargeBearer, TransactionStatus,
                StatusReason, ReturnReason, ServiceLevel
  pacs008.go pacs003.go pacs002.go pacs004.go
  testdata/     committed sample documents
```

Errors: `ErrUnknownMessageDefinition`, `ErrMessageDefinitionMismatch`,
`ErrMissingElement`, `ErrInvalidChoice`, `ErrBICFormat`, `ErrIBANPattern`,
`ErrAmountScale`. All sentinels, `errors.Is`-checkable, following the convention
in `ledger/errors.go` and `payment/errors.go`.

## Testing

The package has no domain to test against, so its tests are about the documents.

- **Golden round-trip, both directions.** For each message, a committed sample
  in `testdata/` derived from the EPC Implementation Guidelines: unmarshal it,
  marshal the result, and compare against a committed `.golden.xml`; then
  unmarshal the golden and deep-equal the two struct trees. The first direction
  catches a field that serialises wrongly, the second catches a field that
  silently fails to parse. Only the second is caught by a struct-equality test
  alone, which is why both run.
- **Element order.** XML sequence order is part of the schema, so the golden
  comparison is byte-wise after whitespace normalisation, not
  order-insensitive. A reordered struct field is a failing test.
- **Optional-element suppression.** A message built with every optional field
  unset must contain no empty composite elements. This is the `omitempty`
  hazard above, pinned rather than commented.
- **Choice validation.** `AccountIdentification4Choice` with both `IBAN` and
  `Othr` set, and with neither, both fail `Marshal` with `ErrInvalidChoice`.
- **Amount scale.** `1234.56` at scale 2 is `123456`; `1234.5` at scale 2 is
  `123450`; `1234.567` at scale 2 is `ErrAmountScale`, not `123456` and not
  `123457`.
- **BIC and IBAN.** Table-driven, including the 9- and 10-character BICs that
  look plausible and are not, and the seed's `SE89-AURORA-1001` compacting to a
  pattern-valid `SE89AURORA1001`.
- **Hostile input to `Unmarshal`.** Truncated XML, an `AppHdr` whose `MsgDefIdr`
  disagrees with the enclosed `Document`, an unknown `MsgDefIdr`, a document
  missing a mandatory element. Each maps to its named error; none panics. Plus
  `go test -fuzz` on `Unmarshal` with a short default corpus, because this is
  the one function in the repository that will eventually consume bytes it did
  not produce.
- **Optional `xmllint`.** A test that shells out to `xmllint --noout --schema`
  against XSDs in `testdata/xsd/` when the binary is present and `t.Skip`s when
  it is not. It cannot be required — CI and contributors will not all have it —
  but when it does run it is the only check that this package's output is
  actually schema-valid rather than merely round-trip-stable, so it is worth
  the skip.

`go test ./...` with no `DATABASE_URL` and `TEST_DATABASE_URL=… go test ./...`
must both stay green. Neither touches this package, which has no store — but the
standing rule is that both runs are green, not that both are relevant.

## Documentation

Deliberately almost none, and this is the sub-project's main cost.

Nothing user-visible changes: no endpoint, no screen, no seeded behaviour. The
`README.md:1016` and `payment/doc.go:61` simplification bullets stay **true**
until 7b makes them false, and editing them now would be a claim the
code does not yet support — the failure mode `CLAUDE.md` exists to prevent.

So this sub-project ships `iso20022/doc.go` and nothing else, and the package is
imported by no one until 7b. That is a real cost, honestly an
unusual one for this repository. It is accepted because the alternative is
worse: designing the wire format inside the transport that carries it means the
message shape gets settled by whatever the transport found convenient, and every
"why is this field here?" answer becomes "because the mesh needed it".

The mitigation is sequencing, not argument: 7b follows immediately,
and if it does not, this package is a branch that was never merged rather than
dead code in `main`.

## Failure modes

- **The package is merged and 7b is not.** Dead code in `main`.
  Mitigated by holding the merge until 2's plan is written, not by hoping.
- **The EPC samples are wrong or misremembered.** The golden files are the
  entire basis for "these messages are real", so a bad sample propagates
  silently into everything downstream. The optional `xmllint` test is the
  cross-check, and the samples' provenance goes in a `testdata/README.md`.
- **`encoding/xml` namespace handling disappoints.** It is known-awkward for
  emitting prefixed namespaces. The design avoids the awkward case by using
  default namespaces per element (`xmlns=` on `AppHdr` and on `Document`), which
  is valid and is what the samples use. If it fails anyway, the fallback is a
  hand-written marshaller for the envelope only — the leaf structs are
  unaffected. This is the one place the plan should keep a spike step.
- **The BIC decision reopens more than intended.** Adding `Participant.BIC` in
  7b touches the participant store rows in both backends and the
  `storetest` conformance suite. It is small, but it is not zero, and it is
  7b's cost rather than this one's — recorded here so it is not a
  surprise there.
- **The reason-code table drifts from `payment/errors.go`.** A new sentinel
  added later gets no code and falls through to `MS03`. 7b's
  translator should make the mapping exhaustive over a list the tests can hold,
  so a new error is a compile-or-test failure rather than a silent `MS03`.

## What 7b inherits

Stated so its spec starts from a contract rather than from a reading of this
package:

- `Marshal(Envelope) ([]byte, error)` and `Unmarshal([]byte) (Envelope, error)`,
  with `Envelope.Document` a typed value dispatched on `AppHdr.MsgDefIdr`.
- `BIC` and `IBAN` types to validate `Participant.BIC` and
  `deposit.Identifier` values against.
- The reason-code constants and the mapping table above.
- The four messages, with `pacs.002`'s `GrpSts`/`TxSts` split ready for a
  partially-rejected bulk.
- Two questions left open on purpose: `MndtRltdInf.DtOfSgntr` has no source in
  `payment.Mandate`, and `Payment.RejectReason string` should become a
  `StatusReason` plus free text.
