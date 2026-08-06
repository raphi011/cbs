// Package iso20022 implements the ISO 20022 messages this system exchanges: the
// documents a bank, a clearing house and a settlement agent actually send each
// other, rather than a Go struct standing in for one.
//
// That sentence used to say "the SEPA interbank messages", and it stopped being
// true. SEPA is a scheme, and the account-management messages a bank's admission
// depends on carry no payment between banks and were built here from the ISO
// schema alone, with no European Payments Council guideline consulted for any
// part of the family. Which messages the scheme does profile has a section of
// its own below; the reversal is recorded after the message list.
//
// It imports nothing from the rest of this repository — not ledger, not
// deposit, not payment. That is deliberate and load-bearing: the package's
// claim is that these are the STANDARD's types, and an import of ledger.Amount
// would quietly make that false, because the next reader could no longer tell
// which fields came from ISO 20022 and which came from here. The cost is one
// conversion boundary, and sub-project 7b paid it: payment/translate.go
// imports this package, and nothing here imports payment. The boundary belongs
// on the payment side rather than here — a translator that lived in this
// package would be the same import, only pointing the other way. The rule is a
// test rather than a convention: imports_test.go fails on a repository import
// in any non-test file.
//
// # The standard and the scheme's profile of it
//
// Base ISO 20022 permits a great deal that SEPA forbids. The European Payments
// Council's Implementation Guidelines are a constrained SUBSET of the standard:
// accounts are identified by IBAN and nothing else, the currency is euro, and
// the charge bearer is always SLEV — "Only 'SLEV' is allowed" (SCT Inter-PSP IG
// idx 2.28, SDD Core IG idx 2.26).
//
// This package implements the EPC subset for the messages the EPC profiles, and
// the relationship is itself worth knowing: the standard is a superset, and a
// scheme narrows it until only one thing can be meant. Which messages those are
// is the next section, and it is a shorter list than this one implies.
//
// Some narrowings are THIS PACKAGE's and not the scheme's, and the difference is
// worth keeping visible, because a claim about the standard travels further than
// a claim about the code — the README, the hint content and the quiz all copy
// from here. The ones that touch a message the EPC profiles are:
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
//   - Organisation identification. AnyBIC is minOccurs="0" wherever
//     OrganisationIdentification29 appears, and this package requires it. On
//     pacs.002 that is a status naming its originator, which is EPC-mandatory
//     as an element but not as a BIC; on the acmt family, which no scheme
//     profiles, it is the applicant's only identifier. One Go type makes both
//     refusals, so the narrowing spans the profiled subset and the rest alike.
//     See OrganisationIdentification.
//
// Three of the claims above carry their index into the Implementation
// Guidelines — the charge bearer, the settlement method and remittance
// information — so the next reader can check those against the clause rather
// than against a 400-page PDF. Two of the three were false before a review
// fetched the guidelines, which is the whole argument for citing an index.
//
// The IBAN-only and euro-only claims carry none. Nobody has looked them up
// against the primary document, and saying "every claim here is cited" while
// two of five are not would be the same kind of overclaim the citations exist
// to prevent. They are the outstanding debt, recorded as such.
//
// # Which messages the scheme actually profiles
//
// The customer messages: pacs.008, pacs.003, pacs.002 and pacs.004. Everything
// in the section above is about those, and nothing in it is about the rest.
//
// The rest follow the ISO schema alone. testdata/README.md already records why
// for pacs.009 — the EPC governs SEPA Credit Transfer and SEPA Direct Debit
// between PSPs and their customers, not a clearing house's settlement
// instruction to a central bank — and the same holds for camt.053 and for the
// three acmt messages: no Implementation Guideline was consulted for any of
// them, and their shapes come from the XSD and from nothing else. That is a
// statement about what was read, not a claim that the EPC is silent, and the
// distinction is the same one the outstanding debt above is recorded for.
//
// So a fact taken out of this package needs its kind attached. "The charge
// bearer is always SLEV" is the scheme narrowing the standard. "An acmt.007
// names its applicant by AnyBIC" is the standard. "An acmt.007 without that BIC
// is refused here" is neither — it is this package narrowing the standard on its
// own account. Each narrowing of that third kind is recorded on the type that
// makes it, because on these messages there is no rule book to point at: see
// AccountOwner, PostalAddress, AccountRequestAcknowledgement and
// OrganisationIdentification. The last of those also refuses inside the profiled
// subset, on pacs.002, which is why it is listed above as well as here.
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
// Most are the interbank counterpart of an operation the payment package
// already performs — an instruction, a collection, its status, its return, the
// settlement leg that discharges a cycle. Another reports on an account after
// the fact. The account-management family carries the settlement-account
// request and its answer: NOT a bank's admission to the scheme, which is
// contractual and travels on no message at all, but the account without which
// an admitted bank could not settle. See Acmt007:
//
//   - pacs.008.001.08 FIToFICstmrCdtTrf — a SEPA Credit Transfer.
//   - pacs.003.001.08 FIToFICstmrDrctDbt — a SEPA Direct Debit collection.
//   - pacs.002.001.10 FIToFIPmtStsRpt — a status report. This is the message
//     that makes clearing asynchronous: a bank sends an instruction and learns
//     its fate later, in a separate document.
//   - pacs.004.001.09 PmtRtr — a return, the R-transaction.
//   - pacs.009.001.08 FICdtTrf — a financial institution credit transfer, in
//     which both parties are banks: the settlement instruction a clearing
//     house sends its settlement agent when a cycle closes. The other four are
//     sub-project 7a's; this one is 7b's, which is the sub-project that made
//     the central bank an actor with something to receive.
//   - camt.053.001.08 BkToCstmrStmt — a statement: an account servicer telling
//     an account holder what happened on an account the holder does not keep.
//     The central bank sends one to each member after a cut-off, for that
//     member's reserve account. It is sub-project 8's, and it is the message
//     that reverses the first ruling below.
//   - acmt.007.001.03 AcctOpngReq — an account-opening request: a bank asking a
//     settlement agent for the settlement account its admission depends on.
//   - acmt.010.001.03 AcctReqAck — the agent's acknowledgement, naming the
//     accounts it opened. It is the message the clearing house's routing entry
//     is written from, by an institution that neither sent it nor was addressed
//     on it.
//   - acmt.011.001.03 AcctReqRjctn — the agent's refusal, and the same
//     conversation ending the other way.
//
// The three acmt messages are one conversation and are read as one. Acmt007
// carries the family's documentation, including why this use of eBAM is not how
// a central-bank account is really opened, and they are the messages that
// reverse the second ruling below. They have callers now — payment/translate.go
// builds and reads all three, and mesh carries them between the joining bank,
// the clearing house and the settlement agent — which the first version of that
// documentation, written a sub-task before those callers, said they did not.
//
// Deliberately absent: pain.001 and pain.008 (the customer-to-bank layer),
// camt.056 recalls and pacs.007 reversals, message signing, and runtime XSD
// validation. Each is recorded in the design document with the reason. The rest
// of the acmt family goes with them, for the reason UseCase gives: this system
// opens a settlement account at admission and never afterwards maintains,
// closes or inspects one.
//
// # A reversed ruling: the camt family
//
// The whole camt family was recorded here as deliberately absent, and the reason
// was true when it was written: no institution in this system needed to be TOLD
// about a movement on an account it does not hold, because every actor could
// read every book. Sub-project 8 creates the first institution that cannot — a
// member bank whose reserve at the central bank moves in the CENTRAL BANK's
// book — so the movement has to arrive as a message or not at all. camt.053 is
// carried; the rest of the family is not, and camt.054 is refused on the
// specific ground that a notification carries no balance and therefore cannot
// detect a wrong posting. See Camt053.
//
// # A reversed ruling: "the SEPA interbank messages"
//
// This file's first sentence used to say the package implements the SEPA
// interbank messages of the ISO 20022 standard, and the acmt family reversed it.
// What travels on that family is not a payment between banks, and no EPC
// Implementation Guideline was consulted for any part of it.
//
// The sub-project's design document puts that second half flatly — "the EPC
// profiles no part of it" — and this file states the weaker thing it can source,
// for the reason "Which messages the scheme actually profiles" gives: nobody
// here has verified a negative about a body of guidelines nobody here has read
// in full. Either wording carries the same consequence. The framing this package
// is built on, that the standard is a superset and a scheme narrows it, has
// nothing to say about the messages admission adds, and leaving the old sentence
// would have implied a scheme behind them that nothing here can point at. That
// section above is what replaces it, and it is what to read before quoting
// anything in this package as a fact about SEPA.
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
