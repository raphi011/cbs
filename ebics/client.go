package ebics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client is one subscriber's connection to one host.
//
// A subscriber holds one of these per host it deals with, and that is the entire
// address book of the system: a member bank has two, the clearing house has one,
// and the settlement agent has none because it dials nobody.
//
// Everything it can do, it initiates. There is no callback and nothing arrives
// unasked, so a bank that never calls Download is a bank whose customers are
// never told the fate of anything — a real operational failure with no analogue
// in a system where results are pushed.
type Client struct {
	// Subscriber is who this connection claims to be. See SubscriberHeader.
	Subscriber SubscriberID

	// URL is the host's single endpoint.
	URL string

	// HTTP is the transport. It carries a timeout, because a counterparty that
	// accepts a connection and then says nothing is one of the failures this
	// package exists to make expressible.
	HTTP *http.Client
}

// NewClient returns a connection from sub to the host at url.
func NewClient(sub SubscriberID, url string) *Client {
	return &Client{
		Subscriber: sub,
		URL:        url,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
	}
}

// Upload sends a file and returns the order id the host minted for it.
//
// The answer is technical. An order id means the file arrived and parsed as an
// envelope; what the receiver makes of the payments inside it comes back later,
// on a download. An error here means the file did NOT arrive, and the caller
// still holds it — a send has never been inside anybody's unit of work.
func (c *Client) Upload(ctx context.Context, t OrderType, payload []byte) (OrderID, error) {
	resp, err := c.do(ctx, Request{OrderType: t, Payload: payload})
	if err != nil {
		return "", err
	}
	return resp.OrderID, nil
}

// Download collects what is waiting: C53 for statements, BTD for everything
// else, in the order the host queued it.
//
// An empty queue is no files and no error. EBICS_NO_DOWNLOAD_DATA_AVAILABLE is a
// quiet day rather than a failure, and a caller made to branch on it would
// branch on it at every call site.
func (c *Client) Download(ctx context.Context, t OrderType) ([]File, error) {
	resp, err := c.do(ctx, Request{OrderType: t})
	if CodeOf(err) == NoDownloadDataAvailable {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return resp.Files, nil
}

// Acknowledgements is the HAC download: every order this subscriber has sent
// this host, and what became of each, oldest first.
func (c *Client) Acknowledgements(ctx context.Context) ([]Acknowledgement, error) {
	resp, err := c.do(ctx, Request{OrderType: HAC})
	if err != nil {
		return nil, err
	}
	return resp.Acknowledgements, nil
}

// OrderStatus is what became of one order.
//
// The three answers it separates are the reason HAC is worth having: an order
// the host has never heard of (ErrUnknownOrder), one that arrived and has not
// been looked at (Received), and one the host has worked through (Processed or
// Rejected). A transport that answered what a file meant at the moment it was
// sent could not tell the middle one from either neighbour.
func (c *Client) OrderStatus(ctx context.Context, id OrderID) (Acknowledgement, error) {
	acks, err := c.Acknowledgements(ctx)
	if err != nil {
		return Acknowledgement{}, err
	}
	for _, a := range acks {
		if a.OrderID == id {
			return a, nil
		}
	}
	return Acknowledgement{}, fmt.Errorf("%w: %s", ErrUnknownOrder, id)
}

// do sends one envelope and reads the answer, turning a return code that is not
// OK into a *Refusal.
//
// The two failures it keeps apart are the ones the mesh could not express: an
// error with no return code is a host that was not reached — connection refused,
// a timeout, a 500 — and a *Refusal is a host that answered and said no.
func (c *Client) do(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ebics: encoding a %s order: %w", req.OrderType, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ebics: addressing %s: %w", c.URL, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(SubscriberHeader, c.Subscriber.String())

	httpResp, err := c.client().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ebics: %s is unreachable: %w", c.URL, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ebics: %s answered HTTP %d, which is not an envelope", c.URL, httpResp.StatusCode)
	}

	var resp Response
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("ebics: %s answered something that is not an envelope: %w", c.URL, err)
	}
	if resp.ReturnCode != OK {
		return nil, &Refusal{Code: resp.ReturnCode, Detail: resp.Detail}
	}
	return &resp, nil
}

func (c *Client) client() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}
