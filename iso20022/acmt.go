package iso20022

import (
	"encoding/xml"
	"fmt"
)

// The three namespaces of the account-management family this package carries.
// Each is the target namespace of its own schema, read from the XSD rather than
// recalled — the version suffix is part of the namespace, so a wrong one is a
// document no counterparty can dispatch.
const (
	acmt007Namespace = "urn:iso:std:iso:20022:tech:xsd:acmt.007.001.03"
	acmt010Namespace = "urn:iso:std:iso:20022:tech:xsd:acmt.010.001.03"
	acmt011Namespace = "urn:iso:std:iso:20022:tech:xsd:acmt.011.001.03"
)

func init() {
	registerDocument("acmt.007.001.03", acmt007Namespace, func() Document { return &Acmt007{} })
	registerDocument("acmt.010.001.03", acmt010Namespace, func() Document { return &Acmt010{} })
	registerDocument("acmt.011.001.03", acmt011Namespace, func() Document { return &Acmt011{} })
}

// UseCase says which account-management operation a message is about. It is
// UseCases1Code, and it appears on the acknowledgement and on the rejection and
// not on the request: an AcctOpngReq says what it is in its own element name,
// and an AcctReqAck does not say what it is acknowledging. Both schemas make it
// mandatory.
//
// This type is NOT in codes.go, and the difference is worth knowing. The code
// sets there are EXTERNAL lists — maintained outside the schema, published
// separately, and unverifiable against anything this repository has, which is
// the whole argument BankTransactionCode makes for taking the proprietary arm.
// UseCases1Code is an xs:enumeration INSIDE acmt.010.001.03.xsd and
// acmt.011.001.03.xsd, so a wrong value here is caught by the schema check
// rather than by nobody.
type UseCase string

// UseCaseAccountOpening (OPEN) is the only member declared, following codes.go's
// rule that a code with no condition behind it is a constant nobody uses.
//
// The enumeration has three more — MNTN (maintenance), CLSG (closing) and VIEW
// (a request to see an account's details). This system opens a settlement
// account when a bank is admitted and never afterwards changes, closes or
// inspects one, so none of the three has a message that would carry it.
const UseCaseAccountOpening UseCase = "OPEN"

// MessageIdentification is MessageIdentification1: an identifier and the instant
// it was created. Both children are mandatory in all three schemas.
//
// The same schema type carries MsgId, PrcId and RjctdReqId, and each means
// something different — see AccountRequestReferences on MsgId versus PrcId. One
// Go type serves all of them because one schema type does; the meaning is in the
// element that holds it, which is why validate is told the path.
type MessageIdentification struct {
	Id      string      `xml:"Id"`
	CreDtTm ISODateTime `xml:"CreDtTm"`
}

// validate takes the element path because the same type sits under MsgId, PrcId
// and RjctdReqId, and an error saying "Id" would not say which.
func (m MessageIdentification) validate(element string) error {
	if m.Id == "" {
		return fmt.Errorf("%w: %s/Id", ErrMissingElement, element)
	}
	if m.CreDtTm.IsZero() {
		return fmt.Errorf("%w: %s/CreDtTm", ErrMissingElement, element)
	}
	return nil
}

// PostalAddress is PostalAddress24, of which only the country is carried.
//
// The element exists here because Organisation33 makes LglAdr mandatory, not
// because this system knows where a bank is. Every child of PostalAddress24 is
// optional, so an empty <LglAdr/> would satisfy the schema — and this package
// refuses one anyway, which is a narrowing nobody but this package requires. The
// reason is that the alternative is an element asserting nothing: a legal
// address is the address at which an institution can be served, and a bank whose
// legal address is blank has not given one. The country is the part of it this
// system does hold, because Organisation33 makes CtryOfOpr mandatory too.
//
// The standard's street, town, postcode and seven address lines are absent
// rather than empty. This system knows a bank's BIC and its name and nothing
// about where its offices are.
type PostalAddress struct {
	Ctry string `xml:"Ctry"`
}

