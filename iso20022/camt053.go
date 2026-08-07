package iso20022

import (
	"encoding/xml"
	"fmt"
)

const camt053Namespace = "urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"

func init() {
	registerDocument("camt.053.001.08", camt053Namespace, func() Document { return &Camt053{} })
}

// Camt053 is BankToCustomerStatement: an account servicer telling an account
// holder what happened on an account the holder does not keep.
//
// # Why the camt family is in scope
//
// No institution in this system needs to be TOLD about a movement on an account
// it can read. A member bank's reserve at the central bank is the exception: it
// moves in the CENTRAL BANK's book, which the member may not read, so the
// movement has to arrive as a message or not at all.
//
// # Why a statement and not a notification
//
// A camt.054 carries entries and no balance. It can drive a posting and it can
// never detect a wrong one. A camt.053 carries Ntry — what to book — and
// Bal/CLBD — whether you booked it right, which is the check a member's reserve
// mirror needs and the only in-system detector of a mis-booked position. Nostro
// reconciliation in the field is balance-anchored for exactly this reason. One
// message family covers both jobs, so this package carries one.
//
// # "Customer" here is a bank
//
// The message definition's Cstmr is the account HOLDER, whoever that is. On this
// wire the servicer is the central bank and the holder is a member bank, which
// is the same relationship a retail bank has with a depositor one layer down.
// Nothing in the message says the holder is a person, and the type does not
// pretend otherwise.
//
// Deliberately omitted, and legal in the standard: MsgPgntn (this system sends
// one page), Stmt/ElctrncSeqNb and LglSeqNb (there is no statement series),
// Stmt/FrToDt (a settlement statement covers one cycle, named on the entry, not
// a date range), TxsSummry, Ntry/NtryDtls and every charge, interest and
// related-party element. Each is absent rather than empty.
type Camt053 struct {
	XMLName       xml.Name                `xml:"urn:iso:std:iso:20022:tech:xsd:camt.053.001.08 Document"`
	BkToCstmrStmt BankToCustomerStatement `xml:"BkToCstmrStmt"`
}

func (Camt053) MessageDefinitionIdentifier() string { return "camt.053.001.08" }
func (Camt053) namespace() string                   { return camt053Namespace }

func (d Camt053) validate() error { return d.BkToCstmrStmt.validate() }

// BankToCustomerStatement is a group header and one or more statements.
type BankToCustomerStatement struct {
	GrpHdr StatementGroupHeader `xml:"GrpHdr"`
	Stmt   []AccountStatement   `xml:"Stmt"`
}

func (m BankToCustomerStatement) validate() error {
	if err := m.GrpHdr.validate(); err != nil {
		return err
	}
	if len(m.Stmt) == 0 {
		return fmt.Errorf("%w: Stmt", ErrMissingElement)
	}
	for i := range m.Stmt {
		if err := m.Stmt[i].validate(); err != nil {
			return fmt.Errorf("Stmt[%d]: %w", i, err)
		}
	}
	return nil
}

// StatementGroupHeader is a message identifier and a creation instant.
//
// It is NOT CreditTransferGroupHeader, which pacs.008 and pacs.009 share: that
// type carries NbOfTxs, SttlmInf and IntrBkSttlmDt, none of which a statement
// has. A shared struct here would emit three elements the schema does not allow
// in a camt.053.
type StatementGroupHeader struct {
	MsgId   string      `xml:"MsgId"`
	CreDtTm ISODateTime `xml:"CreDtTm"`
}

func (h StatementGroupHeader) validate() error {
	if h.MsgId == "" {
		return fmt.Errorf("%w: GrpHdr/MsgId", ErrMissingElement)
	}
	if h.CreDtTm.IsZero() {
		return fmt.Errorf("%w: GrpHdr/CreDtTm", ErrMissingElement)
	}
	return nil
}

// AccountStatement is one account's statement: which account, what its balance
// is, and what moved.
//
// The field order is the schema's sequence order and must not be changed.
//
// Bal is validated as NON-EMPTY, which the schema also requires (1..n) and which
// is the element this message is chosen for. See the type doc on Camt053.
type AccountStatement struct {
	Id      string           `xml:"Id"`
	CreDtTm ISODateTime      `xml:"CreDtTm"`
	Acct    CashAccount      `xml:"Acct"`
	Bal     []CashBalance    `xml:"Bal"`
	Ntry    []StatementEntry `xml:"Ntry"`
}

