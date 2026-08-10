import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "18-interest-overdrafts-and-arrears",
  number: 18,
  part: "Part VIII · Lending",
  title: "Interest, Overdrafts and Arrears",
  questions: [
    {
      kind: "mc",
      id: "ch18-q1",
      difficulty: "intro",
      concept: "day-count",
      prompt: "What is a day-count convention?",
      options: [
        "A rule that turns a pair of dates into a fraction of a year, used to compute interest",
        "A setting that controls how many decimal places an asset's minor unit has",
        "How often an account statement is generated",
        "A cutoff time after which a payment is booked to the next business day",
      ],
      answer: 0,
      explanation:
        "A [[day-count]] convention is a real term of the contract, not an implementation detail: the same balance at the same rate accrues a different amount under `ACT/365`, `ACT/360`, or `30/360`, because each turns the span between two dates into a different fraction of a year.",
    },
    {
      kind: "mc",
      id: "ch18-q2",
      difficulty: "core",
      concept: "day-count",
      prompt: "Which day-count convention is defined so that every calendar month is exactly a twelfth of a year?",
      options: ["ACT/365", "ACT/360", "30/360", "None — no convention has this property"],
      answer: 2,
      explanation:
        "Under [[day-count|30/360]], every month is treated as exactly 30 days and every year as 360, so a calendar month is always precisely 30/360 = a twelfth of a year — regardless of how many actual days it has.",
    },
    {
      kind: "mc",
      id: "ch18-q3",
      difficulty: "challenge",
      concept: "day-count",
      prompt: "Why does the 30/360 convention exist at all, given that ACT/365 arguably reflects reality more closely?",
      options: [
        "It makes the accrual calculation run faster",
        "Under it, a scheduled instalment's projected interest and what actually accrues over that month always agree to the cent, so a repayment never has to reconcile the two",
        "It charges customers strictly less interest overall",
        "Regulators require it for every retail lending product",
      ],
      answer: 1,
      explanation:
        "Under `ACT/365` or `ACT/360`, a 30-day month accrues less than a flat scheduled twelfth and a 31-day one accrues more — the difference a [[repayment-allocation|repayment]] has to absorb. `30/360` exists precisely to make plan and actual agree exactly, at the cost of no longer tracking real calendar days.",
    },
    {
      kind: "numeric",
      id: "ch18-q4",
      difficulty: "core",
      concept: "interest-accrual",
      prompt:
        "A €10,000 loan at 6% accrues interest for its very first day under ACT/365. Rounded to the nearest cent, how many cents does the general ledger post that day?",
      answer: 164,
      unit: { asset: "EUR", in: "minor" },
      tolerance: 0,
      explanation:
        "One day's accrual is €10,000 × 6% ÷ 365 = 164.383561 cents. Nothing has been posted before this day, so the first day's posting is the full rounded figure: **164 cents**. See [[interest-accrual]] for why later days post 164 or 165 depending on how the rounding falls.",
    },
    {
      kind: "mc",
      id: "ch18-q5",
      difficulty: "core",
      concept: "accrued-interest",
      prompt: "Why is interest tracked at a higher precision (micro-minor-units) than the ledger ever posts?",
      options: [
        "Because regulators mandate six decimal places on every interest figure",
        "Because a day's interest on a real balance is mostly fraction, and discarding that fraction daily compounds into a real annual error no bank would accept",
        "Because Bitcoin-denominated facilities need nine decimal places",
        "Because the amortization schedule is computed in floating point and needs matching precision",
      ],
      answer: 1,
      explanation:
        "€10,000 at 6% accrues 164.383561 cents a day — mostly fraction. Rounding that away every day rather than keeping it is a measurable annual error, so [[accrued-interest]] is held exact at sub-minor-unit precision and only rounded when the ledger actually needs a whole minor unit to post.",
    },
    {
      kind: "mc",
      id: "ch18-q8",
      difficulty: "core",
      concept: "capitalization",
      prompt: "What does capitalizing a revolving line's interest into its principal make the balance do, and why?",
      options: [
        "Shrink — capitalizing subtracts interest from what is owed",
        "Compound — next period's accrual now runs on a principal that already includes this period's interest",
        "Freeze — capitalization pauses further accrual until the next statement",
        "Split — the capitalized interest moves onto a line of its own",
      ],
      answer: 1,
      explanation:
        "[[capitalization]] folds the accrued receivable into the drawn principal rather than collecting it separately. Because the next period's [[interest-accrual|accrual]] runs on that now-larger principal, the balance compounds — interest earning interest.",
    },
    {
      kind: "numeric",
      id: "ch18-q9",
      difficulty: "challenge",
      concept: "capitalization",
      prompt:
        "A revolving line's exact 30-day accrual is 1,479.452040 minor units, and its billing cycle capitalizes the rounded 1,479 into principal. State the residue left on the accrued-interest record afterward, in micro-minor-units, as a signed number (positive if the record is left owed a little more, negative if it is left owed a little less).",
      answer: 452040,
      tolerance: 0,
      explanation:
        "1,479.452040 rounds DOWN to 1,479 — the customer is charged 0.452040 minor units LESS than actually accrued — so the record is left **+452,040** micro-minor-units, still owed. That is positive, not negative: capitalizing always leaves a residue of up to half a minor unit either way, and here the rounding fell down. A residue this size — below half a minor unit — rounds to 0, so the ledger and the record stay in step — it would only round away from zero, to ±1, at an EXACT half.",
    },
    {
      kind: "truefalse",
      id: "ch18-q10",
      difficulty: "core",
      concept: "capitalization",
      prompt: "Charging interest on a term loan folds it back into principal, the same way a revolving line's billing cycle does.",
      answer: false,
      explanation:
        "A term loan settles interest through its own scheduled instalments — [[capitalization|capitalizing]] it into principal would silently re-amortize a signed contract, so attempting it is refused outright. Only a revolving line's interest is capitalized this way.",
    },
    {
      kind: "multi",
      id: "ch18-q11",
      difficulty: "core",
      concept: "repayment-allocation",
      prompt: "Which of the following are true about how a repayment is allocated? Select all that apply.",
      options: [
        "Interest is settled before principal",
        "The interest a repayment settles is whatever the schedule projects for that instalment, not what actually accrued",
        "Under 30/360, the scheduled interest and the actual accrual always agree to the cent",
        "Any shortfall between scheduled and accrued interest is silently absorbed by the principal portion of the payment",
        "Paying more than everything owed on a facility is accepted as an early payoff with change returned",
      ],
      answers: [0, 2, 3],
      explanation:
        "[[repayment-allocation|Allocation]] settles what actually ACCRUED, not the schedule's projection — the opposite of option two. Under `30/360` the two happen to always agree, which is exactly why that convention exists. Overpaying what is owed is refused rather than accepted: it is a refund, not a repayment.",
    },
    {
      kind: "numeric",
      id: "ch18-q12",
      difficulty: "challenge",
      concept: "repayment-allocation",
      prompt:
        "A €10,000 loan at 6% accrues interest under ACT/365 for exactly 30 days. Rounded to the cent, how much interest has actually accrued, in cents?",
      answer: 4932,
      unit: { asset: "EUR", in: "minor" },
      tolerance: 0,
      explanation:
        "30 days of ACT/365 accrual on €10,000 at 6% is 4,931.506849 cents exactly, which rounds to **4,932 cents (€49.32)** — not the €50.00 a flat scheduled twelfth would suggest.",
    },
    {
      kind: "mc",
      id: "ch18-q13",
      difficulty: "core",
      concept: "repayment-allocation",
      prompt:
        "A term loan's schedule projects €50.00 of interest for the first month; the ACT/365 accrual actually earned by the due date is €49.32. What absorbs that 68-cent gap when the scheduled €193.33 instalment is paid?",
      options: [
        "The interest-income account, which recognizes the extra 68 cents anyway",
        "The principal portion of the payment, which is credited 68 cents more than the schedule itself assumed",
        "The customer, who is billed an extra 68-cent fee",
        "Nothing — the repayment is rejected until the schedule is corrected",
      ],
      answer: 1,
      explanation:
        "The receivable is credited exactly what accrued (€49.32); the rest of the payment — €144.01, not the schedule's own €143.33 — goes to principal. [[repayment-allocation|Repayment allocation]] settles interest before principal by amount actually earned, and the principal portion absorbs whatever the schedule got wrong.",
    },
    {
      kind: "mc",
      id: "ch18-q14",
      difficulty: "intro",
      concept: "days-past-due",
      prompt: "How are a facility's days past due measured?",
      options: [
        "From the date the facility was opened",
        "As the calendar-day age of the OLDEST instalment that is still due and unpaid",
        "As the number of business days an account has been overdrawn",
        "From the most recent unpaid instalment, regardless of older ones",
      ],
      answer: 1,
      explanation:
        "[[days-past-due]] always measures from the oldest still-unpaid, still-due instalment — always in real calendar days, whatever [[day-count|day-count convention]] the facility accrues interest under, because delinquency is a fact about the calendar, not about accrual.",
    },
    {
      kind: "truefalse",
      id: "ch18-q15",
      difficulty: "core",
      concept: "days-past-due",
      prompt:
        "Paying the newest overdue instalment while an older one remains unpaid resets a facility's days-past-due clock to zero.",
      answer: false,
      explanation:
        "The clock is anchored to the OLDEST unpaid, overdue instalment. Satisfying a newer one does nothing to that anchor if an older one is still open — a borrower permanently one instalment behind stays visibly one instalment behind, which is exactly [[days-past-due|the point]].",
    },
    {
      kind: "multi",
      id: "ch18-q16",
      difficulty: "core",
      concept: "non-performing",
      prompt: "Which of the following are true about the non-performing marker? Select all that apply.",
      options: [
        "It is set once days past due reaches 90",
        "Marking a facility non-performing stops its interest from accruing into income",
        "The flag changes no accounting at all — accrual, posting and the schedule are all unaffected",
        "Non-accrual accounting and expected-credit-loss provisioning are recorded as deferred future work, not implemented here",
        "A facility can be non-performing while its arrears bucket still reads Current",
      ],
      answers: [0, 2, 3],
      explanation:
        "[[non-performing|Non-performing]] is set at 90+ days and marks ONLY — it changes nothing about accrual, posting, or the schedule; non-accrual accounting and ECL provisioning are explicitly deferred. It cannot coexist with a Current bucket: both are set from the same days-past-due figure, and 90+ days is its own top bucket.",
    },
    {
      kind: "numeric",
      id: "ch18-q17",
      difficulty: "intro",
      concept: "non-performing",
      prompt: "At how many days past due is a facility marked non-performing?",
      answer: 90,
      tolerance: 0,
      explanation:
        "90 days past due is the threshold at which [[non-performing]] is set — the boundary of the `90+` [[days-past-due|arrears bucket]], and essentially every regulatory and management report's own cutoff.",
    },
    {
      kind: "mc",
      id: "ch18-q18",
      difficulty: "challenge",
      concept: "overdraft",
      prompt:
        "An overdrawn current account needs to appear as a receivable — an Asset — somewhere in the bank's reporting. How does this system actually produce that figure?",
      options: [
        "A nightly sweep posts a reclassification transaction moving the drawn amount into a Loans Asset account",
        "A credit facility is opened automatically the instant the account goes negative",
        "It is computed by aggregating Σ max(0, −balance) across every deposit account, on demand — nothing is ever posted for it",
        "The account's overdraft limit itself is booked directly as the Asset-side figure",
      ],
      answer: 2,
      explanation:
        "Nothing is posted, ever, to an Asset account for the DRAWN AMOUNT — not even for an account with no rate set at all. The Asset-side total is a derived aggregate, the same on-demand shape that already produces \"total customer deposits\". The interest on that drawn amount does post to an Asset line, daily — the bank's [[accrued-interest|accrued-interest receivable]], under this account — but that is interest earned, not the balance reclassified.",
    },
    {
      kind: "truefalse",
      id: "ch18-q19",
      difficulty: "challenge",
      concept: "overdraft",
      prompt:
        "An overdrawn account gets its own [[credit-facility|credit facility]] record, the same way a term loan or revolving line does, so its drawn amount can be tracked independently.",
      answer: false,
      explanation:
        "It deliberately does not. The drawn amount IS the customer's own negative balance in the bank's [[ledger-vs-subledger|customer-deposit line]], viewed by sign — not a second fact that happens to agree with it. A facility row for it would store a number that already exists, which is exactly the duplication a unified ledger is built without.",
    },
    {
      kind: "mc",
      id: "ch18-q20",
      difficulty: "core",
      concept: "unarranged-rate",
      prompt: "An account is drawn €700 against a €500 arranged overdraft limit. At what rate does interest accrue on the €200 beyond the limit?",
      options: [
        "It doesn't accrue interest at all — only the arranged €500 does",
        "The same arranged rate as the rest of the balance",
        "The unarranged rate — a separate, higher rate that applies specifically to the amount beyond the limit",
        "The account is frozen instead of accruing further interest",
      ],
      answer: 2,
      explanation:
        "The [[unarranged-rate]] exists specifically so that exceeding the limit is never cheaper than staying inside it: the arranged rate applies up to the limit, and the higher unarranged rate applies to whatever sits beyond it — here, the €200 excess. An unarranged rate is a SURCHARGE, so an account that has none falls back to the arranged rate on the excess. The one thing that never happens is the first option: the money beyond the limit is never free.",
    },
    {
      kind: "truefalse",
      id: "ch18-q21",
      difficulty: "core",
      concept: "effective-dated-terms",
      prompt:
        "A posting that arrives value-dated to a day BEFORE the account's last repricing is trued up by the next accrual run, the same way any other back-dated posting is.",
      answer: true,
      explanation:
        "It is — and it is true *because* the terms are [[effective-dated-terms|effective-dated]]. Each run re-derives every day since the account opened, pricing each one at the terms actually in force on it, and posts the change in the rounded value as a delta. While terms were mutable columns the recompute window had to start at the last repricing — reaching further would have re-derived old days at *today's* rate — so a back-value landing behind that line was trued up only from the repricing forward, and the days between where it took effect and the repricing kept the interest computed without it, permanently. The repricing was a line the correction stopped at.",
    },
    {
      kind: "numeric",
      id: "ch18-q22",
      difficulty: "challenge",
      concept: "interest-accrual",
      prompt:
        "An account is overdrawn by a constant €2,000 — well inside its arranged limit, with no movements in the window — and accrues under ACT/365. The arranged rate is 15% for the first 20 days and is repriced to 18% for the next 25. How much does the account accrue over the whole 45 days, in micro-minor-units (minor units × 1,000,000)?",
      answer: 4109589000,
      tolerance: 0,
      explanation:
        "Accrual is per DAY and per terms row, never one rate applied across the window. One day at 15% is 200,000 minor units × 0.15 ÷ 365 = 82.19178082 minor units — 82,191,780.82 micro-minor-units — and the division truncates to **82,191,780**. One day at 18% is 98,630,136.98 → **98,630,136**. So 20 × 82,191,780 + 25 × 98,630,136 = 1,643,835,600 + 2,465,753,400 = **4,109,589,000**. Computing each rate's block in one call instead gives 1,643,835,616 + 2,465,753,424 = 4,109,589,040 — 40 more, because the truncation then happens once per call rather than once per day, and [[interest-accrual|the accrual walks the days]] to reproduce exactly what was charged day by day.",
    },
    {
      kind: "mc",
      id: "ch18-q23",
      difficulty: "core",
      concept: "pricing-overlay",
      prompt:
        "A customer negotiated 9% on their overdraft last year. The bank now publishes a new version of their product at 14.9%. What does that customer pay?",
      options: [
        "9%, until the negotiated rate is explicitly cleared",
        "14.9%, because a published version overrides everything",
        "The higher of the two, 14.9%",
        "9% until the end of the current billing cycle, then 14.9%",
      ],
      answer: 0,
      explanation:
        "A [[pricing-overlay|negotiated rate]] is this customer's price instead of the product's, and it outranks a reprice published underneath it. Clearing it puts them back on the product at whatever it costs BY THEN — not at what it cost when the overlay was set. Nothing expires on its own: an overlay ends when a row says it ends.",
    },
    {
      kind: "truefalse",
      id: "ch18-q24",
      difficulty: "challenge",
      concept: "product-catalogue",
      prompt:
        "A rate was published in error last month. The bank corrects it by publishing a replacement version effective from the wrong rate's start date.",
      answer: false,
      explanation:
        "It cannot: publication is [[product-catalogue|forward-only]]. A backdated version would move interest already charged on every account bound to the product at once, with the audit log as the only control — and this system has no four-eyes check to sit in front of it. The retroactive tool is the per-account [[pricing-overlay|overlay]], whose blast radius is one named customer and whose delta the [[interest-accrual|accrual]] posts as ordinary correction interest. Correcting a mispublished rate is therefore laborious, and should be: it is a set of individual decisions about money already taken from named people.",
    },
  ],
};
