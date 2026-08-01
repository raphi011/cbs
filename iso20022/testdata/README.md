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

**To enable the schema check**, download the message schemas from
<https://www.iso20022.org/iso-20022-message-definitions> into `testdata/xsd/`
using the file names the test expects:

    testdata/xsd/head.001.001.02.xsd
    testdata/xsd/pacs.008.001.08.xsd
    testdata/xsd/pacs.003.001.08.xsd
    testdata/xsd/pacs.002.001.10.xsd
    testdata/xsd/pacs.004.001.09.xsd
    testdata/xsd/pacs.009.001.08.xsd

The directory is not committed: the schemas are redistributed under ISO's terms
and are not this repository's to vendor. The test skips when they are absent.

**A skip is not a pass.** Once you have the schemas, run the check as a
*required* one:

    make test-schemas

which sets `ISO20022_REQUIRE_SCHEMAS=1`. With that variable set to any
non-empty value, a missing `xmllint` or a missing schema is a failure rather
than a skip — so a machine or a CI job that has the schemas notices when they
go away, instead of quietly reporting `PASS` for ten checks that never ran.

**Identities.** Every file uses the same cast, matching `seed/seed.go`:

| Entity | BIC | IBAN |
| --- | --- | --- |
| Aurora Bank | `AURTSESSXXX` | `SE89AURORA1001` (Alice Andersson) |
| Banca Verde | `VERDITMMXXX` | `IT60VERDE2002` (Bella Bruno) |
| Clearing house | `CSMBFRPPXXX` | — |

The IBANs are the seed's `SE89-AURORA-1001` and `IT60-VERDE-2002` in compact
form. They carry no valid mod-97 check digit, on purpose — see `IBAN`.