func (s AccountStatement) validate() error {
	if s.Id == "" {
		return fmt.Errorf("%w: Stmt/Id", ErrMissingElement)
	}
	if s.CreDtTm.IsZero() {
		return fmt.Errorf("%w: Stmt/CreDtTm", ErrMissingElement)
	}
	if err := s.Acct.validate(); err != nil {
		return fmt.Errorf("Acct: %w", err)
	}
	if len(s.Bal) == 0 {
		return fmt.Errorf("%w: Stmt/Bal", ErrMissingElement)
	}
	for i := range s.Bal {
		if err := s.Bal[i].validate(); err != nil {
			return fmt.Errorf("Bal[%d]: %w", i, err)
		}
	}
	for i := range s.Ntry {
		if err := s.Ntry[i].validate(); err != nil {
			return fmt.Errorf("Ntry[%d]: %w", i, err)
		}
	}
	return nil
}

// CashBalance is one balance on the account, and which balance it is.
type CashBalance struct {
	Tp        BalanceTypeChoice       `xml:"Tp"`
	Amt       ActiveCurrencyAndAmount `xml:"Amt"`
	CdtDbtInd CreditDebitCode         `xml:"CdtDbtInd"`
	Dt        DateAndDateTime         `xml:"Dt"`
}

func (b CashBalance) validate() error {
	if b.Tp.CdOrPrtry.Cd == "" {
		return fmt.Errorf("%w: Bal/Tp/CdOrPrtry/Cd", ErrMissingElement)
	}
	if err := b.Amt.Validate(); err != nil {
		return fmt.Errorf("Bal/Amt: %w", err)
	}
	if b.CdtDbtInd == "" {
		return fmt.Errorf("%w: Bal/CdtDbtInd", ErrMissingElement)
	}
	return b.Dt.validate()
}

// BalanceTypeChoice names which balance this is. The standard offers a code or a
// proprietary identifier; only the code arm is carried, as ServiceLevelChoice
// does, because every balance this system reports is in the external code set.
type BalanceTypeChoice struct {
	CdOrPrtry BalanceTypeCode `xml:"CdOrPrtry"`
}

// BalanceTypeCode is the extra element of nesting the standard puts between Tp
// and the code. It exists so the XML comes out right and for no other reason.
type BalanceTypeCode struct {
	Cd BalanceType `xml:"Cd"`
}

// StatementEntry is one movement on the account.
//
// AcctSvcrRef is the SERVICER's reference for the entry, and here it is the
// CLEARING CYCLE the movement discharged. That is what makes the statement
// actionable: a member bank has no cycles of its own — it never sees a batch —
// so the only way it can tell which cut-off a reserve movement belongs to is for
// the central bank to say. See payment.ReadStatement.
//
// The field order is the schema's sequence order and must not be changed. That
// sentence was here before anything checked it, and it was wrong: AddtlNtryInf
// is the LAST element of ReportEntry10, after NtryDtls, and this struct emitted
// it immediately after AcctSvcrRef. Every camt.053 this system produced was
// invalid on that alone, from the day the message landed until the day somebody
// downloaded the schemas. See BkTxCd below for the second half of the same
// story, and testdata/README.md for why neither was caught.
type StatementEntry struct {
	Amt         ActiveCurrencyAndAmount `xml:"Amt"`
	CdtDbtInd   CreditDebitCode         `xml:"CdtDbtInd"`
	Sts         EntryStatusChoice       `xml:"Sts"`
	BookgDt     DateAndDateTime         `xml:"BookgDt"`
	ValDt       DateAndDateTime         `xml:"ValDt"`
	AcctSvcrRef string                  `xml:"AcctSvcrRef,omitempty"`

	// BkTxCd is what KIND of movement this is, and it is the one element of
	// ReportEntry10 the schema makes mandatory — every other child of an entry
	// is minOccurs="0". This struct did not have it at all.
	//
	// A reconciling account holder needs it for a reason the rest of the entry
	// does not cover: Amt and CdtDbtInd say how much moved and which way, and
	// nothing else says whether this was a settlement, a fee, an interest
	// posting or a correction. A bank that books every entry the same way is
	// reconciling an amount rather than a movement.
	BkTxCd BankTransactionCode `xml:"BkTxCd"`

	AddtlNtryInf string `xml:"AddtlNtryInf,omitempty"`
}

