import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "06-booking-date-vs-value-date",
  number: 6,
  part: "Part II · Transactions and Time",
  title: "Booking Date vs. Value Date",
  questions: [
    {
      kind: "mc",
      id: "ch6-q1",
      difficulty: "intro",
      concept: "booking-date",
      prompt: "What does the booking date represent in a banking transaction?",
      options: [
        "The date funds become economically available",
        "The date the transaction was recorded in the system",
        "The date interest starts accruing",
        "The date the bank confirms the transaction with the customer",
      ],
      answer: 1,
      explanation:
        "The [[booking-date]] is purely a processing timestamp — it records when the system captured the entry. Economic effect (interest, availability) follows the [[value-date]].",
    },
    {
      kind: "mc",
      id: "ch6-q2",
      difficulty: "intro",
      concept: "value-date",
      prompt: "What does the value date determine?",
      options: [
        "When the bank's back-office team reviewed the transaction",
        "When the transaction appears in audit logs",
        "When interest starts and funds become available",
        "When the customer's statement is generated",
      ],
      answer: 2,
      explanation:
        "The [[value-date]] is the economic effective date. Interest accrual, fund availability, and period-end reporting all follow the value date, not the booking date.",
    },
    {
      kind: "truefalse",
      id: "ch6-q3",
      difficulty: "intro",
      concept: "booking-date",
      prompt:
        "For a simple cash deposit made in a branch, the booking date and value date are always the same.",
      answer: true,
      explanation:
        "Cash deposits are the textbook case where [[booking-date]] and [[value-date]] coincide — the funds are immediately available and interest starts the same day they are recorded.",
    },
    {
      kind: "mc",
      id: "ch6-q4",
      difficulty: "core",
      concept: "value-date",
      prompt:
        "A wire transfer arrives at a bank on Friday evening after the cut-off. The bank books it Friday but sets the value date to Monday. Why?",
      options: [
        "The bank wants to earn interest over the weekend",
        "Monday is when the funds economically take effect — the bank cannot act on them until then",
        "Value dates must always be a business day, so Friday is never valid",
        "Wires always have a two-day settlement delay",
      ],
      answer: 1,
      explanation:
        "The [[value-date]] reflects economic reality. Because the receiving bank cannot deploy the funds until Monday, value-dating to Monday correctly captures when the funds take effect. The [[booking-date]] (Friday) only records when the instruction was captured.",
    },
    {
      kind: "mc",
      id: "ch6-q5",
      difficulty: "core",
      concept: "booking-date",
      prompt:
        "A customer deposits a check on Monday. The bank books it Monday but sets the value date to Wednesday. What will be true on Tuesday?",
      options: [
        "The customer can withdraw the funds because the booking date has passed",
        "Interest has been accruing since Monday",
        "The funds are not yet available and no interest has accrued",
        "The check is considered dishonored",
      ],
      answer: 2,
      explanation:
        "Availability and interest both follow the [[value-date]] (Wednesday), not the [[booking-date]] (Monday). On Tuesday the transaction is recorded but has not yet taken economic effect.",
    },
    {
      kind: "truefalse",
      id: "ch6-q6",
      difficulty: "core",
      concept: "value-date",
      prompt: "The value date must always be on or after the booking date.",
      answer: false,
      explanation:
        "The [[value-date]] can be *earlier* than the [[booking-date]]. Back-dated corrections are a common example: the system records the fix today (booking date = today) but value-dates it to the past so interest is recalculated correctly for the historical period.",
    },
    {
      kind: "mc",
      id: "ch6-q7",
      difficulty: "core",
      concept: "value-date",
      prompt:
        "An accountant discovers a posting error from last Tuesday and corrects it today (Friday). The correction should carry which value date?",
      options: [
        "Today's date, because that is when the correction was made",
        "Last Tuesday, so interest and balances reflect the correct historical position",
        "Next Monday, to give the system time to process the adjustment",
        "The end of the current month",
      ],
      answer: 1,
      explanation:
        "Back-dated corrections use the [[value-date]] of the original erroneous event. This ensures that interest calculations and period-end balance snapshots are accurate for the historical date, even though the [[booking-date]] is today.",
    },
    {
      kind: "mc",
      id: "ch6-q8",
      difficulty: "challenge",
      concept: "value-date",
      prompt:
        "A standing order is set up today (Wednesday) to pay a bill next Monday. Which statement is correct?",
      options: [
        "Both booking date and value date are Wednesday",
        "Booking date is Wednesday; value date is next Monday",
        "Booking date is next Monday; value date is Wednesday",
        "Both dates are next Monday",
      ],
      answer: 1,
      explanation:
        "Forward-dated standing orders are captured and recorded immediately — [[booking-date]] = Wednesday. But the economic effect (funds leave the account, interest stops) does not happen until the execution date — [[value-date]] = Monday.",
    },
    {
      kind: "truefalse",
      id: "ch6-q9",
      difficulty: "challenge",
      concept: "value-date",
      prompt:
        "A bank's period-end balance report should include all transactions whose booking date falls within the period, regardless of value date.",
      answer: false,
      explanation:
        "Correct period-end reporting uses the [[value-date]], not the [[booking-date]]. Including transactions by booking date would misstate balances, interest, and liquidity positions — a transaction booked in the period but value-dated outside it has not yet taken economic effect.",
    },
    {
      kind: "mc",
      id: "ch6-q10",
      difficulty: "challenge",
      concept: "booking-date",
      prompt:
        "Which of the following correctly describes the relationship between booking date and value date?",
      options: [
        "They are always identical",
        "The booking date records system capture; value date governs economic effect — they can differ in either direction",
        "Value date is always later than booking date to allow for clearing",
        "Booking date is always later than value date because posting lags reality",
      ],
      answer: 1,
      explanation:
        "[[booking-date]] and [[value-date]] serve different purposes and can diverge in either direction. Value-dated earlier than booking = back-dated correction. Value-dated later = future-dated standing order or a check clearing delay. For simple cash deposits they coincide.",
    },
  ],
};
