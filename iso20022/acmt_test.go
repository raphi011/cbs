package iso20022

import (
	"errors"
	"strings"
	"testing"
)

// The three samples are one admission, told in three messages. Aurora Bank asks
// its settlement agent for accounts in EUR, USD and CHF — three acmt.007s, one
// per currency, because RequestedAccount holds one — and gets two of them. All
// three messages carry the same PrcId, which is what says they are one
// conversation; each carries its own MsgId, which is what says they are three
// messages.
//
// sample007 is the first of those requests, on the first leg of the relay: the
// bank to the clearing house. The document itself names the settlement agent as
// the account servicer, which is why the relay needs no state — the second leg
// replaces the header and forwards the same document.
func sample007() Envelope {
	return Envelope{
		AppHdr: AppHdr{
			Fr:        NewAgent("AURODEFFXXX"),
			To:        NewAgent("CSMXFRPPXXX"),
			BizMsgIdr: "AURODEFFXXX-ADMIT-1",
			MsgDefIdr: "acmt.007.001.03",
			CreDt:     ISODateTime{testTime},
		},
		Document: &Acmt007{AcctOpngReq: AccountOpeningRequest{
			Refs: AccountRequestReferences{
				MsgId: MessageIdentification{Id: "AURODEFFXXX-ADMIT-1", CreDtTm: ISODateTime{testTime}},
				PrcId: MessageIdentification{Id: "AURODEFFXXX-ADMIT", CreDtTm: ISODateTime{testTime}},
			},
			Acct:       RequestedAccount{Ccy: "EUR"},
			AcctSvcrId: BranchAndFinancialInstitution{FinInstnId: FinancialInstitutionIdentification{BICFI: "CBSEDEFFXXX"}},
			Org: AccountOwner{
				FullLglNm: "Aurora Bank",
				CtryOfOpr: "DE",
				LglAdr:    PostalAddress{Ctry: "DE"},
				OrgId:     OrganisationIdentification{AnyBIC: "AURODEFFXXX"},
			},
		}},
	}
}

// sample010 is the settlement agent's acknowledgement: the two accounts it
// opened, each named with the currency that says which one it is.
func sample010() Envelope {
	return Envelope{
		AppHdr: AppHdr{
			Fr:        NewAgent("CBSEDEFFXXX"),
			To:        NewAgent("CSMXFRPPXXX"),
			BizMsgIdr: "CBSEDEFFXXX-ADMIT-1",
			MsgDefIdr: "acmt.010.001.03",
			CreDt:     ISODateTime{testTime},
		},
		Document: &Acmt010{AcctReqAck: AccountRequestAcknowledgement{
			Refs: AccountAcknowledgementReferences{
				ReqTp: UseCaseAccountOpening,
				MsgId: MessageIdentification{Id: "CBSEDEFFXXX-ADMIT-1", CreDtTm: ISODateTime{testTime}},
				PrcId: MessageIdentification{Id: "AURODEFFXXX-ADMIT", CreDtTm: ISODateTime{testTime}},
			},
			AcctId: []OpenedAccount{
				{Id: AccountIdentification4Choice{Othr: &GenericAccountIdentification{Id: "acc_cb_reserve_bank_1_eur"}}, Ccy: "EUR"},
				{Id: AccountIdentification4Choice{Othr: &GenericAccountIdentification{Id: "acc_cb_reserve_bank_1_usd"}}, Ccy: "USD"},
			},
			OrgId: OrganisationIdentification{
				AnyBIC: "AURODEFFXXX",
				Othr: []GenericOrganisationIdentification{{
					Id:      "99900001",
					SchmeNm: OrganisationIdentificationScheme{Prtry: "DEBLZ"},
					Issr:    "CBSEDEFFXXX",
				}},
			},
			AcctSvcrId: BranchAndFinancialInstitution{FinInstnId: FinancialInstitutionIdentification{BICFI: "CBSEDEFFXXX"}},
		}},
	}
}