func (a PostalAddress) validate(element string) error {
	if a.Ctry == "" {
		return fmt.Errorf("%w: %s/Ctry", ErrMissingElement, element)
	}
	return nil
}

// Acmt007 is AccountOpeningRequest: an account holder asking an account servicer
// to open it an account. Here it is a bank asking a settlement agent for the
// settlement account its admission to a scheme depends on.
//
// # This file is the documents; the conversation is elsewhere
//
// It carries the three documents and their validation, and no caller. Who
// composes the request, who relays it and who writes what down when the answer
// comes back are the mesh and payment layers' — payment/translate.go builds and
// reads all three, and mesh carries them between the joining bank, the clearing
// house and the settlement agent.
//
// Where the docs below say what a reader does with an element they describe
// code, and the code is the authority on them. What is checked HERE is what the
// tests in acmt_test.go and the xmllint run in xmllint_test.go check, which is
// the documents' shape and nothing about who acts on them.
//
// # This is not how the real thing works
//
// acmt is eBAM — electronic Bank Account Management — and it is designed for a
// CORPORATE opening an account at its bank. A bank's RTGS account at its central
// bank is not created this way. It is reference data: in TARGET it is CRDM
// static data and reda messages, set up by the central bank's own operations
// rather than requested over a payment network.
//
// This system models admission as a conversation because the SEQUENCE and the
// OWNERSHIP are what it teaches — who may open which account, and in whose book
// — and this family is the closest true one rather than the actual one. A reader
// who meets CRDM later should find the difference recorded here rather than
// discover it.
//
// # Scheme membership travels on no message at all
//
// Joining a payment scheme is contractual: an adherence agreement, signed. There
// is no ISO 20022 message for it and this package does not invent one. What
// travels here is the settlement-account request, and the clearing house's
// routing entry falls out of the acknowledgement — which is why the clearing
// house writes that row from a message it did not originate and is not addressed
// on.
//
// # What the schema said that the plan for this file did not
//
// The children of AccountOpeningRequestV03 with no minOccurs attribute, and
// therefore mandatory, are Refs, Acct, AcctSvcrId and Org. The field order below
// is the schema's sequence order, in which Acct comes BEFORE the servicer and
// the owner. Neither the order nor the completeness of that list is asserted by
// this comment: TestGoldenFilesValidateAgainstTheSchema runs xmllint over
// testdata/acmt007.xml against acmt.007.001.03.xsd.
//
// Deliberately omitted, and legal in the standard: Fr (the header already says
// who sent it), CtrctDts and UndrlygMstrAgrmt (there is no account contract to
// date or master agreement to cite — the adherence agreement above is not one of
// these), Mndt and Grp (nobody holds a mandate over a settlement account; the
// bank operates it itself), RefAcct, DgtlSgntr (message signing is out of scope
// for the whole package) and SplmtryData. Each is absent rather than empty.
type Acmt007 struct {
	XMLName     xml.Name              `xml:"urn:iso:std:iso:20022:tech:xsd:acmt.007.001.03 Document"`
	AcctOpngReq AccountOpeningRequest `xml:"AcctOpngReq"`
}

func (Acmt007) MessageDefinitionIdentifier() string { return "acmt.007.001.03" }
func (Acmt007) namespace() string                   { return acmt007Namespace }

func (d Acmt007) validate() error { return d.AcctOpngReq.validate() }

// AccountOpeningRequest is the mandatory children of AccountOpeningRequestV03,
// in the schema's sequence order.
type AccountOpeningRequest struct {
	Refs       AccountRequestReferences      `xml:"Refs"`
	Acct       RequestedAccount              `xml:"Acct"`
	AcctSvcrId BranchAndFinancialInstitution `xml:"AcctSvcrId"`
	Org        AccountOwner                  `xml:"Org"`
}

