package ebics_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/store/testenv"
)

// dial stands a host up on a real listener and returns a subscriber's
// connection to it.
func dial(t *testing.T) (*ebics.Server, *ebics.Client) {
	t.Helper()

	s := host(t)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	return s, ebics.NewClient(aurora, srv.URL)
}

// bare is a host with nobody enrolled: what a listener serves before
// provisioning has admitted anybody.
func bare(t *testing.T) *ebics.Server {
	t.Helper()
	return ebics.NewServer(testenv.NewSet(t, frozen).ClearingHouseEBICS())
}

// TestAFileGoesUpAndTheAnswerComesBackOnALaterDownload is the seam the whole
// package is for: the upload is answered technically, and what the receiver made
// of the contents is a separate collection.
func TestAFileGoesUpAndTheAnswerComesBackOnALaterDownload(t *testing.T) {
	s, c := dial(t)

	id, err := c.Upload(ctx, ebics.CCT, []byte("a file of credit transfers"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Nothing is waiting yet. The uploader knows its file arrived and nothing
	// else, which is the honest state of a bulk system most of the time.
	if files, err := c.Download(ctx, ebics.BTD); err != nil || len(files) != 0 {
		t.Fatalf("straight after uploading: %d files, %v; want a quiet queue", len(files), err)
	}

	// The institution works through its inbox and addresses an answer back.
	pending, err := s.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("the host holds %d orders, want 1", len(pending))
	}
	if _, err := s.Enqueue(ctx, pending[0].Subscriber, ebics.CST, []byte("a status report")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := s.Processed(ctx, pending[0].ID, "1 transaction"); err != nil {
		t.Fatalf("Processed: %v", err)
	}

	files, err := c.Download(ctx, ebics.BTD)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(files) != 1 || string(files[0].Payload) != "a status report" {
		t.Fatalf("collected %+v, want the status report", files)
	}

	ack, err := c.OrderStatus(ctx, id)
	if err != nil {
		t.Fatalf("OrderStatus: %v", err)
	}
	if ack.Status != ebics.Processed {
		t.Errorf("order %s is %s, want %s", id, ack.Status, ebics.Processed)
	}
}

// TestOrderStatusSeparatesTheThreeAnswers is HAC's whole purpose.
func TestOrderStatusSeparatesTheThreeAnswers(t *testing.T) {
	s, c := dial(t)

	if _, err := c.OrderStatus(ctx, "A000"); !errors.Is(err, ebics.ErrUnknownOrder) {
		t.Errorf("an order nobody sent: %v, want ErrUnknownOrder", err)
	}

	id, err := c.Upload(ctx, ebics.CCT, []byte("a file"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	ack, err := c.OrderStatus(ctx, id)
	if err != nil {
		t.Fatalf("OrderStatus: %v", err)
	}
	if ack.Status != ebics.Received {
		t.Errorf("an order nobody has looked at: %s, want %s", ack.Status, ebics.Received)
	}

	if err := s.Rejected(ctx, id, "the file is not a pacs.008"); err != nil {
		t.Fatalf("Rejected: %v", err)
	}
	ack, err = c.OrderStatus(ctx, id)
	if err != nil {
		t.Fatalf("OrderStatus: %v", err)
	}
	if ack.Status != ebics.Rejected || !strings.Contains(ack.Detail, "pacs.008") {
		t.Errorf("an order the host refused: %+v, want a rejection carrying its reason", ack)
	}
}

// TestAHostThatCannotBeReachedIsNotARefusal: the two call for opposite things —
// retry the first, do not retry the second.
func TestAHostThatCannotBeReachedIsNotARefusal(t *testing.T) {
	srv := httptest.NewServer(bare(t))
	url := srv.URL
	srv.Close()

	c := ebics.NewClient(aurora, url)
	_, err := c.Upload(ctx, ebics.CCT, []byte("a file"))
	if err == nil {
		t.Fatal("uploading to a closed listener succeeded")
	}
	if code := ebics.CodeOf(err); code != "" {
		t.Errorf("an unreachable host answered %s; it answered nothing at all", code)
	}
}

// TestARefusalIsAnEnvelopeAndNotAnHttpError keeps the two failure kinds apart on
// the wire as well as in the client.
func TestARefusalIsAnEnvelopeAndNotAnHttpError(t *testing.T) {
	srv := httptest.NewServer(bare(t))
	t.Cleanup(srv.Close)

	body := strings.NewReader(`{"orderType":"CCT"}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL, body)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set(ebics.SubscriberHeader, string(borea))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("posting: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("HTTP %d, want 200: the host was reached and it refused", resp.StatusCode)
	}
	var envelope ebics.Response
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decoding the envelope: %v", err)
	}
	if envelope.ReturnCode != ebics.InvalidUserOrUserState {
		t.Errorf("return code %s, want %s", envelope.ReturnCode, ebics.InvalidUserOrUserState)
	}
}

func TestARequestThatNamesNobodyIsRefused(t *testing.T) {
	srv := httptest.NewServer(bare(t))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"orderType":"CCT"}`))
	if err != nil {
		t.Fatalf("posting: %v", err)
	}
	defer resp.Body.Close()

	var envelope ebics.Response
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decoding the envelope: %v", err)
	}
	if envelope.ReturnCode != ebics.InvalidRequest {
		t.Errorf("a request with no subscriber header: %s, want %s", envelope.ReturnCode, ebics.InvalidRequest)
	}
}

// TestTheEndpointIsOneUrlAndTakesOnlyAPost pins the protocol's shape: the order
// type is in the envelope and never in a path, so the handler answers wherever
// the deployment mounts it.
func TestTheEndpointIsOneUrlAndTakesOnlyAPost(t *testing.T) {
	s := host(t)
	mux := http.NewServeMux()
	mux.Handle("/somewhere/else/", http.StripPrefix("/somewhere/else", s))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := ebics.NewClient(aurora, srv.URL+"/somewhere/else/")
	if _, err := c.Upload(ctx, ebics.CCT, []byte("a file")); err != nil {
		t.Errorf("uploading to a mounted handler: %v", err)
	}

	resp, err := http.Get(srv.URL + "/somewhere/else/")
	if err != nil {
		t.Fatalf("getting: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET answered HTTP %d, want 405", resp.StatusCode)
	}
}

// TestACancelledCollectionIsTheCallersOwnFailure — the context reaches the
// request, so a day engine that gives up on a slow counterparty is not left
// holding a goroutine.
func TestACancelledCollectionIsTheCallersOwnFailure(t *testing.T) {
	_, c := dial(t)

	ctx, cancel := context.WithCancel(ctx)
	cancel()

	if _, err := c.Download(ctx, ebics.BTD); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled download: %v, want context.Canceled", err)
	}
}
