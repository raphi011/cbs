package api

import "time"

// The mesh: every institution at once. It is the DEPLOYMENT's shape and no
// institution's, which is why nothing here is reachable from one.

// The part an institution plays in the network. A member bank is a subscriber
// at both hosts; the clearing house is a host and a subscriber; the settlement
// agent is a host and a subscriber nowhere.
const (
	RoleMemberBank      = "member bank"
	RoleClearingHouse   = "clearing house"
	RoleSettlementAgent = "settlement agent"
)

// The event names a watcher listens for. Each selects one of the three shapes a
// day's report is made of, because the stream is that report arriving without
// anybody having asked for it.
const (
	EventFile    = "file"
	EventOutcome = "outcome"
	EventProblem = "problem"
)

// NetworkFlowDTO is the mesh: every institution this deployment holds, the
// connections between them, and every file that crossed one.
type NetworkFlowDTO struct {
	Institutions []InstitutionDTO `json:"institutions"`
	Wires        []WireDTO        `json:"wires"`
	Crossings    []CrossingDTO    `json:"crossings"`
}

// InstitutionDTO is one node of the mesh: an address, a name, and which part it
// plays.
type InstitutionDTO struct {
	BIC  string `json:"bic"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// WireDTO is one EBICS connection, named by the parts its ends play: a
// subscriber dials a host, and nothing is ever pushed the other way. A bank
// with no wire is one the scheme has not admitted.
type WireDTO struct {
	Subscriber string `json:"subscriber"`
	Host       string `json:"host"`
}

// CrossingDTO is one file between two institutions: the send one end recorded
// and the take the other did, paired on the transport's order id. A crossing
// with no take is a file resting on the wire — a queue nobody has come for.
type CrossingDTO struct {
	From string `json:"from"`
	To   string `json:"to"`

	// MsgDefIdr and MsgID are the envelope's own header: what the file is, and
	// the id its sender put on it.
	MsgDefIdr string `json:"msgDefIdr"`
	MsgID     string `json:"msgId"`

	// OrderID is the transport's handle, and it is what the two halves of one
	// crossing have in common.
	OrderID string `json:"orderId,omitempty"`

	// SentSeq and ReceivedSeq name the row in each end's OWN log, which is how a
	// reader gets from the mesh to the document. A seq means nothing elsewhere.
	SentSeq     int64 `json:"sentSeq,omitempty"`
	ReceivedSeq int64 `json:"receivedSeq,omitempty"`

	// SentAt is absent where the SENDER holds no record of a file its
	// counterparty received, which is a missing record rather than a state a
	// crossing passes through.
	SentAt *time.Time `json:"sentAt,omitempty"`
	// ReceivedAt is absent while the file is still resting on the wire.
	ReceivedAt *time.Time `json:"receivedAt,omitempty"`

	// Payments is what the file carried, in document order. A file naming none is
	// ordinary: a settlement instruction and a statement both do.
	Payments []string `json:"payments"`
	// PayloadSize is how big the document is, which is how the mesh says a file
	// is there without carrying it.
	PayloadSize int `json:"payloadSize"`
}