// sample011 is the settlement agent refusing the third request, for an asset it
// does not operate in. The same conversation, one of its turns ending the other
// way.
func sample011() Envelope {
	return Envelope{
		AppHdr: AppHdr{
			Fr:        NewAgent("CBSEDEFFXXX"),
			To:        NewAgent("CSMXFRPPXXX"),
			BizMsgIdr: "CBSEDEFFXXX-ADMIT-2",
			MsgDefIdr: "acmt.011.001.03",
			CreDt:     ISODateTime{testTime},
		},
		Document: &Acmt011{AcctReqRjctn: AccountRequestRejection{
			Refs: AccountRejectionReferences{
				RjctdReqTp: UseCaseAccountOpening,
				RjctnRsn:   []string{"no settlement account is opened in CHF: this agent does not operate in that asset"},
				RjctdReqId: MessageIdentification{Id: "AURODEFFXXX-ADMIT-3", CreDtTm: ISODateTime{testTime}},
				MsgId:      MessageIdentification{Id: "CBSEDEFFXXX-ADMIT-2", CreDtTm: ISODateTime{testTime}},
				PrcId:      MessageIdentification{Id: "AURODEFFXXX-ADMIT", CreDtTm: ISODateTime{testTime}},
			},
			AcctSvcrId: BranchAndFinancialInstitution{FinInstnId: FinancialInstitutionIdentification{BICFI: "CBSEDEFFXXX"}},
			OrgId:      OrganisationIdentification{AnyBIC: "AURODEFFXXX"},
		}},
	}
}

// TestAcmt007GoldenRoundTrip pins testdata/acmt007.xml, which is what actually
// holds AccountOpeningRequest's field order to a committed sample.
//
// The order matters more here than the round trip alone can show, and this test
// does not check it against the schema: TestGoldenFilesValidateAgainstTheSchema
// does, by running xmllint over this same file. The two together are the check —
// this one says the struct still emits what the file says, that one says the
// file is what acmt.007.001.03.xsd allows.
func TestAcmt007GoldenRoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "acmt007.xml")

	doc, ok := env.Document.(*Acmt007)
	if !ok {
		t.Fatalf("Document is %T, want *Acmt007", env.Document)
	}
	req := doc.AcctOpngReq
	if got := req.Org.OrgId.AnyBIC; got != "AURODEFFXXX" {
		t.Errorf("Org/OrgId/AnyBIC = %q, want the applicant AURODEFFXXX", got)
	}
	if got := req.AcctSvcrId.FinInstnId.BICFI; got != "CBSEDEFFXXX" {
		t.Errorf("AcctSvcrId/FinInstnId/BICFI = %q, want the settlement agent CBSEDEFFXXX", got)
	}
	if got := req.Acct.Ccy; got != "EUR" {
		t.Errorf("Acct/Ccy = %q, want EUR", got)
	}
}

// TestAcmt007RoundTrips is the same shape as the other message round-trips: a
// document built in Go, marshalled, unmarshalled, and compared. It is what
// catches an element whose XML tag does not match the field it was written to.
func TestAcmt007RoundTrips(t *testing.T) {
	raw, err := Marshal(sample007())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), acmt007Namespace) {
		t.Fatalf("marshalled document does not carry its namespace:\n%s", raw)
	}
	env, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, ok := env.Document.(*Acmt007)
	if !ok {
		t.Fatalf("Unmarshal gave %T, want *Acmt007", env.Document)
	}
	if got.AcctOpngReq.Org.OrgId.AnyBIC != "AURODEFFXXX" {
		t.Errorf("account owner BIC round-tripped as %q", got.AcctOpngReq.Org.OrgId.AnyBIC)
	}
	if got.AcctOpngReq.Acct.Ccy != "EUR" {
		t.Errorf("requested currency round-tripped as %q", got.AcctOpngReq.Acct.Ccy)
	}
}

// TestAcmt007WithNoCurrencyIsRefused pins the one element without which the
// settlement agent cannot act. A request naming no currency asks for no
// account, and answering it would mean opening an arbitrary one.
//
// The schema agrees: Ccy is the only child of CustomerAccount4 with no
// minOccurs attribute, so it is the only one the standard makes mandatory.
func TestAcmt007WithNoCurrencyIsRefused(t *testing.T) {
	env := sample007()
	env.Document.(*Acmt007).AcctOpngReq.Acct.Ccy = ""
	if _, err := Marshal(env); !errors.Is(err, ErrMissingElement) {
		t.Fatalf("Marshal of a request with no currency: %v, want ErrMissingElement", err)
	}
}

// TestAcmt007WithNoOwnerBICIsRefused is the narrowing this package makes and the
// standard does not: AnyBIC is minOccurs="0" in OrganisationIdentification29,
// and this system requires it.
//
// The acknowledgement's route home and the roster entry's key are both this BIC;
// a request without it names no bank at all.
func TestAcmt007WithNoOwnerBICIsRefused(t *testing.T) {
	env := sample007()
	env.Document.(*Acmt007).AcctOpngReq.Org.OrgId.AnyBIC = ""
	if _, err := Marshal(env); !errors.Is(err, ErrMissingElement) {
		t.Fatalf("Marshal of a request with no owner BIC: %v, want ErrMissingElement", err)
	}
}

