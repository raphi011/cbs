// Package ebics is the file transport between institutions: an envelope, order
// types, order ids, return codes, and one download queue per subscriber.
//
// It carries bytes. It parses no pacs.008, holds no payment and imports nothing
// from this repository, which is what lets a clearing house hold a file
// addressed to a member bank without being able to read it. What the file MEANS
// is the business layer's, on both ends.
//
// # The subscriber dials, and nothing is ever pushed
//
// EBICS has no push. A member bank uploads its files to the clearing house and
// comes back later to collect whatever is waiting for it; the clearing house
// does the same at the settlement agent. The settlement agent dials nobody.
//
// That is the whole topology, and it is why there is no address table here. An
// institution routes a file to a subscriber by putting it in that subscriber's
// download queue, so there is nothing to resolve and nothing that can disagree
// with the roster: enrolment is what creates the queue, and a file addressed to
// somebody who is not enrolled has nowhere to go and is refused.
//
// # An upload is answered technically, and only technically
//
// Upload answers with a return code and a minted order id, immediately. EBICS_OK
// means the file arrived — nothing more. What the receiver made of its contents
// comes back later, as a business message the subscriber collects on a download,
// and that seam is the point of the protocol rather than an artefact of it.
//
// HAC is what makes the seam visible. A subscriber that has uploaded a file and
// been told nothing about its contents can ask what became of the ORDER, and
// "received, not yet processed" is an answer distinct from "processed" and from
// "no such order". It is also the one download this package understands the
// contents of, because it is about the transport rather than about payments.
//
// # What the header stands in for
//
// Subscriber identity is a request header (SubscriberHeader), and it is not
// authentication. Real EBICS is the most heavily authenticated hop in the whole
// chain: three RSA key pairs per subscriber — A006 signs the order, E002
// encrypts the payload, X002 authenticates the request — with enrolment
// half-offline by design (INI/HIA, public keys electronically and then a
// hand-signed paper letter carrying their hashes, which the bank compares).
// None of that is here. The header is a claim, exactly as a listener's port is a
// claim in api. A reader should not infer that file transfer between banks is
// unauthenticated by nature; it is the opposite, and this models none of it.
//
// # A queue outlives the process holding it
//
// The queues and the order log are rows in the hosting institution's own
// database, reached through a port this package declares (Store) and implemented
// somewhere this package does not name. Nothing here is lost when a process ends:
// a file released into a receiving bank's queue is an obligation with money
// already moved against it, and an order log a restart emptied would answer HAC
// about nothing. The bytes stay opaque for all that — see Store, which carries
// the argument.
//
// # One queue is ordered, two queues are not
//
// Files come out of a subscriber's queue in the order they went in, and there is
// no ordering whatsoever between two queues. A bank collecting from the
// settlement agent and from the clearing house is holding two connections that
// share nothing, so an ordering it needs between them is a decision it makes
// about the sequence of its own collections — not a property the transport can
// be asked for.
//
// # What is absent
//
// No security half: no A006, E002 or X002, no INI/HIA, no VEU distributed
// signature. No EBICS 3.0 BTF descriptor — the order types here are 2.x's
// three-letter codes. No host id, because one listener is one institution and a
// host id would be a second name for the connection.
//
// No download receipt. Real EBICS downloads in three phases — initialisation,
// transfer, receipt — and the receipt is what makes a client that died mid-read
// collect the file again rather than lose it. Here the queue row is deleted in
// the same transaction that reads it, so a client that dies after the response
// is written has lost the files and one that dies before it has not.
//
// And no delivery guarantee. The store-and-forward transports under real bulk
// clearing, SWIFT FIN and FileAct, carry non-repudiation and a delivery
// guarantee. This one carries an HTTP status.
package ebics