func (r AccountOpeningRequest) validate() error {
	if err := r.Refs.validate(); err != nil {
		return err
	}
	if err := r.Acct.validate(); err != nil {
		return err
	}
	if err := r.AcctSvcrId.validate(); err != nil {
		return fmt.Errorf("AcctSvcrId: %w", err)
	}
	return r.Org.validate()
}

// AccountRequestReferences is References4: what identifies this message, and
// what identifies the process it is one turn of.
//
// MsgId and PrcId are separate elements and separate facts. MsgId names this
// document; PrcId names the account-opening PROCESS, and it is the same value on
// every message of one admission — References4, References5 and References6 all
// make it mandatory. That matters more here than one request and one answer
// would need it to: because RequestedAccount holds a single currency, a bank
// operating in several assets sends several requests, and PrcId is what says
// they are one admission rather than several.
//
// It is also why this package does not carry acmt.010's optional AckdMsgId. A
// back-reference to one request's message id would answer a narrower question
// than PrcId already answers on every message in the family, and would have to
// be a list to answer even that much.
//
// This type is NOT shared with the acknowledgement or the rejection. The three
// schemas define three different reference types — References4, References5 and
// References6, with different children in different orders — so one Go type
// would emit two of the three invalidly. See AccountAcknowledgementReferences
// and AccountRejectionReferences.
//
// AttchdDocNm is the one child of References4 this type omits; there is no
// attached document.
type AccountRequestReferences struct {
	MsgId MessageIdentification `xml:"MsgId"`
	PrcId MessageIdentification `xml:"PrcId"`
}

func (r AccountRequestReferences) validate() error {
	if err := r.MsgId.validate("Refs/MsgId"); err != nil {
		return err
	}
	return r.PrcId.validate("Refs/PrcId")
}

// RequestedAccount is CustomerAccount4, of which one child is mandatory and
// carried: the currency the account is to be denominated in.
//
// ONE currency, not a list: Ccy's maxOccurs defaults to 1. A settlement account
// holds one asset, which is the same shape payment.BankAccounts already
// has — one set of internal accounts per asset a bank operates in — so a bank
// clearing a euro and a dollar scheme sends two of these messages rather than
// one naming two currencies.
//
// The acknowledgement is the asymmetric half, and the asymmetry is the schema's
// rather than a choice made here: AccountForAction1 is unbounded there, so one
// acknowledgement can name every account that was opened, however many requests
// asked for them.
//
// Everything else CustomerAccount4 offers — a proposed identifier, a name, a
// status, a type, figures about expected turnover and notification thresholds,
// statement frequencies and restrictions — is optional and absent. The
// applicant proposing its own account identifier would be the strangest of
// them: the servicer's book is the servicer's, and the identifier comes back in
// the acknowledgement.
type RequestedAccount struct {
	Ccy string `xml:"Ccy"`
}

func (a RequestedAccount) validate() error {
	if a.Ccy == "" {
		return fmt.Errorf("%w: Acct/Ccy", ErrMissingElement)
	}
	return nil
}

