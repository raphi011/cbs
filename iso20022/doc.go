// Package iso20022 implements the SEPA interbank messages of the ISO 20022
// standard: the documents that two banks and a clearing house actually
// exchange, rather than a Go struct standing in for one.
//
// It imports nothing from the rest of this repository — not ledger, not
// deposit, not payment. That is deliberate and load-bearing: the package's
// claim is that these are the STANDARD's types, and an import of ledger.Amount
// would quietly make that false, because the next reader could no longer tell
// which fields came from ISO 20022 and which came from here. The cost is one
// conversion boundary, and sub-project 7b is what pays it: nothing imports this
// package yet, so the translator does not exist. It belongs on the payment
// side rather than here — a translator that lived in this package would be the
// same import, only pointing the other way.
//
// # The standard and the scheme's profile of it
//
// Base ISO 20022 permits a great deal that SEPA forbids. The European Payments
// Council's Implementation Guidelines are a constrained SUBSET of the standard:
// accounts are identified by IBAN and nothing else, the currency is euro, and
// the charge bearer is always SLEV — "Only 'SLEV' is allowed" (SCT Inter-PSP IG
// idx 2.28, SDD Core IG idx 2.26).
//
// This package implements the EPC subset. The relationship is itself worth
// knowing: the standard is a superset, and a scheme narrows it until only one
// thing can be meant.
//
// Two further narrowings are THIS PACKAGE's and not the scheme's, and the
// difference is worth keeping visible, because a claim about the standard
// travels further than a claim about the code — the README, the hint content
// and the quiz all copy from here:
//
//   - Settlement method. The guidelines allow CLRG, INGA and INDA for a credit
//     transfer (SCT Inter-PSP IG idx 1.9) and restrict a direct debit not at
//     all (SDD Core IG idx 1.10). This package declares CLRG alone, because
//     that is the only method this system produces, and checks SttlmMtd for
//     presence rather than for value. See SettlementInstruction.
//   - Remittance information. Either the structured or the unstructured arm may
//     be present (SCT Inter-PSP IG idx 2.137, SDD Core IG idx 2.156), and the
//     2025 SCT IG adds an extended option. This package models the unstructured
//     arm only, which the scheme does limit to one occurrence of Max140Text.
//     See RemittanceInformation.
//
// Every claim above about what the guidelines require carries its index into
// the Implementation Guidelines, so the next reader can check it against the
// clause rather than against a 400-page PDF. Two of these claims were false
// before a review fetched the guidelines; a citation is what makes the third
// one cheap to falsify.
//
// # The envelope
//
// There is no single "ISO 20022 envelope". What is standard is the Business
// Application Header, head.001.001.02 — Fr, To, BizMsgIdr, MsgDefIdr, CreDt —
// which carries the routing identity and says which message definition the
// document follows. What is NOT standard is how a header and a document are
// packaged together for a particular network: SWIFT CBPR+ wraps them one way,
// EBA CLEARING STEP2 files another, and each clearing house's file and bulk
// framing is its own.
//
// So the outer <Envelope> element here is this repository's stand-in for a
// clearing-house-specific wrapper. The two elements inside it are the standard.
// A reader who later meets a real STEP2 file finds the difference explained
// rather than surprising.
//
// # Messages
//
// Four, each the interbank counterpart of an operation the payment package
// already performs:
//
//   - pacs.008.001.08 FIToFICstmrCdtTrf — a SEPA Credit Transfer.
//   - pacs.003.001.08 FIToFICstmrDrctDbt — a SEPA Direct Debit collection.
//   - pacs.002.001.10 FIToFIPmtStsRpt — a status report. This is the message
//     that makes clearing asynchronous: a bank sends an instruction and learns
//     its fate later, in a separate document.
//   - pacs.004.001.09 PmtRtr — a return, the R-transaction.
//
// Deliberately absent: pain.001 and pain.008 (the customer-to-bank layer), the
// camt reporting family, camt.056 recalls and pacs.007 reversals, message
// signing, and runtime XSD validation. Each is recorded in the design document
// with the reason.
//
// # Two things encoding/xml cannot do
//
// The struct shapes here are dictated by the standard everywhere except two
// places, and both are worth knowing before reading them.
//
// First, omitempty does not suppress an empty STRUCT. So every optional
// composite element is a POINTER. A non-pointer optional field would emit
// <RmtInf></RmtInf> into every message, which is not merely untidy: the schema
// makes RmtInf's child mandatory when RmtInf is present, so an empty one is
// invalid.
//
// Second, encoding/xml cannot express xsd:choice. AccountIdentification4Choice
// (IBAN or Othr, never both) and StatusReason6Choice (Cd or Prtry) are
// therefore structs of pointers with a validate method enforcing exactly-one.
// Marshal validates the whole tree before emitting, so an invalid choice is a
// Go error rather than a document a counterparty rejects.
//
// # Element order
//
// XML sequence order is part of the schema. Struct field order determines the
// order elements are emitted in, so the fields of every message type are in the
// standard's order and must not be reordered.
package iso20022