// TestAcmt007Validate covers the rest of what AccountOpeningRequestV03 makes
// mandatory. Each is checked on its own, because wiring one up and assuming the
// others follow is the gap this package's reviews have found more than once.
func TestAcmt007Validate(t *testing.T) {
	valid := func() *Acmt007 { return sample007().Document.(*Acmt007) }

	for _, tc := range []struct {
		name string
		mut  func(*AccountOpeningRequest)
	}{
		{"no message id", func(r *AccountOpeningRequest) { r.Refs.MsgId.Id = "" }},
		{"no message creation instant", func(r *AccountOpeningRequest) { r.Refs.MsgId.CreDtTm = ISODateTime{} }},
		{"no process id", func(r *AccountOpeningRequest) { r.Refs.PrcId.Id = "" }},
		{"no process creation instant", func(r *AccountOpeningRequest) { r.Refs.PrcId.CreDtTm = ISODateTime{} }},
		{"no account servicer", func(r *AccountOpeningRequest) { r.AcctSvcrId.FinInstnId.BICFI = "" }},
		{"no legal name", func(r *AccountOpeningRequest) { r.Org.FullLglNm = "" }},
		{"no country of operation", func(r *AccountOpeningRequest) { r.Org.CtryOfOpr = "" }},
		{"no legal address country", func(r *AccountOpeningRequest) { r.Org.LglAdr.Ctry = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := valid()
			tc.mut(&d.AcctOpngReq)
			if err := d.validate(); !errors.Is(err, ErrMissingElement) {
				t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
			}
		})
	}
}

// TestAcmt010GoldenRoundTrip pins testdata/acmt010.xml.
func TestAcmt010GoldenRoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "acmt010.xml")

	doc, ok := env.Document.(*Acmt010)
	if !ok {
		t.Fatalf("Document is %T, want *Acmt010", env.Document)
	}
	ack := doc.AcctReqAck
	if got := ack.OrgId.AnyBIC; got != "AURODEFFXXX" {
		t.Errorf("OrgId/AnyBIC = %q, want the admitted bank AURODEFFXXX", got)
	}
	if got := ack.Refs.ReqTp; got != UseCaseAccountOpening {
		t.Errorf("Refs/ReqTp = %q, want %q", got, UseCaseAccountOpening)
	}
}

// TestAcmt010CarriesOneAccountPerCurrency is the property the conversation
// rests on: the acknowledgement is what the CLEARING HOUSE writes its routing
// entry from and what the BANK learns its settlement references from, so an
// acknowledgement that named the accounts without their currencies would leave
// both readers guessing which is which.
func TestAcmt010CarriesOneAccountPerCurrency(t *testing.T) {
	raw, err := Marshal(sample010())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	env, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := env.Document.(*Acmt010).AcctReqAck
	if len(got.AcctId) != 2 {
		t.Fatalf("accounts round-tripped as %+v, want two", got.AcctId)
	}
	if got.AcctId[1].Ccy != "USD" {
		t.Errorf("second account's currency = %q, want USD", got.AcctId[1].Ccy)
	}
	if got.AcctId[1].Id.Othr == nil || got.AcctId[1].Id.Othr.Id != "acc_cb_reserve_bank_1_usd" {
		t.Errorf("second account's identifier = %+v, want the Othr arm carrying acc_cb_reserve_bank_1_usd", got.AcctId[1].Id)
	}
}

