package ebics_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/store/testenv"
)

const (
	aurora = ebics.SubscriberID("AURODEFFXXX")
	borea  = ebics.SubscriberID("BOREITRRXXX")
)

// ctx is the one every call here takes. Nothing in this suite cancels anything;
// the parameter is there because every act on a host is a unit of work.
var ctx = context.Background()

// frozen is the clock the stores read. Nothing in the transport is dated, so it
// exists only because opening a store needs one.
func frozen() time.Time { return time.Unix(0, 0).UTC() }

// host is a server with aurora enrolled, which is what provisioning leaves
// behind.
func host(t *testing.T) *ebics.Server {
	t.Helper()
	s := ebics.NewServer(testenv.NewSet(t, frozen).ClearingHouseEBICS())
	s.Enrol(aurora)
	return s
}

func TestAnUploadIsAnsweredWithAnOrderIdAndNothingElse(t *testing.T) {
	s := host(t)

	id, err := s.Upload(ctx, aurora, ebics.CCT, []byte("a file of credit transfers"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if id == "" {
		t.Fatal("Upload minted no order id")
	}

	// The file is the institution's work list, and nothing has looked inside it.
	pending, err := s.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("Pending holds %d orders, want 1", len(pending))
	}
	if pending[0].ID != id || pending[0].Subscriber != aurora || pending[0].Type != ebics.CCT {
		t.Errorf("Pending holds %+v, want the order just uploaded", pending[0])
	}

	acks, err := s.Acknowledgements(ctx, aurora)
	if err != nil {
		t.Fatalf("Acknowledgements: %v", err)
	}
	if len(acks) != 1 || acks[0].Status != ebics.Received {
		t.Errorf("acknowledgements are %+v, want one that is merely received", acks)
	}
}

// TestASubscriberWithNoEnrolmentIsRefusedEverything is the only membership
// question the transport answers, and it answers it off the queue that
// enrolment creates.
func TestASubscriberWithNoEnrolmentIsRefusedEverything(t *testing.T) {
	s := host(t)

	if _, err := s.Upload(ctx, borea, ebics.CCT, nil); ebics.CodeOf(err) != ebics.InvalidUserOrUserState {
		t.Errorf("Upload by a stranger: %v, want %s", err, ebics.InvalidUserOrUserState)
	}
	if _, err := s.Download(ctx, borea, ebics.BTD); ebics.CodeOf(err) != ebics.InvalidUserOrUserState {
		t.Errorf("Download by a stranger: %v, want %s", err, ebics.InvalidUserOrUserState)
	}
	if _, err := s.Acknowledgements(ctx, borea); ebics.CodeOf(err) != ebics.InvalidUserOrUserState {
		t.Errorf("Acknowledgements for a stranger: %v, want %s", err, ebics.InvalidUserOrUserState)
	}

	// And a file addressed to it has nowhere to go, which is the same fact from
	// the other side: no enrolment, no queue.
	if _, err := s.Enqueue(ctx, borea, ebics.CST, nil); ebics.CodeOf(err) != ebics.InvalidUserOrUserState {
		t.Errorf("Enqueue to a stranger: %v, want %s", err, ebics.InvalidUserOrUserState)
	}
}

func TestOneQueueComesOutInTheOrderItWentIn(t *testing.T) {
	s := host(t)
	for _, payload := range []string{"first", "second", "third"} {
		if _, err := s.Enqueue(ctx, aurora, ebics.CST, []byte(payload)); err != nil {
			t.Fatalf("Enqueue %s: %v", payload, err)
		}
	}

	files, err := s.Download(ctx, aurora, ebics.BTD)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("collected %d files, want 3", len(files))
	}
	for i, want := range []string{"first", "second", "third"} {
		if got := string(files[i].Payload); got != want {
			t.Errorf("file %d is %q, want %q", i, got, want)
		}
	}
}

