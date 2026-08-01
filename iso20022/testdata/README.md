# Sample documents

Each file here is a complete `Envelope`: a `head.001.001.02` business
application header and one ISO 20022 message.

**Provenance.** These are hand-constructed to the element lists and code values
of the EPC Implementation Guidelines. They are *not* copies of the EPC's own
sample files, which are distributed as PDFs. That matters, because these files
are the entire basis for the claim that this package emits real messages — a
mistake here propagates silently into everything downstream. They match those
element lists in every respect but one, identified deliberately: `pacs002.xml`
carries no `TxInfAndSts/OrgnlTxRef`, which the EPC guidelines make mandatory —
see `PaymentTransactionStatus` for why it is elided. The cross-check is
`TestGoldenFilesValidateAgainstTheSchema` in `xmllint_test.go`, which validates
them against the official XSDs when `xmllint` and the schemas are present.

**To enable the schema check**, download the message schemas from
<https://www.iso20022.org/iso-20022-message-definitions> into `testdata/xsd/`
using the file names the test expects:

    testdata/xsd/head.001.001.02.xsd
    testdata/xsd/pacs.008.001.08.xsd
    testdata/xsd/pacs.003.001.08.xsd
    testdata/xsd/pacs.002.001.10.xsd
    testdata/xsd/pacs.004.001.09.xsd

The directory is not committed: the schemas are redistributed under ISO's terms
and are not this repository's to vendor. The test skips when they are absent.

**Identities.** Every file uses the same cast, matching `seed/seed.go`:

| Entity | BIC | IBAN |
| --- | --- | --- |
| Aurora Bank | `AURTSESSXXX` | `SE89AURORA1001` (Alice Andersson) |
| Banca Verde | `VERDITMMXXX` | `IT60VERDE2002` (Bella Bruno) |
| Clearing house | `CSMBFRPPXXX` | — |

The IBANs are the seed's `SE89-AURORA-1001` and `IT60-VERDE-2002` in compact
form. They carry no valid mod-97 check digit, on purpose — see `IBAN`.
