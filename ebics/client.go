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
func (c *Client) Upload(ctx context.Context, t OrderType, payload []byte) (OrderID, error) {
	resp, err := c.do(ctx, Request{OrderType: t, Payload: payload})
	if err != nil {
		return "", err
	}
	return resp.OrderID, nil
}

// Download collects what is waiting: C53 for statements, BTD for everything
// else, in the order the host queued it. A published type — HRD — comes back the
// same way and empties nothing; see Server.Published.
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