func TestADownloadEmptiesTheQueueExactlyOnce(t *testing.T) {
	s := host(t)
	if _, err := s.Enqueue(ctx, aurora, ebics.CST, []byte("a status report")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if files, err := s.Download(ctx, aurora, ebics.BTD); err != nil || len(files) != 1 {
		t.Fatalf("first Download = %d files, %v; want 1 file", len(files), err)
	}
	if _, err := s.Download(ctx, aurora, ebics.BTD); ebics.CodeOf(err) != ebics.NoDownloadDataAvailable {
		t.Errorf("second Download: %v, want %s", err, ebics.NoDownloadDataAvailable)
	}
}

// TestTheStatementQueueIsCollectedSeparately is what lets a bank decide the
// order it learns things in: the statement is a C53 and everything else is a
// BTD, so collecting one does not drag the other along.
func TestTheStatementQueueIsCollectedSeparately(t *testing.T) {
	s := host(t)
	if _, err := s.Enqueue(ctx, aurora, ebics.C53, []byte("a statement")); err != nil {
		t.Fatalf("Enqueue a statement: %v", err)
	}
	if _, err := s.Enqueue(ctx, aurora, ebics.CST, []byte("a status report")); err != nil {
		t.Fatalf("Enqueue a status report: %v", err)
	}

	statements, err := s.Download(ctx, aurora, ebics.C53)
	if err != nil {
		t.Fatalf("Download C53: %v", err)
	}
	if len(statements) != 1 || string(statements[0].Payload) != "a statement" {
		t.Fatalf("C53 collected %+v, want the statement alone", statements)
	}

	rest, err := s.Download(ctx, aurora, ebics.BTD)
	if err != nil {
		t.Fatalf("Download BTD: %v", err)
	}
	if len(rest) != 1 || string(rest[0].Payload) != "a status report" {
		t.Errorf("BTD collected %+v, want the status report alone", rest)
	}
}

func TestTwoSubscribersSeeOnlyTheirOwnQueue(t *testing.T) {
	s := host(t)
	s.Enrol(borea)

	if _, err := s.Enqueue(ctx, aurora, ebics.CST, []byte("for aurora")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if _, err := s.Download(ctx, borea, ebics.BTD); ebics.CodeOf(err) != ebics.NoDownloadDataAvailable {
		t.Errorf("borea collected aurora's file: %v", err)
	}
	if files, err := s.Download(ctx, aurora, ebics.BTD); err != nil || len(files) != 1 {
		t.Errorf("aurora's own file: %d files, %v", len(files), err)
	}
}

func TestAnOrderTypeTravellingTheWrongWayIsRefused(t *testing.T) {
	s := host(t)

	if _, err := s.Upload(ctx, aurora, ebics.C53, nil); ebics.CodeOf(err) != ebics.UnsupportedOrderType {
		t.Errorf("uploading a statement: %v, want %s", err, ebics.UnsupportedOrderType)
	}
	if _, err := s.Download(ctx, aurora, ebics.CCT); ebics.CodeOf(err) != ebics.UnsupportedOrderType {
		t.Errorf("collecting a CCT: %v, want %s", err, ebics.UnsupportedOrderType)
	}

	// BTD selects files and never names one, so nothing can be enqueued under
	// it — nor under HAC, which is computed rather than queued.
	for _, selector := range []ebics.OrderType{ebics.BTD, ebics.HAC} {
		if _, err := s.Enqueue(ctx, aurora, selector, nil); ebics.CodeOf(err) != ebics.UnsupportedOrderType {
			t.Errorf("enqueuing a %s: %v, want %s", selector, err, ebics.UnsupportedOrderType)
		}
	}
}

// TestAnOrderIsAnsweredOnceTheInstitutionHasWorkedThroughIt walks the state HAC
// exists to expose, and the middle of the three is the one no push transport
// has.
func TestAnOrderIsAnsweredOnceTheInstitutionHasWorkedThroughIt(t *testing.T) {
	s := host(t)
	id, err := s.Upload(ctx, aurora, ebics.CCT, []byte("a file"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if got := statusOf(t, s, id); got != ebics.Received {
		t.Errorf("before the institution ran: %s, want %s", got, ebics.Received)
	}

	if err := s.Processed(ctx, id, "1 transaction, 1 accepted"); err != nil {
		t.Fatalf("Processed: %v", err)
	}
	if got := statusOf(t, s, id); got != ebics.Processed {
		t.Errorf("after the institution ran: %s, want %s", got, ebics.Processed)
	}

	// It leaves the work list, and the queue it was never in stays empty: an
	// answer about a file's contents travels as a message, not as this.
	pending, err := s.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Pending still holds %d orders", len(pending))
	}
}

func TestAFileTheReceiverCouldNotReadIsRecordedAgainstItsOrder(t *testing.T) {
	s := host(t)
	id, err := s.Upload(ctx, aurora, ebics.CCT, []byte("not a pacs.008"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// The uploader was told the file arrived and went away, so this is the only
	// place the failure can be recorded.
	if err := s.Rejected(ctx, id, "the file is not a pacs.008"); err != nil {
		t.Fatalf("Rejected: %v", err)
	}

	acks, err := s.Acknowledgements(ctx, aurora)
	if err != nil {
		t.Fatalf("Acknowledgements: %v", err)
	}
	if len(acks) != 1 || acks[0].Status != ebics.Rejected || acks[0].Detail == "" {
		t.Errorf("acknowledgements are %+v, want a rejection with a reason", acks)
	}
}

func TestAnsweringAnOrderNoHostHasIsRefused(t *testing.T) {
	s := host(t)

	err := s.Processed(ctx, "Z999", "")
	if ebics.CodeOf(err) != ebics.InvalidRequest {
		t.Errorf("Processed on an unknown order: %v, want %s", err, ebics.InvalidRequest)
	}
	var refusal *ebics.Refusal
	if !errors.As(err, &refusal) {
		t.Errorf("the error is %T, want a *ebics.Refusal", err)
	}
}

func TestEnrollingTwiceIsNothing(t *testing.T) {
	s := host(t)
	if _, err := s.Enqueue(ctx, aurora, ebics.CST, []byte("for aurora")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Provisioning runs against a deployment that may already hold the
	// institution, so a second enrolment must not empty the queue.
	s.Enrol(aurora)

	if files, err := s.Download(ctx, aurora, ebics.BTD); err != nil || len(files) != 1 {
		t.Errorf("after re-enrolment: %d files, %v; want the file still there", len(files), err)
	}
}

// TestAPublishedFileIsNotConsumedByCollectingIt is the whole difference between
// a snapshot and a queue.
func TestAPublishedFileIsNotConsumedByCollectingIt(t *testing.T) {
	s := host(t)
	s.Enrol(borea)

	if err := s.Publish(ebics.HRD, []byte("the routing table")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// The same bytes, twice to one subscriber and once to another. A queue would
	// have been empty on the second ask.
	for _, sub := range []ebics.SubscriberID{aurora, aurora, borea} {
		raw, err := s.Published(sub, ebics.HRD)
		if err != nil {
			t.Fatalf("Published for %s: %v", sub, err)
		}
		if string(raw) != "the routing table" {
			t.Errorf("%s collected %q, want the published file", sub, raw)
		}
	}

	// And republishing replaces it, because a directory is a snapshot and not a
	// feed of what changed.
	if err := s.Publish(ebics.HRD, []byte("a member joined")); err != nil {
		t.Fatalf("Publish again: %v", err)
	}
	if raw, _ := s.Published(aurora, ebics.HRD); string(raw) != "a member joined" {
		t.Errorf("after republishing, %s collected %q", aurora, raw)
	}
}

// TestAPublishedTypeIsNeitherQueuedNorAddressed pins the three refusals that
// keep a snapshot from being mistaken for a queue.
func TestAPublishedTypeIsNeitherQueuedNorAddressed(t *testing.T) {
	s := host(t)

	// Nothing published yet, which is not a failure of the caller's: there is
	// simply nothing to collect, exactly as an empty queue answers.
	if _, err := s.Published(aurora, ebics.HRD); ebics.CodeOf(err) != ebics.NoDownloadDataAvailable {
		t.Errorf("collecting before anything is published: %v, want %s", err, ebics.NoDownloadDataAvailable)
	}
	if err := s.Publish(ebics.HRD, []byte("the routing table")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// A published file is published TO THE SUBSCRIBERS and not to the world.
	if _, err := s.Published(borea, ebics.HRD); ebics.CodeOf(err) != ebics.InvalidUserOrUserState {
		t.Errorf("collecting by a stranger: %v, want %s", err, ebics.InvalidUserOrUserState)
	}
	// It is addressed to nobody, so it cannot be put in one subscriber's queue.
	if _, err := s.Enqueue(ctx, aurora, ebics.HRD, nil); ebics.CodeOf(err) != ebics.UnsupportedOrderType {
		t.Errorf("enqueuing the routing table: %v, want %s", err, ebics.UnsupportedOrderType)
	}
	// And nothing else this host offers is a snapshot.
	if err := s.Publish(ebics.C53, nil); ebics.CodeOf(err) != ebics.UnsupportedOrderType {
		t.Errorf("publishing a statement: %v, want %s", err, ebics.UnsupportedOrderType)
	}
}

// TestASecondHostOverTheSameStoreIsHoldingWhatTheFirstWas is the whole reason
// this state is rows.
func TestASecondHostOverTheSameStoreIsHoldingWhatTheFirstWas(t *testing.T) {
	set := testenv.NewSet(t, frozen)

	first := ebics.NewServer(set.ClearingHouseEBICS())
	first.Enrol(aurora)
	id, err := first.Upload(ctx, aurora, ebics.CCT, []byte("a file of credit transfers"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if _, err := first.Enqueue(ctx, aurora, ebics.CST, []byte("a status report")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// The process ends and another opens the same database.
	second := ebics.NewServer(set.ClearingHouseEBICS())
	second.Enrol(aurora)

	files, err := second.Download(ctx, aurora, ebics.BTD)
	if err != nil {
		t.Fatalf("Download after the restart: %v", err)
	}
	if len(files) != 1 || string(files[0].Payload) != "a status report" {
		t.Errorf("collected %+v after the restart, want the file the first host was holding", files)
	}

	pending, err := second.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending after the restart: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != id || string(pending[0].Payload) != "a file of credit transfers" {
		t.Errorf("the work list is %+v after the restart, want the order the first host took in", pending)
	}
	if got := statusOf(t, second, id); got != ebics.Received {
		t.Errorf("order %s is %s after the restart, want %s", id, got, ebics.Received)
	}

	// And the id space does not start again. An order id is unique at the host
	// that minted it, and a counter rebuilt from the rows would hand this one the
	// id the first host has already acknowledged.
	next, err := second.Upload(ctx, aurora, ebics.CCT, []byte("another file"))
	if err != nil {
		t.Fatalf("Upload after the restart: %v", err)
	}
	if next == id {
		t.Errorf("the second host minted %s again, which the first host has already answered for", next)
	}
}

func statusOf(t *testing.T, s *ebics.Server, id ebics.OrderID) ebics.OrderStatus {
	t.Helper()
	acks, err := s.Acknowledgements(ctx, aurora)
	if err != nil {
		t.Fatalf("Acknowledgements: %v", err)
	}
	for _, a := range acks {
		if a.OrderID == id {
			return a.Status
		}
	}
	t.Fatalf("no acknowledgement for order %s", id)
	return ""
}
