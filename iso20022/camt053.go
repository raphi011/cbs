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
type StatementEntry struct {
	Amt         ActiveCurrencyAndAmount `xml:"Amt"`
	CdtDbtInd   CreditDebitCode         `xml:"CdtDbtInd"`
	Sts         EntryStatusChoice       `xml:"Sts"`
	BookgDt     DateAndDateTime         `xml:"BookgDt"`
	ValDt       DateAndDateTime         `xml:"ValDt"`
	AcctSvcrRef string                  `xml:"AcctSvcrRef,omitempty"`

	// BkTxCd is what KIND of movement this is, and it is the one element of
	// ReportEntry10 the schema makes mandatory — every other child of an entry is
	// minOccurs="0". This struct did not have it at all.
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