// AccountOwner is Organisation33: the institution asking for the account.
//
// Every field here is mandatory in Organisation33, and all but one is here only
// because of that. FullLglNm, CtryOfOpr and LglAdr describe a corporate
// applicant, which is what eBAM was written for and what this use of it is not.
//
// Two of those three are read after all, and the reasons are different. An
// account servicer names an account after the member it opens it for, so
// payment.ReadAdmissionRequest takes FullLglNm and the settlement agent's own
// member row keeps it — which makes this the only element on the family that
// delivers a legal name anywhere, since the acknowledgement carries none (see
// AccountRequestAcknowledgement).
//
// CtryOfOpr says which national registry the applicant is applying to, and it is
// the whole of what a request says about the bank code it is asking for: an
// applicant proposes no code, because a code is the registry's to allocate and
// the acknowledgement is what carries one back. A bank operating in Germany is
// given a Bankleitzahl and one operating in Italy an ABI, and nothing but this
// element says which.
//
// LglAdr is read by nothing. It is the address at which an institution can be
// served, and this system knows a bank's BIC and its name and nothing about
// where its offices are — which is also why the two country elements can
// legitimately disagree: a bank passported into another market operates there
// and is established here.
//
// OrgId/AnyBIC is the load-bearing one, and the standard leaves it OPTIONAL:
// AnyBIC is minOccurs="0" in OrganisationIdentification29. This package requires
// it, and the requirement is this system's rather than the standard's. The
// reason is that this message is carried to a settlement agent that holds no
// roster: the BIC is what it keys its member record by, what the clearing house
// keys its routing entry by, and what the acknowledgement is addressed back on.
// See OrganisationIdentification, whose validate is where the refusal lives.
type AccountOwner struct {
	FullLglNm string                     `xml:"FullLglNm"`
	CtryOfOpr string                     `xml:"CtryOfOpr"`
	LglAdr    PostalAddress              `xml:"LglAdr"`
	OrgId     OrganisationIdentification `xml:"OrgId"`
}

func (o AccountOwner) validate() error {
	if o.FullLglNm == "" {
		return fmt.Errorf("%w: Org/FullLglNm", ErrMissingElement)
	}
	if o.CtryOfOpr == "" {
		return fmt.Errorf("%w: Org/CtryOfOpr", ErrMissingElement)
	}
	if err := o.LglAdr.validate("Org/LglAdr"); err != nil {
		return err
	}
	if err := o.OrgId.validate(); err != nil {
		return fmt.Errorf("Org: %w", err)
	}
	return nil
}

// Acmt010 is AccountRequestAcknowledgement: the account servicer saying it did
// what was asked, and naming what it opened.
//
// It is shaped for two readers who want different things out of it. The clearing
// house takes the routing entry — the bank is reachable from that moment — and
// the bank takes the settlement references it will quote for the rest of its
// life at this agent. Neither is the sender, and neither had the information
// before the message arrived. Both readers exist: payment's
// ReadAdmissionAcknowledgement is what they share, and mesh's csm and bank
// handlers are what take the two different things out of it.
//
// # What the schema said that the plan for this file did not
//
// The mandatory children of AccountRequestAcknowledgementV03 are Refs, OrgId and
// AcctSvcrId. AcctId — the accounts — is minOccurs="0", which this package
// narrows; see AccountRequestAcknowledgement. The owner is named by OrgId, an
// OrganisationIdentification29, and NOT by an Organisation33 the way the request
// names it: the acknowledgement carries no legal name, no country and no
// address, because the servicer is answering an applicant it has just read all
// of that from.
//
// Deliberately omitted, and legal in the standard: Fr, Refs/AckdMsgId (see
// AccountRequestReferences on why PrcId is the correlator), Refs/Sts,
// Refs/AttchdDocNm, DgtlSgntr and SplmtryData.
type Acmt010 struct {
	XMLName    xml.Name                      `xml:"urn:iso:std:iso:20022:tech:xsd:acmt.010.001.03 Document"`
	AcctReqAck AccountRequestAcknowledgement `xml:"AcctReqAck"`
}

func (Acmt010) MessageDefinitionIdentifier() string { return "acmt.010.001.03" }
func (Acmt010) namespace() string                   { return acmt010Namespace }

func (d Acmt010) validate() error { return d.AcctReqAck.validate() }

