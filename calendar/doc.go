// Package calendar is the TARGET settlement calendar and the deployment's
// clock: which days money can move on, and what day this deployment thinks it
// is.
//
// # A settlement day is the settlement agent's day
//
// T2, the Eurosystem's RTGS, is open on business days only, and every batch
// scheme that settles in it inherits that. So the calendar here is not a
// currency's, not a market's and not any member state's: German banks are shut
// on 3 October and TARGET is not, which means a payment submitted that morning
// clears and settles that afternoon. A national holiday closes branches; it does
// not close the settlement agent.
//
// Six named days and two weekend days are the whole of it — New Year's Day, Good
// Friday, Easter Monday, 1 May, 25 December, 26 December — and two of the six
// move with Easter, which is arithmetic rather than a data file. That is why
// this is a function and not a table: there is nothing to ship, nothing to keep
// current and nothing that can be stale. What it costs is that the Eurosystem
// could add a closing day and this repository would need a commit to learn about
// it, where a table would need a row.
//
// TARGET substitutes nothing. A 25 December that falls on a Saturday is simply a
// closing day the year does not get, unlike the UK and US calendars, which move
// the observance to the following Monday.
//
// # Whose clock it is
//
// The business date is a fact about the DEPLOYMENT and is held in none of the
// N+2 databases. A bank's database carrying one would be that bank's opinion
// about what day it is, and two banks could then disagree — which is a state no
// clearing system has and this one should not be able to reach. So the clock is
// a small file beside the databases, or nothing at all when they are ephemeral.
//
// Advancing it moves one CALENDAR day, not to the next settlement day. A weekend
// is a day this system lives through rather than skips: interest accrues over
// it, which is the entire reason day-count conventions exist. Which days clear
// is IsSettlementDay's answer, and asking it is the business day engine's job.
//
// Advancing preserves the time of day, so every event within one business day
// carries the same instant. Ordering inside a day is therefore the entries'
// sequence and never their timestamps — which is already true of the ledger, and
// is stated here because a reader of two events an hour apart on the same day
// would otherwise expect to see an hour between them.
//
// # UTC, and the one import
//
// A date is read off an instant by ledger.DayStart, and this package imports
// ledger for that function alone. Computing the day boundary again here would be
// a second answer to "which day is this entry in", and the two would eventually
// disagree — for a holiday that is a payment that clears on a day the calendar
// says is shut.
//
// TARGET's own day is Frankfurt's, so an instant between midnight UTC and
// midnight CET is one date here and the previous date at the settlement agent.
// Nothing in this system can observe that: the clock only ever holds instants
// this package produced, and they are whole days apart.
//
// # What is absent
//
// There is no time of day within a settlement day. SEPA runs several settlement
// cycles per business day and this calendar cannot express one, because a
// cut-off time is a point inside a day and the day has to exist before a point
// inside it means anything.
package calendar
