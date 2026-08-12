package ebics

import "strings"

// OrderType is an EBICS 2.x order type: three letters saying what a file is and
// which way it travels.
type OrderType string

const (
	// Uploads.
	CCT OrderType = "CCT" // a pacs.008 file: credit transfers
	CDD OrderType = "CDD" // a pacs.003 file: direct debit collections
	CRT OrderType = "CRT" // a pacs.004 file: returns
	CST OrderType = "CST" // a pacs.002 file: status reports
	CSI OrderType = "CSI" // a pacs.009 settlement instruction
	CLD OrderType = "CLD" // a camt.050 lodgement

	// Downloads.
	C53 OrderType = "C53" // camt.053 statements
	HAC OrderType = "HAC" // the acknowledgement file: what became of each order
	BTD OrderType = "BTD" // everything else waiting in the queue, in order

	// C25 is a camt.025 receipt waiting in a queue. It is a LABEL and never a
	// selector: BTD is what collects it, and asking for it by name is refused like
	// any other type this host does not offer.
	C25 OrderType = "C25"
)

// uploads is every order type a subscriber may send. A type that is in neither
// this set nor the download set is one this host does not offer, which is a
// refusal and not a crash.
var uploads = map[OrderType]bool{CCT: true, CDD: true, CRT: true, CST: true, CSI: true, CLD: true}

// downloads is every order type a subscriber may collect. HAC is here and is
// answered from the order log rather than from a queue.
var downloads = map[OrderType]bool{C53: true, HAC: true, BTD: true}

// IsUpload reports whether t is a type a subscriber sends.
func (t OrderType) IsUpload() bool { return uploads[t] }

// IsDownload reports whether t is a type a subscriber collects.
func (t OrderType) IsDownload() bool { return downloads[t] }

// OrderID identifies one order at one host: four characters, a letter and then
// three from 0-9A-Z, which is the protocol's own format.
type OrderID string

// orderIDAlphabet is the protocol's: digits then letters, so A000 is the first
// id a host mints and ids sort in the order they were allocated.
const orderIDAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// orderIDSpace is how many ids the format holds: 26 leading letters against
// three base-36 characters.
const orderIDSpace = 26 * 36 * 36 * 36

// mintOrderID is the nth order id a host allocates, counting from zero.
func mintOrderID(n int) OrderID {
	n %= orderIDSpace

	var b [4]byte
	b[0] = 'A' + byte(n/(36*36*36))
	rest := n % (36 * 36 * 36)
	for i := 3; i >= 1; i-- {
		b[i] = orderIDAlphabet[rest%36]
		rest /= 36
	}
	return OrderID(b[:])
}

// OrderStatus is what the host has made of an uploaded order, and the three
// values are the whole point of asking.
type OrderStatus string

const (
	Received  OrderStatus = "received"
	Processed OrderStatus = "processed"
	Rejected  OrderStatus = "rejected"
)

// Acknowledgement is one line of the HAC file: an order the subscriber uploaded
// and what the host has done with it.
type Acknowledgement struct {
	OrderID   OrderID     `json:"orderId"`
	OrderType OrderType   `json:"orderType"`
	Status    OrderStatus `json:"status"`
	Detail    string      `json:"detail,omitempty"`
}

// File is one file waiting in, or collected from, a subscriber's download queue.
// Payload is opaque to this package and to the institution holding the queue.
type File struct {
	OrderID   OrderID   `json:"orderId"`
	OrderType OrderType `json:"orderType"`
	Payload   []byte    `json:"payload"`
}

// SubscriberID identifies one party enrolled at one host.
type SubscriberID string

func (s SubscriberID) String() string { return string(s) }

// normaliseSubscriber trims the header's whitespace and upper-cases it, because
// an identity that differs from an enrolment by a space is a mystery to debug
// rather than a rule to enforce.
func normaliseSubscriber(s string) SubscriberID {
	return SubscriberID(strings.ToUpper(strings.TrimSpace(s)))
}