// AccountRequestAcknowledgement is the children of
// AccountRequestAcknowledgementV03 this package carries, in the schema's
// sequence order — in which the accounts come first, then the owner, then the
// servicer.
//
// That order is not the rejection's, and the two must not be read as the same
// shape: AccountRequestRejectionV03 sequences AcctSvcrId, then AcctId, then
// OrgId. Nothing in Go says so; testdata/acmt010.xml and testdata/acmt011.xml
// under TestGoldenFilesValidateAgainstTheSchema are what check it.
//
// validate refuses an acknowledgement with no account, which the schema permits.
// That narrowing is this system's: the clearing house writes its routing entry —
// including which assets the bank clears — from this list, so an acknowledgement
// naming no account would admit a bank to nothing.
//
// It refuses one carrying anything but EXACTLY ONE OrgId/Othr, for the same kind
// of reason and a sharper one. This is the message a bank code travels on: the
// servicer allocates it, the clearing house publishes it, and the applicant mints
// its customers' addresses under it, and none of the three has another source.
// None at all admits a bank whose customers nobody can address; two would leave
// three readers to choose between them, and a bank holds one allocation because
// a code is one country's to give. See GenericOrganisationIdentification.
type AccountRequestAcknowledgement struct {
	Refs       AccountAcknowledgementReferences `xml:"Refs"`
	AcctId     []OpenedAccount                  `xml:"AcctId"`
	OrgId      OrganisationIdentification       `xml:"OrgId"`
	AcctSvcrId BranchAndFinancialInstitution    `xml:"AcctSvcrId"`
}

func (a AccountRequestAcknowledgement) validate() error {
	if err := a.Refs.validate(); err != nil {
		return err
	}
	if len(a.AcctId) == 0 {
		return fmt.Errorf("%w: AcctId", ErrMissingElement)
	}
	for i := range a.AcctId {
		if err := a.AcctId[i].validate(); err != nil {
			return fmt.Errorf("AcctId[%d]: %w", i, err)
		}
	}
	if err := a.OrgId.validate(); err != nil {
		return err
	}
	switch len(a.OrgId.Othr) {
	case 1:
	case 0:
		return fmt.Errorf("%w: OrgId/Othr", ErrMissingElement)
	default:
		return fmt.Errorf("iso20022: OrgId/Othr names %d allocations and an admission carries one",
			len(a.OrgId.Othr))
	}
	if err := a.AcctSvcrId.validate(); err != nil {
		return fmt.Errorf("AcctSvcrId: %w", err)
	}
	return nil
}

// AccountAcknowledgementReferences is References5. It is not
// AccountRequestReferences: ReqTp comes first and is mandatory, because an
// acknowledgement's element name says it acknowledges something without saying
// what.
//
// AckdMsgId, Sts and AttchdDocNm are the optional children left out.
type AccountAcknowledgementReferences struct {
	ReqTp UseCase               `xml:"ReqTp"`
	MsgId MessageIdentification `xml:"MsgId"`
	PrcId MessageIdentification `xml:"PrcId"`
}

func (r AccountAcknowledgementReferences) validate() error {
	if r.ReqTp == "" {
		return fmt.Errorf("%w: Refs/ReqTp", ErrMissingElement)
	}
	if err := r.MsgId.validate("Refs/MsgId"); err != nil {
		return err
	}
	return r.PrcId.validate("Refs/PrcId")
}

// OpenedAccount is AccountForAction1: one account the servicer opened, and the
// currency that says which one it is. Both children are mandatory.
//
// The identifier is an AccountIdentification4Choice and not a string, which is
// the same choice camt.053's Acct carries and takes the same arm: a reserve
// account at a central bank has no IBAN, because it is not a payment address and
// no customer ever quotes it. See GenericAccountIdentification.
type OpenedAccount struct {
	Id  AccountIdentification4Choice `xml:"Id"`
	Ccy string                       `xml:"Ccy"`
}

func (a OpenedAccount) validate() error {
	if err := a.Id.validate(); err != nil {
		return fmt.Errorf("Id: %w", err)
	}
	if a.Ccy == "" {
		return fmt.Errorf("%w: Ccy", ErrMissingElement)
	}
	return nil
}

