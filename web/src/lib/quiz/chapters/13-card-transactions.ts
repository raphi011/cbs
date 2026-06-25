import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "13-card-transactions",
  number: 13,
  part: "Part IV · Moving Money Between Banks",
  title: "Card Transactions",
  questions: [
    {
      kind: "mc",
      id: "ch13-q1",
      difficulty: "intro",
      concept: "holds",
      prompt:
        "When a card authorization is approved, what immediately happens to the cardholder's account?",
      options: [
        "The book balance decreases and the available balance decreases",
        "The book balance is unchanged but the available balance decreases",
        "Both the book balance and available balance are unchanged",
        "The book balance decreases but the available balance is unchanged",
      ],
      answer: 1,
      explanation:
        "At authorization, a [[holds]] is placed. The **book balance stays the same** — no posting has occurred — but the **available balance drops** because the held amount is reserved and cannot be spent.",
      explore: { label: "View payments", href: "/payments" },
    },
    {
      kind: "mc",
      id: "ch13-q2",
      difficulty: "intro",
      concept: "hold-capture",
      prompt:
        "In the dual-message card flow, what is 'presentment' (capture)?",
      options: [
        "The initial authorization that places a hold",
        "The real posting that replaces the hold with an actual debit entry",
        "The moment the merchant receives the funds",
        "The card network's netting calculation",
      ],
      answer: 1,
      explanation:
        "[[hold-capture]] (presentment) is when the merchant submits the final amount for collection. The authorization hold is converted into a **real accounting entry** — debit cardholder deposit, credit clearing suspense — and the hold is released.",
    },
    {
      kind: "truefalse",
      id: "ch13-q3",
      difficulty: "intro",
      concept: "holds",
      prompt:
        "In a dual-message card transaction the book balance decreases at authorization, before capture.",
      answer: false,
      explanation:
        "Authorization only places a [[holds]] — **no posting occurs**. The book balance changes only at capture (presentment), when the real debit entry is made. This is the key distinction between dual-message (hold → capture) and single-message flows.",
    },
    {
      kind: "mc",
      id: "ch13-q4",
      difficulty: "core",
      concept: "hold-release",
      prompt:
        "A customer pays $45 at a petrol pump that pre-authorised $100. After the actual fuel amount of $45 is captured, what happens to the remaining $55?",
      options: [
        "It is permanently forfeited to the merchant",
        "It remains held indefinitely until the bank manually releases it",
        "The $55 hold is released, restoring that amount to available balance",
        "A second capture is automatically raised for $55",
      ],
      answer: 2,
      explanation:
        "When the captured amount ($45) is less than the authorised amount ($100), the difference ($55) is a [[hold-release]] — the excess hold is released, restoring $55 to the cardholder's available balance.",
      explore: { label: "View payments", href: "/payments" },
    },
    {
      kind: "multi",
      id: "ch13-q5",
      difficulty: "core",
      concept: "holds",
      prompt:
        "Select ALL scenarios where the authorized amount may differ from the captured amount.",
      options: [
        "A petrol pump pre-authorises $100 but the final fill costs $45",
        "A restaurant authorises the meal cost but the customer adds a tip at signing",
        "A hotel pre-authorises a large security deposit but charges only the room rate",
        "A grocery store with a fixed price item",
        "A partial capture where only some ordered items are fulfilled",
      ],
      answers: [0, 1, 2, 4],
      explanation:
        "[[holds]] are often for estimated amounts. Variable final amounts occur at petrol pumps (fuel quantity unknown), restaurants (tip unknown), hotels (damage deposits), and partial fulfilments. A fixed-price grocery item is typically captured for exactly the authorised amount.",
    },
    {
      kind: "mc",
      id: "ch13-q6",
      difficulty: "core",
      concept: "scheme-direction-pull",
      prompt:
        "Card payments use a four-party model. Which parties are involved?",
      options: [
        "Cardholder, issuer, central bank, card network",
        "Cardholder, issuer, merchant, acquirer — coordinated by the card network",
        "Merchant, acquirer, card network, central bank",
        "Cardholder, merchant, central bank, clearing house",
      ],
      answer: 1,
      explanation:
        "The four parties are: **cardholder** (payer), **issuer** (cardholder's bank), **merchant** (payee), and **acquirer** (merchant's bank). The [[scheme-direction-pull]] card network routes authorization and clearing messages between them.",
    },
    {
      kind: "truefalse",
      id: "ch13-q7",
      difficulty: "core",
      concept: "account-status",
      prompt:
        "A frozen account can still receive incoming credits even though outgoing debits are blocked.",
      answer: true,
      explanation:
        "[[account-status]] frozen blocks debit-side requests (including card authorization). It does **not** prevent incoming credits — a frozen account can still receive payments. Authorization is rejected because it would ultimately produce a debit entry.",
    },
    {
      kind: "mc",
      id: "ch13-q8",
      difficulty: "core",
      concept: "hold-capture",
      prompt:
        "After a card payment is captured, how does it proceed to final settlement?",
      options: [
        "It settles immediately via a direct central bank wire",
        "It becomes an ordinary debtor-to-creditor interbank payment, netting and settling via the same scheme machinery as any credit transfer",
        "It waits until the cardholder manually confirms the charge",
        "The card network settles directly with the issuer, bypassing clearing suspense",
      ],
      answer: 1,
      explanation:
        "After [[hold-capture]], the card payment is just like any other interbank payment: the debtor leg has been posted, and the payment nets and settles through the same clearing and settlement machinery described in the payment-schemes chapter.",
      explore: { label: "See settlement cycles", href: "/cycles" },
    },
    {
      kind: "mc",
      id: "ch13-q9",
      difficulty: "challenge",
      concept: "hold-capture",
      prompt:
        "A restaurant authorises $50 for a meal. The customer adds a $12 tip and the final capture is $62. Which statement is correct?",
      options: [
        "The capture is rejected because $62 exceeds the $50 authorization",
        "The original $50 hold is released and a fresh $62 hold is placed",
        "The capture posts $62 and releases the original $50 hold; the extra $12 was within a permitted overage",
        "The capture posts only $50; the $12 tip requires a separate authorization",
      ],
      answer: 2,
      explanation:
        "Card schemes allow captures to exceed the authorized amount by a small overage (e.g., for tips). [[hold-capture]] posts the **final amount** ($62) and the original authorization ($50) is closed. The available balance and book balance both reflect the actual $62 charge.",
    },
    {
      kind: "truefalse",
      id: "ch13-q10",
      difficulty: "challenge",
      concept: "holds",
      prompt:
        "In a single-message card transaction (e.g., PIN debit at ATM), a hold is placed first and a separate capture message follows later.",
      answer: false,
      explanation:
        "Single-message flows combine authorization and clearing in **one step** — money is gone immediately. There is no separate hold-then-capture phase. This contrasts with dual-message (hold + later presentment) used for most credit card transactions.",
    },
  ],
};
