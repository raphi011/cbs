# Sample documents

Each file here is a complete `Envelope`: a `head.001.001.02` business
application header and one ISO 20022 message.

**Provenance.** These are hand-constructed to the element lists and code values
of the EPC Implementation Guidelines. They are *not* copies of the EPC's own
sample files, which are distributed as PDFs. That matters, because these files
are the entire basis for the claim that this package emits real messages — a
mistake here propagates silently into everything downstream. They match those
element lists in every respect but one, identified deliberately: neither
`pacs002.xml` nor `pacs004.xml` carries `OrgnlTxRef`, which the EPC guidelines
make mandatory on both — see `PaymentTransactionStatus` for why it is elided.
The cross-check is
`TestGoldenFilesValidateAgainstTheSchema` in `xmllint_test.go`, which validates
them against the official XSDs when `xmllint` and the schemas are present.

`pacs009.xml` is different: it is this repository's own construction, not an
EPC sample. The EPC Implementation Guidelines cover the four customer
messages above; they say nothing about the FI-to-FI settlement leg, because
EPC governs SEPA Credit Transfer and SEPA Direct Debit between PSPs and their
customers, not a clearing house's own settlement instruction to a central
bank. `pacs009.xml`'s shape therefore follows the ISO 20022 schema directly —
`pacs.009.001.08`'s `FIToFIFinancialInstitutionCreditTransfer` — rather than
an implementation guideline, and the cross-check against `pacs.009.001.08.xsd`
is the same `TestGoldenFilesValidateAgainstTheSchema` when that schema is
present.

`camt050.xml` and `camt025.xml` are a fourth file of the same kind, and one
conversation rather than two documents: Aurora Bank asking its central bank to
move EUR 500,000.00 of vault cash onto its reserve account, and the central bank
saying it did. Their shapes come from `camt.050.001.05.xsd` and
`camt.025.001.05.xsd` and from nothing else, for the reason `pacs009.xml` gives —
the EPC governs SEPA credit transfers and direct debits between PSPs and their
customers, not a member's liquidity transfer to its central bank.

The receipt names the request by its `MsgId` (`RctDtls/OrgnlMsgId/MsgId`),
because a lodgement is one request and one answer — there is no process id above
it, and nothing here needs one. See `Camt025` on `OriginalMessageAndIssuer`.

**One value in `camt025.xml` is unverifiable and is not a schema fact.**
`ReqHdlg/StsCd` is `Max4AlphaNumericText` with no `xs:enumeration` behind it, so
`xmllint` accepts any four characters and the `ACSC` in that file is this
system's choice rather than something the check confirms. See `RequestHandling`,
which records why the value was reused from `TransactionStatus` instead of
invented, and what a repository holding the external status code list should do
about it. It is the same unpaid debt `BankTransactionCode` carries.

**To enable the schema check**, download the message schemas from
<https://www.iso20022.org/iso-20022-message-definitions> into `testdata/xsd/`
using the file names the test expects:

    testdata/xsd/head.001.001.02.xsd
    testdata/xsd/pacs.008.001.08.xsd
    testdata/xsd/pacs.003.001.08.xsd
    testdata/xsd/pacs.002.001.10.xsd
    testdata/xsd/pacs.004.001.09.xsd
    testdata/xsd/pacs.009.001.08.xsd
    testdata/xsd/camt.053.001.08.xsd
    testdata/xsd/camt.050.001.05.xsd
    testdata/xsd/camt.025.001.05.xsd

The list above must match the `files` map in `xmllint_test.go`, and it did not:
`camt.053.001.08.xsd` was missing here from the day the statement landed, so
anybody following these instructions downloaded six schemas for seven checks.
Under `ISO20022_REQUIRE_SCHEMAS` that is a failure rather than a silent skip,
which is the whole reason that switch exists — but the instructions were still
wrong. **A message added to that map is a line added here.**

The directory is not committed: the schemas are redistributed under ISO's terms
and are not this repository's to vendor. The test skips when they are absent, and
`.gitignore` is what keeps a `git add -A` from vendoring them anyway.