// Acmt011 is AccountRequestRejection: the account servicer refusing.
//
// It ends the same conversation the other way, and it carries no account,
// because none was opened. The applicant stays whatever it was before it asked.
//
// # What the schema said that the plan for this file did not
//
// The reason is not a block of its own alongside Refs. It is RjctnRsn, a child
// OF References6, and it is Max350Text repeated one-to-many rather than a code
// with an optional free-text note. So there is no narrowing to record here at
// all: the standard itself makes an account-management rejection free prose,
// which is the opposite of what it does for a payment rejection, where
// StatusReason is a real external code set and this package uses it as one.
// That contrast is the reason this type documents it.
//
// Deliberately omitted, and legal in the standard: Fr, AcctId (an account the
// servicer refused to open has no identifier to name), Refs/AttchdDocNm,
// DgtlSgntr and SplmtryData.
type Acmt011 struct {
	XMLName      xml.Name                `xml:"urn:iso:std:iso:20022:tech:xsd:acmt.011.001.03 Document"`
	AcctReqRjctn AccountRequestRejection `xml:"AcctReqRjctn"`
}

func (Acmt011) MessageDefinitionIdentifier() string { return "acmt.011.001.03" }
func (Acmt011) namespace() string                   { return acmt011Namespace }

func (d Acmt011) validate() error { return d.AcctReqRjctn.validate() }

// AccountRequestRejection is the mandatory children of
// AccountRequestRejectionV03, in the schema's sequence order — in which the
// servicer comes before the owner. The acknowledgement's order is the other way
// round; see AccountRequestAcknowledgement.
type AccountRequestRejection struct {
	Refs       AccountRejectionReferences    `xml:"Refs"`
	AcctSvcrId BranchAndFinancialInstitution `xml:"AcctSvcrId"`
	OrgId      OrganisationIdentification    `xml:"OrgId"`
}

func (r AccountRequestRejection) validate() error {
	if err := r.Refs.validate(); err != nil {
		return err
	}
	if err := r.AcctSvcrId.validate(); err != nil {
		return fmt.Errorf("AcctSvcrId: %w", err)
	}
	return r.OrgId.validate()
}

// AccountRejectionReferences is References6, which carries the refusal itself.
//
// RjctdReqId is the back-reference the acknowledgement does not have: on a
// rejection the schema makes naming the refused request mandatory. PrcId still
// correlates the conversation — see AccountRequestReferences — so the two say
// different things, and the rejection says both.
//
// RjctnRsn is validated as present and non-blank. Present because References6
// makes it minOccurs="1"; non-blank because its type is Max350Text, whose
// minLength is 1, so an empty <RjctnRsn/> is a document xmllint rejects. Between
// them that is also the domain rule this system wants — a rejection that says
// nothing is a rejection nobody can act on — but the rule is the standard's
// here, not this package's.
//
// AttchdDocNm is the optional child left out.
type AccountRejectionReferences struct {
	RjctdReqTp UseCase               `xml:"RjctdReqTp"`
	RjctnRsn   []string              `xml:"RjctnRsn"`
	RjctdReqId MessageIdentification `xml:"RjctdReqId"`
	MsgId      MessageIdentification `xml:"MsgId"`
	PrcId      MessageIdentification `xml:"PrcId"`
}

func (r AccountRejectionReferences) validate() error {
	if r.RjctdReqTp == "" {
		return fmt.Errorf("%w: Refs/RjctdReqTp", ErrMissingElement)
	}
	if len(r.RjctnRsn) == 0 {
		return fmt.Errorf("%w: Refs/RjctnRsn", ErrMissingElement)
	}
	for i, reason := range r.RjctnRsn {
		if reason == "" {
			return fmt.Errorf("%w: Refs/RjctnRsn[%d]", ErrMissingElement, i)
		}
	}
	if err := r.RjctdReqId.validate("Refs/RjctdReqId"); err != nil {
		return err
	}
	if err := r.MsgId.validate("Refs/MsgId"); err != nil {
		return err
	}
	return r.PrcId.validate("Refs/PrcId")
}