// TestAcmtGoldensCorrelateOnTheProcessIdentifier pins what ties the three
// messages of one admission together, in the three committed files rather than
// in the fixtures they were generated from.
//
// It is not the message identifier: each message has its own, and the
// acknowledgement carries no reference to the request's. It is PrcId, which
// References4, References5 and References6 all make mandatory — the process is
// the conversation, and the message is one turn in it. That is why this package
// carries no AckdMsgId, which the schema offers and leaves optional; a reader
// working out the correlation rule from testdata/ would otherwise have to guess
// whether the samples agreeing was intentional.
func TestAcmtGoldensCorrelateOnTheProcessIdentifier(t *testing.T) {
	req := assertGoldenRoundTrip(t, "acmt007.xml").Document.(*Acmt007).AcctOpngReq
	ack := assertGoldenRoundTrip(t, "acmt010.xml").Document.(*Acmt010).AcctReqAck
	rjctn := assertGoldenRoundTrip(t, "acmt011.xml").Document.(*Acmt011).AcctReqRjctn

	process := req.Refs.PrcId.Id
	if process == "" {
		t.Fatal("the request carries no PrcId")
	}
	if got := ack.Refs.PrcId.Id; got != process {
		t.Errorf("acknowledgement's PrcId = %q, want the request's %q", got, process)
	}
	if got := rjctn.Refs.PrcId.Id; got != process {
		t.Errorf("rejection's PrcId = %q, want the request's %q", got, process)
	}
	if ack.Refs.MsgId.Id == req.Refs.MsgId.Id {
		t.Errorf("acknowledgement reuses the request's MsgId %q; each message has its own",
			ack.Refs.MsgId.Id)
	}
	if rjctn.Refs.RjctdReqId.Id == req.Refs.MsgId.Id {
		t.Errorf("the rejection refuses the request the acknowledgement answered (%q); the samples "+
			"are one admission in which two accounts were opened and a third was refused",
			rjctn.Refs.RjctdReqId.Id)
	}
}

// TestAcmt010Validate covers what an acknowledgement must carry.
//
// "No account at all" is in the list and is this package's narrowing rather than
// the standard's: AcctId is minOccurs="0" in AccountRequestAcknowledgementV03.
// This system requires at least one, because the clearing house writes its
// routing entry — including which assets the bank clears — from this list, and
// an empty one would admit a bank to nothing.
func TestAcmt010Validate(t *testing.T) {
	valid := func() *Acmt010 { return sample010().Document.(*Acmt010) }

	for _, tc := range []struct {
		name string
		mut  func(*AccountRequestAcknowledgement)
		want error
	}{
		{"no request type", func(a *AccountRequestAcknowledgement) { a.Refs.ReqTp = "" }, ErrMissingElement},
		{"no message id", func(a *AccountRequestAcknowledgement) { a.Refs.MsgId.Id = "" }, ErrMissingElement},
		{"no process id", func(a *AccountRequestAcknowledgement) { a.Refs.PrcId.Id = "" }, ErrMissingElement},
		{"no account at all", func(a *AccountRequestAcknowledgement) { a.AcctId = nil }, ErrMissingElement},
		{"an account with no currency", func(a *AccountRequestAcknowledgement) { a.AcctId[1].Ccy = "" }, ErrMissingElement},
		{"an account identified in no way", func(a *AccountRequestAcknowledgement) {
			a.AcctId[1].Id.Othr = nil
		}, ErrInvalidChoice},
		{"no owner BIC", func(a *AccountRequestAcknowledgement) { a.OrgId.AnyBIC = "" }, ErrMissingElement},
		{"no allocation", func(a *AccountRequestAcknowledgement) { a.OrgId.Othr = nil }, ErrMissingElement},
		{"an allocation with no code", func(a *AccountRequestAcknowledgement) { a.OrgId.Othr[0].Id = "" }, ErrMissingElement},
		{"an allocation naming no register", func(a *AccountRequestAcknowledgement) {
			a.OrgId.Othr[0].SchmeNm.Prtry = ""
		}, ErrMissingElement},
		{"an allocation naming no allocator", func(a *AccountRequestAcknowledgement) {
			a.OrgId.Othr[0].Issr = ""
		}, ErrMissingElement},
		{"no account servicer", func(a *AccountRequestAcknowledgement) { a.AcctSvcrId.FinInstnId.BICFI = "" }, ErrMissingElement},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := valid()
			tc.mut(&d.AcctReqAck)
			if err := d.validate(); !errors.Is(err, tc.want) {
				t.Fatalf("validate() = %v, want it to wrap %v", err, tc.want)
			}
		})
	}
}

// TestTheAllocationTravelsOnTheAnswerAndNotOnTheRequest is the direction the
// whole admission conversation rests on: an applicant asks a register for a bank
// code and does not propose one, so Org/OrgId carries no Othr on the way out and
// exactly one on the way back.
//
// Two would be worse than none and that is the case worth having a name for. The
// clearing house publishes the code, the applicant mints its customers'
// addresses under it and the settlement agent holds the register, and a message
// naming two allocations leaves each of the three to choose — silently, and not
// necessarily the same way.
func TestTheAllocationTravelsOnTheAnswerAndNotOnTheRequest(t *testing.T) {
	req := sample007().Document.(*Acmt007)
	if got := req.AcctOpngReq.Org.OrgId.Othr; len(got) != 0 {
		t.Errorf("the request proposes %+v; an applicant holds no allocation to propose", got)
	}
	if err := req.validate(); err != nil {
		t.Errorf("validate() = %v, want a request with no allocation to be valid", err)
	}

	ack := sample010().Document.(*Acmt010)
	twice := ack.AcctReqAck.OrgId.Othr[0]
	ack.AcctReqAck.OrgId.Othr = append(ack.AcctReqAck.OrgId.Othr, twice)
	if err := ack.validate(); err == nil {
		t.Error("validate() accepted an acknowledgement naming two allocations")
	}
}

