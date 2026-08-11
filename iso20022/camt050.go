package iso20022

import (
	"encoding/xml"
	"fmt"
)

const camt050Namespace = "urn:iso:std:iso:20022:tech:xsd:camt.050.001.05"

func init() {
	registerDocument("camt.050.001.05", camt050Namespace, func() Document { return &Camt050{} })
}

// Camt050 is LiquidityCreditTransfer: an account holder telling the institution
// that keeps its account to move money INTO one of its accounts there.
//
// Here it is a member bank asking its central bank to credit the bank's reserve
// account — a lodgement. The bank has the cash; what it wants is a claim on the
// central bank instead.
//
// # Why a lodgement is a conversation and a deposit is not
//
// A customer paying cash in at a branch is ONE institution's act. The bank takes
// the notes and writes two lines in its own book: its vault cash rises, and what
// it owes that customer rises with it. Nobody else's book moves, nobody has to
// agree, and there is nothing to tell anyone — which is why Network.DepositTx
// posts in one book and sends no message at all.
//
// Moving that cash onto reserve is a DIFFERENT act and a different pair of
// institutions. The bank cannot write in the central bank's book, so it cannot
// credit its own reserve account: only the account servicer can, and the way one
// institution asks another to post is a message. So the two legs land in two
// books, in two units of work, with a document between them — and that is not a
// mechanism this system adds for the sake of realism, it is the only shape
// available once each institution keeps its own book.
//
// A bank posting in the central bank's ledger inside the bank's own unit of work
// is what splitting the two avoids. This message is the half of the lodgement
// that travels.
//
// # This one is the real thing
//
// camt.050 is exactly what a TARGET2 or CLM participant sends to move liquidity
// between its accounts, and a liquidity transfer into an RTGS account is one of
// its ordinary uses — so nothing here is a stand-in for a message a real network
// would not carry. What this system leaves out is the rest of the family:
// camt.051 pulls liquidity the other way, and the reservation and standing-order
// messages manage it. This system lodges cash and never afterwards reserves,
// schedules or withdraws it.
//
// # What the schema said, checked rather than recalled
//
// LiquidityCreditTransferV05 has three children: MsgHdr and LqdtyCdtTrf are
// mandatory and SplmtryData is not. The nesting of LqdtyCdtTrf inside
// LqdtyCdtTrf is the schema's own and not a mistake here — the message's root
// element and its one payload element have the same name.
//
// Inside LiquidityCreditTransfer2 exactly ONE child is mandatory: TrfdAmt.
// Everything else — the transfer's own identifier, both parties, both accounts
// and the settlement date — is minOccurs="0". This package requires four of them
// anyway and the narrowing is recorded on LiquidityTransfer, because an
// instruction naming no account to credit is one no servicer can act on.
//
// Deliberately omitted, and legal in the standard: SplmtryData, LqdtyTrfId's
// InstrId, TxId and UETR (EndToEndId is the one reference this system quotes back
// on the receipt), Dbtr/DbtrAcct's branch and CashAccount38's Tp, Ccy, Nm and
// Prxy. Each is absent rather than empty.
type Camt050 struct {
	XMLName     xml.Name                  `xml:"urn:iso:std:iso:20022:tech:xsd:camt.050.001.05 Document"`
	LqdtyCdtTrf LiquidityCreditTransferV5 `xml:"LqdtyCdtTrf"`
}

func (Camt050) MessageDefinitionIdentifier() string { return "camt.050.001.05" }
func (Camt050) namespace() string                   { return camt050Namespace }

func (d Camt050) validate() error { return d.LqdtyCdtTrf.validate() }

// LiquidityCreditTransferV5 is the mandatory children of
// LiquidityCreditTransferV05: a header and one transfer.
//
// ONE transfer and not a list. LqdtyCdtTrf is maxOccurs="1" here, which is the
// opposite of every payment message in this package — a pacs.008 carries a batch
// — and it is why nothing in this file counts transactions or checks a claimed
// count against an arrived one. A lodgement is one movement between two accounts
// the sender holds, and the schema says so.
type LiquidityCreditTransferV5 struct {
	MsgHdr      MessageHeader     `xml:"MsgHdr"`
	LqdtyCdtTrf LiquidityTransfer `xml:"LqdtyCdtTrf"`
}

func (m LiquidityCreditTransferV5) validate() error {
	if err := m.MsgHdr.validate(); err != nil {
		return err
	}
	return m.LqdtyCdtTrf.validate()
}

// MessageHeader is MessageHeader1: what identifies this message and when it was
// made.
//
// CreDtTm is minOccurs="0" in MessageHeader1 and this package requires it. That
// narrowing is this system's: every other message here carries a creation
// instant, the receipt quotes this message back by identifier alone, and a
// document that cannot be placed in time is one nothing can order against the
// postings it caused.
type MessageHeader struct {
	MsgId   string      `xml:"MsgId"`
	CreDtTm ISODateTime `xml:"CreDtTm"`
}

func (h MessageHeader) validate() error {
	if h.MsgId == "" {
		return fmt.Errorf("%w: MsgHdr/MsgId", ErrMissingElement)
	}
	if h.CreDtTm.IsZero() {
		return fmt.Errorf("%w: MsgHdr/CreDtTm", ErrMissingElement)
	}
	return nil
}

