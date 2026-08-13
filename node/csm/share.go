package csm

import (
	"strconv"

	"github.com/raphi011/cbs/iso20022"
)

// creditTransferPart and directDebitPart are one destination's share of an
// uploaded file: the transactions at the given positions, under the sender's
// own group header.
func creditTransferPart(doc *iso20022.Pacs008, idx []int) iso20022.Document {
	if len(idx) == len(doc.FIToFICstmrCdtTrf.CdtTrfTxInf) {
		return doc
	}
	out := *doc
	out.FIToFICstmrCdtTrf.CdtTrfTxInf = pick(doc.FIToFICstmrCdtTrf.CdtTrfTxInf, idx)
	out.FIToFICstmrCdtTrf.GrpHdr.NbOfTxs = strconv.Itoa(len(idx))
	return &out
}

func directDebitPart(doc *iso20022.Pacs003, idx []int) iso20022.Document {
	if len(idx) == len(doc.FIToFICstmrDrctDbt.DrctDbtTxInf) {
		return doc
	}
	out := *doc
	out.FIToFICstmrDrctDbt.DrctDbtTxInf = pick(doc.FIToFICstmrDrctDbt.DrctDbtTxInf, idx)
	out.FIToFICstmrDrctDbt.GrpHdr.NbOfTxs = strconv.Itoa(len(idx))
	return &out
}

func pick[T any](all []T, idx []int) []T {
	out := make([]T, 0, len(idx))
	for _, i := range idx {
		out = append(out, all[i])
	}
	return out
}
