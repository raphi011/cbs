package ebics

import (
	"errors"
	"fmt"
)

// ReturnCode is the protocol's technical answer to a request. It says what
// happened to the FILE — that it arrived, that the sender is not enrolled, that
// there was nothing to collect — and never what the file's contents mean.
type ReturnCode string

const (
	// OK is the only code that is not a refusal. On an upload it means the file
	// arrived and nothing more.
	OK ReturnCode = "EBICS_OK"

	// NoDownloadDataAvailable is a quiet day, not a failure: the queue this
	// subscriber asked about is empty.
	NoDownloadDataAvailable ReturnCode = "EBICS_NO_DOWNLOAD_DATA_AVAILABLE"

	// InvalidUserOrUserState is a subscriber this host has never enrolled. It
	// is the only membership question the transport answers, and it answers it
	// off the enrolment that created the queue rather than off any roster.
	InvalidUserOrUserState ReturnCode = "EBICS_INVALID_USER_OR_USER_STATE"

	// UnsupportedOrderType is an order type this host does not offer, or one
	// used in the direction it does not travel in.
	UnsupportedOrderType ReturnCode = "EBICS_UNSUPPORTED_ORDER_TYPE"

	// InvalidRequest is an envelope that could not be read as one.
	InvalidRequest ReturnCode = "EBICS_INVALID_REQUEST"

	// InternalError is the host failing at something that is nobody's fault but
	// its own.
	InternalError ReturnCode = "EBICS_INTERNAL_ERROR"
)

// Refusal is a request the host answered with something other than OK.
type Refusal struct {
	Code   ReturnCode
	Detail string
}

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return string(r.Code)
	}
	return fmt.Sprintf("%s: %s", r.Code, r.Detail)
}

// refuse is the constructor the server and the client both go through, so a
// refusal always carries a code.
func refuse(code ReturnCode, format string, args ...any) *Refusal {
	return &Refusal{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// CodeOf is the return code err carries, or "" if it is not a refusal — a
// connection that could not be made, say, which has no code because it never
// reached a host.
func CodeOf(err error) ReturnCode {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

// ErrUnknownOrder is an order id this host never minted, asked about from
// either side: Client.OrderStatus finding no acknowledgement for one, and a
// Store asked to record what became of one.
var ErrUnknownOrder = errors.New("ebics: no such order at this host")