// LiquidityTransfer is LiquidityCreditTransfer2: which accounts, whose, and how
// much.
//
// The field order is the schema's sequence order and must not be changed.
//
// # Four narrowings, and the schema requires none of them
//
// TrfdAmt is the schema's only mandatory child. This package additionally
// requires the creditor, the creditor's account, the debtor and the transfer
// identifier, and each has its own reason:
//
//   - CdtrAcct is the account to be credited, and it is the whole instruction.
//     An account servicer handed a transfer with no account to credit has
//     nothing to post to and no way to guess: this system's central bank keeps a
//     reserve account per member per asset, so even knowing the sender does not
//     narrow it to one. See payment.ReadLodgement, which refuses it.
//   - Cdtr is the institution that keeps the account. It is the message's own
//     statement of who is being asked, and the header's To says the same thing —
//     which is exactly why it is checked: the two disagreeing is a message that
//     was addressed to one servicer and asks about another's book.
//   - Dbtr is the member lodging the cash, and it is what the servicer keys its
//     own member row by. The header's Fr carries it too, and the same comparison
//     applies for the same reason.
//   - LqdtyTrfId/EndToEndId is the sender's own reference, and the receipt quotes
//     it back. Without it a bank that sent two lodgements cannot tell which one
//     a camt.025 answers.
//
// DbtrAcct is NOT required, and the asymmetry is the point rather than an
// oversight. The account being debited is the bank's own vault cash, in the
// bank's own book, which the central bank neither keeps nor may see. Naming it
// would be telling the servicer the number of an account in a ledger it has no
// access to — the same reason a pacs.008 does not carry the payer's internal GL
// account. This system leaves it absent.
//
// SttlmDt is absent too. A lodgement here is executed when it arrives, so a
// requested settlement date would be a claim about scheduling this system does
// not model.
type LiquidityTransfer struct {
	LqdtyTrfId LiquidityTransferIdentification `xml:"LqdtyTrfId"`
	Cdtr       BranchAndFinancialInstitution   `xml:"Cdtr"`
	CdtrAcct   CashAccount                     `xml:"CdtrAcct"`
	TrfdAmt    TransferredAmount               `xml:"TrfdAmt"`
	Dbtr       BranchAndFinancialInstitution   `xml:"Dbtr"`
}

func (t LiquidityTransfer) validate() error {
	if err := t.LqdtyTrfId.validate(); err != nil {
		return fmt.Errorf("LqdtyTrfId: %w", err)
	}
	if err := t.Cdtr.validate(); err != nil {
		return fmt.Errorf("Cdtr: %w", err)
	}
	if err := t.CdtrAcct.validate(); err != nil {
		return fmt.Errorf("CdtrAcct: %w", err)
	}
	if err := t.TrfdAmt.validate(); err != nil {
		return fmt.Errorf("TrfdAmt: %w", err)
	}
	if err := t.Dbtr.validate(); err != nil {
		return fmt.Errorf("Dbtr: %w", err)
	}
	return nil
}

// LiquidityTransferIdentification is PaymentIdentification8, of which one child
// is mandatory and carried: the sender's end-to-end reference.
//
// It is NOT PaymentIdentification, which pacs.008 and pacs.009 share:
// PaymentIdentification7 makes InstrId optional beside a mandatory EndToEndId
// and this type's schema adds TxId and UETR. Carrying only EndToEndId means the
// two Go types would emit identically today, and they are still separate,
// because the elements each is ALLOWED to grow into differ — a UETR on a
// pacs.008 is invalid and a UETR here is not.
type LiquidityTransferIdentification struct {
	EndToEndId string `xml:"EndToEndId"`
}

func (i LiquidityTransferIdentification) validate() error {
	if i.EndToEndId == "" {
		return fmt.Errorf("%w: EndToEndId", ErrMissingElement)
	}
	return nil
}

// TransferredAmount is Amount2Choice, and only the currency-bearing arm is
// carried.
//
// The standard offers AmtWthtCcy — an ImpliedCurrencyAndAmount, a bare number
// whose currency the reader is expected to know — or AmtWthCcy, which states it.
// This package carries the second and refuses to model the first, which is a
// narrowing on this system's own account rather than a scheme's.
//
// The reason is that the implied arm is only safe where exactly one currency is
// possible, and here it never is: the central bank keeps a reserve account per
// member PER ASSET, so a lodgement's amount with no currency on it would have to
// be resolved against the account it names, and a mismatch between the two would
// be unstateable. ledger.LookupAsset is what turns the code into a scale on the
// way in, so the currency is load-bearing at the domain boundary too. See
// payment.ReadLodgement.
//
// This is BalanceTypeChoice's shape rather than AccountIdentification4Choice's:
// one arm carried, so there is nothing for a validate to choose between and
// presence is the whole check.
type TransferredAmount struct {
	AmtWthCcy ActiveCurrencyAndAmount `xml:"AmtWthCcy"`
}

func (a TransferredAmount) validate() error { return a.AmtWthCcy.Validate() }