Not every one of them is on that page. The versions this repository pins are in
several cases no longer the current ones, and a superseded version lives in the
**archive** instead — <https://www.iso20022.org/catalogue-messages/iso-20022-messages-archive>,
which takes a `?search=` query. `camt.050.001.05` and `camt.025.001.05` are both
there, under Cash Management V01, and each row has its own `XSD` button. Search
for the identifier, expand the message set and then the `camt` group, and take
the XSD from the row whose identifier matches EXACTLY — the neighbouring rows are
other messages of the same family, and `camt.051.001.05` is one row below the one
this repository wants.

**They are not scriptable to fetch.** `iso20022.org`'s catalogue pages do not
answer a non-browser client, and the schema downloads sit behind an acceptance
of ISO's terms — which is the point of them rather than an obstacle to route
around. Download them in a browser.

**What happened the first time this check actually ran (2026-08-05).** It failed,
and not on the golden file: `camt053.xml` was rejected on two counts, both of
them in `camt053.go` and therefore in **every camt.053 this system had ever
emitted**. `AddtlNtryInf` was in the wrong position — it is the last element of
`ReportEntry10`, and the struct emitted it six elements early, under a comment
saying the field order was the schema's — and `BkTxCd`, the one child of an entry
the schema makes mandatory, was missing entirely. Both shipped with Task 15 and
survived a per-task review, a documentation sweep and a whole-branch review with
probes.

Nothing in the repository could have caught either. That is the argument for
this file, made by the thing it warns about: `ISO20022_REQUIRE_SCHEMAS` exists so
that a skip becomes a failure, and until somebody downloaded the schemas there
was nothing for it to be required against.

**A skip is not a pass.** Once you have the schemas, run the check as a
*required* one:

    make test-schemas

which sets `ISO20022_REQUIRE_SCHEMAS=1`. With that variable set to any
non-empty value, a missing `xmllint` or a missing schema is a failure rather
than a skip — so a machine or a CI job that has the schemas notices when they
go away, instead of quietly reporting `PASS` for ten checks that never ran.

**Identities.** The four customer-message files (`pacs008.xml`, `pacs003.xml`,
`pacs002.xml`, `pacs004.xml`) use the same cast, matching `seed/seed.go`:

| Entity | BIC | IBAN |
| --- | --- | --- |
| Aurora Bank | `AURTSESSXXX` | `SE0888100000000000000001` (Alice Andersson) |
| Banca Verde | `VERDITMMXXX` | `IT78K8881200000000000000002` (Bella Bruno) |
| Clearing house | `CSMBFRPPXXX` | — |

The names are the seed's; the addresses are these files' own, under bank codes
allocated to nobody, and each agrees with its bank's country. They are real
IBANs — correct length, correct national structure, mod-97 and Italy's CIN both
computed — so a reader can check one by hand. That is a property of the
fixtures and not of the codec: `IBAN.Validate` is the schema's pattern and
passes a value whose check digits are wrong, which is what lets a receiver read
a mistyped address before refusing it.

`pacs009.xml` uses a different, narrower cast, because it is not a customer
message: both parties are financial institutions, and one of them is the
central bank sub-project 7a introduced as an actor with nothing it could
receive until this message existed.

| Entity | BIC |
| --- | --- |
| Clearing house (as `Fr` / instructing agent) | `CSMXFRPPXXX` |
| Central bank (as `To` / instructed agent) | `CBSEDEFFXXX` |
| Aurora Bank (as `Dbtr`, the debiting settlement member) | `AURODEFFXXX` |
| Banca Verde (as `Cdtr`, the crediting settlement member) | `VERDITMMXXX` |

`CSMXFRPPXXX` and `AURODEFFXXX` are deliberately not `CSMBFRPPXXX` and
`AURTSESSXXX`: they are this system's settlement-layer identities, distinct from
the customer-facing BICs above, and are what the clearing house and Aurora Bank
are called in their settlement-layer roles. Banca Verde's BIC is the same in both
roles — `VERDITMMXXX` — because it needs no separate settlement identity.