func (e StatementEntry) validate() error {
	if err := e.Amt.Validate(); err != nil {
		return fmt.Errorf("Ntry/Amt: %w", err)
	}
	if e.CdtDbtInd == "" {
		return fmt.Errorf("%w: Ntry/CdtDbtInd", ErrMissingElement)
	}
	if e.Sts.Cd == "" {
		return fmt.Errorf("%w: Ntry/Sts/Cd", ErrMissingElement)
	}
	if err := e.BookgDt.validate(); err != nil {
		return fmt.Errorf("Ntry/BookgDt: %w", err)
	}
	if err := e.ValDt.validate(); err != nil {
		return fmt.Errorf("Ntry/ValDt: %w", err)
	}
	return e.BkTxCd.validate()
}

// BankTransactionCode says what kind of movement an entry is. The standard
// offers two arms and makes both optional, so one of them satisfies the schema
// and the choice between them is a claim about what this repository knows.
//
// # Why the proprietary arm, and not the domain code
//
// Domn takes ExternalBankTransactionDomain1Code — PMNT, RCDT, ESCT and the rest
// — which is an EXTERNAL code list. It is published as its own spreadsheet and
// it is not in camt.053.001.08.xsd, so nothing available to this repository can
// check a value written into it: xmllint would accept any four characters, and
// a wrong code would look exactly like a right one for ever.
//
// This package already carries an unpaid debt of that shape — see the package
// doc on the IBAN-only and euro-only claims, which are recorded as uncited
// precisely because nobody looked them up. Adding a third by guessing a domain
// code would be worse, because unlike those two it would be a guess made AFTER
// the file that says guessing is how the other two went wrong.
//
// So the proprietary arm, whose Cd is Max35Text and whose meaning is the
// issuer's. Issr names this system as that issuer, which is what stops the
// value being mistaken for a standard one. A repository that obtains the
// external code list should revisit this; the reason recorded here is
// availability, not principle.
type BankTransactionCode struct {
	Prtry ProprietaryBankTransactionCode `xml:"Prtry"`
}

func (c BankTransactionCode) validate() error {
	if c.Prtry.Cd == "" {
		return fmt.Errorf("%w: Ntry/BkTxCd/Prtry/Cd", ErrMissingElement)
	}
	return nil
}

// ProprietaryBankTransactionCode is a code the issuer defines and the issuer
// that defined it.
type ProprietaryBankTransactionCode struct {
	Cd   string `xml:"Cd"`
	Issr string `xml:"Issr,omitempty"`
}

// The one proprietary transaction code this system issues, and its issuer.
//
// ONE code, because every entry this system states is the same kind of
// movement: central-bank reserves discharging an interbank obligation. A
// cut-off produces one and a return produces one, and StatementMessage
// deliberately does not tell a member which — the reference is opaque to the
// bank reading it either way, so a code that distinguished them would be
// telling that bank something it has no row to resolve. If this system ever
// states a fee, an interest posting or a correction, that is when a second code
// is earned.
const (
	BankTransactionCodeSettlement = "SETTLEMENT"
	BankTransactionCodeIssuer     = "CBS"
)

// EntryStatusChoice is booked or pending. Only the code arm is carried, for
// BalanceTypeChoice's reason.
type EntryStatusChoice struct {
	Cd EntryStatus `xml:"Cd"`
}

// DateAndDateTime is a date OR a date-time, never both and never neither.
//
// This is an xsd:choice, which encoding/xml cannot express, so both arms are
// pointers and validate enforces exactly-one — the same shape
// AccountIdentification4Choice has and for the same reason. Only the date arm is
// produced here: a settlement's booking and value dates are days, and a
// date-time would assert a precision the cut-off does not have.
type DateAndDateTime struct {
	Dt   *ISODate     `xml:"Dt,omitempty"`
	DtTm *ISODateTime `xml:"DtTm,omitempty"`
}

func (d DateAndDateTime) validate() error {
	switch {
	case d.Dt != nil && d.DtTm != nil:
		return fmt.Errorf("%w: DateAndDateTime has both Dt and DtTm", ErrInvalidChoice)
	case d.Dt != nil, d.DtTm != nil:
		return nil
	default:
		return fmt.Errorf("%w: DateAndDateTime has neither Dt nor DtTm", ErrInvalidChoice)
	}
}