// TestAcmt011GoldenRoundTrip pins testdata/acmt011.xml.
//
// The rejection's element order is NOT the acknowledgement's — AcctSvcrId comes
// before OrgId here and after it there — so a struct shared between the two
// would emit one of them in the wrong order. That is what this file and
// TestGoldenFilesValidateAgainstTheSchema hold between them; nothing in Go's
// type system says it.
func TestAcmt011GoldenRoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "acmt011.xml")

	doc, ok := env.Document.(*Acmt011)
	if !ok {
		t.Fatalf("Document is %T, want *Acmt011", env.Document)
	}
	rjctn := doc.AcctReqRjctn
	if got := rjctn.Refs.RjctdReqId.Id; got != "AURODEFFXXX-ADMIT-3" {
		t.Errorf("Refs/RjctdReqId/Id = %q, want the refused request AURODEFFXXX-ADMIT-3", got)
	}
	if len(rjctn.Refs.RjctnRsn) != 1 || !strings.Contains(rjctn.Refs.RjctnRsn[0], "CHF") {
		t.Errorf("Refs/RjctnRsn = %q, want the one reason the sample gives", rjctn.Refs.RjctnRsn)
	}
	if got := rjctn.OrgId.AnyBIC; got != "AURODEFFXXX" {
		t.Errorf("OrgId/AnyBIC = %q, want the refused applicant AURODEFFXXX", got)
	}
}

// TestAcmt011Validate covers what a rejection must carry.
//
// A rejection that says nothing is a rejection nobody can act on — and here the
// standard agrees rather than being narrowed: RjctnRsn is minOccurs="1" and
// unbounded in References6, and its type is Max350Text, whose minLength is 1, so
// an empty <RjctnRsn/> is invalid too.
func TestAcmt011Validate(t *testing.T) {
	valid := func() *Acmt011 { return sample011().Document.(*Acmt011) }

	for _, tc := range []struct {
		name string
		mut  func(*AccountRequestRejection)
	}{
		{"no rejected request type", func(r *AccountRequestRejection) { r.Refs.RjctdReqTp = "" }},
		{"no reason at all", func(r *AccountRequestRejection) { r.Refs.RjctnRsn = nil }},
		{"an empty reason", func(r *AccountRequestRejection) { r.Refs.RjctnRsn = []string{""} }},
		{"no rejected request id", func(r *AccountRequestRejection) { r.Refs.RjctdReqId.Id = "" }},
		{"no message id", func(r *AccountRequestRejection) { r.Refs.MsgId.Id = "" }},
		{"no process id", func(r *AccountRequestRejection) { r.Refs.PrcId.Id = "" }},
		{"no account servicer", func(r *AccountRequestRejection) { r.AcctSvcrId.FinInstnId.BICFI = "" }},
		{"no owner BIC", func(r *AccountRequestRejection) { r.OrgId.AnyBIC = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := valid()
			tc.mut(&d.AcctReqRjctn)
			if err := d.validate(); !errors.Is(err, ErrMissingElement) {
				t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
			}
		})
	}
}

// TestAcmt011RoundTrips is the rejection's own build-marshal-unmarshal, which
// the golden file cannot replace: the golden pins the bytes, and this pins that
// a document assembled in Go — the way the settlement agent will assemble one —
// survives the codec.
func TestAcmt011RoundTrips(t *testing.T) {
	raw, err := Marshal(sample011())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	env, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, ok := env.Document.(*Acmt011)
	if !ok {
		t.Fatalf("Unmarshal gave %T, want *Acmt011", env.Document)
	}
	if len(got.AcctReqRjctn.Refs.RjctnRsn) != 1 {
		t.Fatalf("reasons round-tripped as %q, want one", got.AcctReqRjctn.Refs.RjctnRsn)
	}
	if got.AcctReqRjctn.AcctSvcrId.FinInstnId.BICFI != "CBSEDEFFXXX" {
		t.Errorf("account servicer round-tripped as %q", got.AcctReqRjctn.AcctSvcrId.FinInstnId.BICFI)
	}
}
