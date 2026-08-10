import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "17-lending-and-amortization",
  number: 17,
  part: "Part VIII · Lending",
  title: "Lending and Amortization",
  questions: [
    {
      kind: "mc",
      id: "ch17-q1",
      difficulty: "intro",
      concept: "term-loan",
      prompt: "Which best describes a term loan's disbursed principal?",
      options: [
        "A fixed amount paid out once and repaid on a schedule generated at disbursement",
        "A rolling limit the customer can draw against repeatedly",
        "A figure computed from the customer's negative deposit balance",
        "An amount that is regenerated fresh every billing cycle",
      ],
      answer: 0,
      explanation:
        "A [[term-loan]] pays out a fixed principal once, and its [[amortization|schedule]] is generated at that moment. Compare the [[revolving-line]], which is drawn and repaid repeatedly against a reusable commitment rather than paid out once.",
    },
    {
      kind: "mc",
      id: "ch17-q2",
      difficulty: "intro",
      concept: "revolving-line",
      prompt: "What distinguishes a revolving line from a term loan?",
      options: [
        "It has a fixed principal disbursed once, like a term loan",
        "It is a reusable commitment that can be drawn and repaid repeatedly, billed in cycles rather than against a fixed schedule",
        "It cannot accrue interest",
        "Its principal exists only as another account's balance viewed by sign",
      ],
      answer: 1,
      explanation:
        "A [[revolving-line]] is drawn and repaid repeatedly as long as drawn principal stays within its commitment, billed each cycle rather than against an up-front schedule. The last option describes an [[overdraft|arranged overdraft]], not a facility at all.",
    },
    {
      kind: "truefalse",
      id: "ch17-q3",
      difficulty: "core",
      concept: "credit-facility",
      prompt:
        "The two Asset lines a facility posts to — drawn principal and accrued interest receivable — could be merged into one without losing any information, since both are owed by the same borrower.",
      answer: false,
      explanation:
        "A [[repayment-allocation|repayment]] settles interest before principal, and one line cannot express a split between two things it does not distinguish. The two exist precisely so that split has somewhere to be recorded — and note that they are the bank's lines, shared by every borrower, with the [[credit-facility|facility named on each entry]] rather than an account pair opened per loan.",
    },
    {
      kind: "mc",
      id: "ch17-q4",
      difficulty: "core",
      concept: "credit-facility",
      prompt: "Why does lending post to two separate Asset lines rather than one?",
      options: [
        "Because the ledger requires every entity to have at least two accounts",
        "Because a repayment allocates against interest before principal, and one line cannot express that split",
        "Because principal and interest are always denominated in different assets",
        "Because regulators require two account numbers per loan",
      ],
      answer: 1,
      explanation:
        "The split is [[credit-facility|drawn principal]] (what is owed on money taken) and accrued interest receivable (interest earned, not yet collected). [[repayment-allocation|Repayment]] credits the receivable first and only then the principal — impossible if the two shared a line. Both are control accounts of the bank's, so a second borrower adds entries to them rather than accounts beside them.",
    },
    {
      kind: "mc",
      id: "ch17-q5",
      difficulty: "intro",
      concept: "account-type-asset",
      prompt: "Disbursing a €1,000,000 term loan into the customer's deposit account debits which line of the chart of accounts?",
      options: [
        "The bank's customer-deposit line, under this customer",
        "The bank's Interest Income (Revenue) line",
        "The bank's loan-principal (Asset) line, under this facility",
        "The central bank reserve account",
      ],
      answer: 2,
      explanation:
        "Disbursement debits the bank's loan-principal [[account-type-asset|Asset]] line with the facility named on the entry, and credits the counterparty — typically the customer's Liability deposit position, whose balance rises. Both sides balance under [[double-entry]]; nothing is owed on the facility before this posting, and the whole committed amount is after it.",
    },
    {
      kind: "numeric",
      id: "ch17-q6",
      difficulty: "core",
      concept: "amortization",
      prompt:
        "A €12,000 term loan is disbursed over 12 monthly instalments under equal-principal amortization. Ignoring interest, what is the flat principal portion of each of the first 11 instalments, in euros?",
      answer: 1000,
      unit: { asset: "EUR", in: "major" },
      tolerance: 0,
      explanation:
        "Equal-principal splits the disbursed principal evenly across the term: €12,000 ÷ 12 = **€1,000** a month. See [[equal-principal]] for why the very last instalment is computed differently rather than following this flat share.",
    },
    {
      kind: "mc",
      id: "ch17-q7",
      difficulty: "core",
      concept: "amortization",
      prompt: "Why is a term loan's very last instalment computed differently from all the others?",
      options: [
        "It always waives whatever interest remains",
        "It repays whatever principal is left, so the schedule's principal sums to the disbursed principal exactly, however rounding fell along the way",
        "It is always double the regular instalment's principal",
        "It resets the loan's day-count convention",
      ],
      answer: 1,
      explanation:
        "Every other row's principal comes from the [[annuity|annuity]] or [[equal-principal|equal-principal]] formula and can round; the last row instead repays the entire remaining balance, which is what makes the [[amortization|schedule]] sum to the disbursed principal exactly rather than leaving a stray cent unaccounted for.",
    },
    {
      kind: "mc",
      id: "ch17-q8",
      difficulty: "intro",
      concept: "annuity",
      prompt:
        "Under annuity amortization, how does the split between principal and interest change over the life of the loan?",
      options: [
        "It never changes — every instalment splits principal and interest identically",
        "The interest share falls and the principal share rises as the balance amortizes, while the total payment stays level",
        "The principal share falls and the interest share rises",
        "Only interest is paid until the final instalment, which repays all principal at once",
      ],
      answer: 1,
      explanation:
        "[[annuity]] amortization keeps the total instalment level. Since interest is charged on a falling outstanding balance, the interest share of that fixed total falls too, and the principal share rises to make up the difference — the shape of most retail mortgages.",
    },
    {
      kind: "mc",
      id: "ch17-q9",
      difficulty: "core",
      concept: "equal-principal",
      prompt: "Under equal-principal amortization, what happens to the total (principal + interest) payment across the term?",
      options: [
        "It stays level throughout, like an annuity",
        "It shrinks each instalment, because interest is charged on a balance that falls by the same fixed principal amount each time",
        "It grows each instalment as the balance compounds",
        "It is undefined until the loan matures",
      ],
      answer: 1,
      explanation:
        "[[equal-principal]] amortization repays the same slice of principal every instalment, so the outstanding balance — and therefore the interest charged on it — shrinks steadily, and the total payment shrinks with it. This is the mirror image of [[annuity|annuity]] amortization's level total.",
    },
    {
      kind: "truefalse",
      id: "ch17-q10",
      difficulty: "intro",
      concept: "annuity",
      prompt: "Under annuity amortization, every instalment repays the same amount of principal.",
      answer: false,
      explanation:
        "It is the TOTAL instalment that stays level under [[annuity]] amortization, not the principal share within it — that share rises over the term as the interest share falls. A level principal share is what [[equal-principal|equal-principal]] amortization does instead.",
    },
    {
      kind: "multi",
      id: "ch17-q11",
      difficulty: "core",
      concept: "amortization",
      prompt: "Which of the following are true of a term loan's amortization schedule? Select all that apply.",
      options: [
        "It is generated once, at disbursement",
        "It is a plan the facility is compared against, and a repayment may allocate slightly differently from what it projects",
        "It is regenerated every time interest accrues",
        "Its scheduled principal sums exactly to the disbursed principal",
        "A revolving line also gets one, generated at opening",
      ],
      answers: [0, 1, 3],
      explanation:
        "The [[amortization|schedule]] is built once, at disbursement, as a plan — not regenerated on every accrual. It is a PLAN the facility is checked against, and its principal column sums exactly to what was disbursed. A revolving line gets no up-front schedule at all: it has nothing to build one from until it is drawn.",
    },
    {
      kind: "mc",
      id: "ch17-q12",
      difficulty: "challenge",
      concept: "revolving-line",
      prompt: "A revolving line has no schedule generated at opening. Where do its instalments come from instead?",
      options: [
        "They are backfilled once the line is closed",
        "One instalment is appended per billing cycle, when interest is capitalized and a minimum payment is computed",
        "They are copied from a template term loan of the same size",
        "A revolving line never has instalments",
      ],
      answer: 1,
      explanation:
        "Each billing cycle capitalizes the interest accrued so far into principal and appends one instalment: that cycle's interest, plus a minimum-payment share of the newly larger drawn balance. That is how a revolving line actually falls into [[arrears]] — by missing a minimum payment, not a fixed instalment.",
    },
    {
      kind: "numeric",
      id: "ch17-q13",
      difficulty: "intro",
      concept: "derived-balance",
      prompt:
        "The bank's loan-principal line has a book balance of €45,000 under a given facility, and its accrued-interest receivable has €120 under that same facility. What does the facility's drawn principal report, in euros?",
      answer: 45000,
      unit: { asset: "EUR", in: "major" },
      tolerance: 0,
      explanation:
        "Drawn principal is read from the principal line alone, filtered to this facility — **€45,000** — never combined with the accrued-interest receivable, which is a separate figure on a separate line. Both are [[derived-balance|the same sum with the obligor in the filter]]; neither is a column anywhere.",
    },
    {
      kind: "mc",
      id: "ch17-q14",
      difficulty: "core",
      concept: "derived-balance",
      prompt: "Where is a facility's drawn amount stored?",
      options: [
        "In a `drawn` column on the facility's own row",
        "It isn't stored anywhere — it is the loan-principal line's book balance under this facility, read whenever it is asked for",
        "In a cache refreshed once a night",
        "On the customer's deposit account",
      ],
      answer: 1,
      explanation:
        "Like every [[derived-balance|derived balance]] in this system, the drawn amount is not stored: it is computed on demand by summing the loan-principal line's entries that name this facility, the same discipline a plain book balance follows.",
    },
    {
      kind: "truefalse",
      id: "ch17-q15",
      difficulty: "challenge",
      concept: "credit-facility",
      prompt:
        "A facility's **commitment** is derived the same way its drawn amount and accrued interest are — read from a ledger balance rather than stored.",
      answer: false,
      explanation:
        "The **commitment** — a term loan's original principal, or a revolving line's limit — IS stored: it is a fact about the contract, not a fact about postings so far. The **drawn** amount is the [[derived-balance|derived]] one, read from the loan-principal line under this facility. The **accrued interest** is stored, but as an exact sub-minor-unit [[accrued-interest|record]] whose rounded figure the facility's balance in the receivable always equals — so the two agree to the cent either way.",
    },
    {
      kind: "mc",
      id: "ch17-q16",
      difficulty: "intro",
      concept: "lending",
      prompt: "Which of the following is NOT one of this system's two `lending`-package products?",
      options: ["Term loan", "Revolving line", "Arranged overdraft", "None of the above — all three live in the lending package"],
      answer: 2,
      explanation:
        "[[lending]] ships the [[term-loan|term loan]] and the [[revolving-line|revolving line]]. The [[overdraft|arranged overdraft]] is a third form of credit but deliberately lives in `deposit`, not `lending` — its drawn amount is a current account's own negative balance, not an independent fact.",
    },
    {
      kind: "truefalse",
      id: "ch17-q17",
      difficulty: "challenge",
      concept: "term-loan",
      prompt: "A term loan's amortization schedule is generated the moment it is opened, before any money is disbursed.",
      answer: false,
      explanation:
        "Opening a [[term-loan]] only records it as Pending — it does not move money or build a schedule. The schedule needs a first due date, which is not known until disbursement fixes when the money actually went out; a schedule generated at opening would be a plan to repay money never paid out.",
    },
    {
      kind: "numeric",
      id: "ch17-q18",
      difficulty: "core",
      concept: "equal-principal",
      prompt:
        "A €120,000 loan amortizes under equal-principal over 120 monthly instalments. Ignoring the rounding the final row absorbs, what is the flat monthly principal portion, in euros?",
      answer: 1000,
      unit: { asset: "EUR", in: "major" },
      tolerance: 0,
      explanation:
        "€120,000 ÷ 120 months = **€1,000** a month under [[equal-principal]] amortization — level principal, falling total payment as the interest on a shrinking balance falls with it.",
    },
    {
      kind: "multi",
      id: "ch17-q19",
      difficulty: "core",
      concept: "revolving-line",
      prompt: "Which of the following are true of a revolving line? Select all that apply.",
      options: [
        "It has a maturity date fixed at opening, like a term loan",
        "It can be drawn again after being partly repaid, as long as drawn principal stays within the commitment",
        "Its billing cycle capitalizes accrued interest into principal, which is what makes it compound",
        "Drawing beyond the commitment succeeds but marks the facility non-performing",
        "It accrues no interest until its first billing cycle is charged",
      ],
      answers: [1, 2],
      explanation:
        "A [[revolving-line]] has no maturity date — it is open-ended. It CAN be redrawn within its commitment, and its cycle [[capitalization|capitalizes]] interest into principal, which is what makes it compound. Drawing beyond the commitment is refused outright, not silently allowed; and interest [[interest-accrual|accrues daily]] from the first draw, independently of when it is next charged.",
    },
    {
      kind: "mc",
      id: "ch17-q20",
      difficulty: "intro",
      concept: "term-loan",
      prompt: "A bank has lent in euro before. What happens to its chart of accounts when it opens another euro term loan, before that loan is disbursed?",
      options: [
        "Two Asset accounts are created for this loan, both at a zero balance",
        "Nothing is added: the euro loan-principal and receivable lines already exist, and no entry yet names this facility",
        "Two accounts are created, already holding the committed amount",
        "One account is created, covering principal and interest together",
      ],
      answer: 1,
      explanation:
        "A [[term-loan|facility]] is an obligor under lines the bank already has — the *first* euro facility opened them, and every one after it adds entries rather than rows. Its drawn principal reads zero not because an account was opened at zero but because nothing has been posted under its id. Nothing is owed and nothing has accrued until [[account-type-asset|Disburse]] posts the first real transaction naming it.",
    },
  ],
};
