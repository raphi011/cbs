import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "16-multi-asset-accounting",
  number: 16,
  part: "Part VII · More Than One Kind of Money",
  title: "Multi-Asset Accounting",
  questions: [
    {
      kind: "mc",
      id: "ch16-q1",
      difficulty: "intro",
      concept: "asset",
      prompt:
        "What does it mean that a general-ledger account is 'denominated in' an asset?",
      options: [
        "The account is bound to exactly one asset, chosen when it is created and never changed",
        "The account can hold several assets and tracks a separate balance for each",
        "The asset is chosen per transaction, so one account can be posted to in euro today and bitcoin tomorrow",
        "The asset is a display preference; internally every account holds the same neutral unit",
      ],
      answer: 0,
      explanation:
        "An [[asset|account is denominated in exactly one asset]], fixed at creation. This is how real core banking works — an account number and its currency are inseparable, which is why IBANs are per-currency. It is also what keeps a balance a *scalar*: if one account could hold several assets, every balance, statement line and snapshot would have to carry a map instead of a number.",
    },
    {
      kind: "truefalse",
      id: "ch16-q2",
      difficulty: "intro",
      concept: "asset",
      prompt:
        "A euro deposit account and a bitcoin deposit account are different account *types*.",
      answer: false,
      explanation:
        "Both are **Liability** accounts — the bank owes the customer, whichever kind of money it owes. An account's [[normal-balance|type]] says whether it is something the bank owns or owes; its [[asset]] says what it is counted in. The two dimensions are independent, which is exactly why the Go type is named `AssetDef`: `Asset` was already taken by the [[account-type-asset|account-type constant]].",
    },
    {
      kind: "mc",
      id: "ch16-q3",
      difficulty: "intro",
      concept: "per-asset-balance",
      prompt:
        "How is the double-entry rule stated once a book holds more than one asset?",
      options: [
        "Total debits must equal total credits across the whole transaction",
        "Debits must equal credits within each asset",
        "Total debits must equal total credits after converting everything to a base currency",
        "Each account must end the transaction with a non-negative balance",
      ],
      answer: 1,
      explanation:
        "[[per-asset-balance|Debits must equal credits within each asset]]. The global version is not a weaker rule, it is a broken one — and note what the third option would require: a conversion, therefore a rate, therefore a price sitting inside an accounting engine. Per asset, there is no rate to get wrong.",
    },
    {
      kind: "truefalse",
      id: "ch16-q4",
      difficulty: "intro",
      concept: "asset-scale",
      prompt:
        "The integer 100000000 stored as an amount means the same quantity of money whichever asset it belongs to.",
      answer: false,
      explanation:
        "It is one bitcoin (BTC has [[asset-scale|scale]] 8, so the unit is the satoshi) and it is also a million euro (EUR has scale 2, so the unit is the cent). Nothing can be rendered — or compared — without knowing its asset, which is one more reason the asset lives on the account rather than being passed around beside the number.",
    },
    {
      kind: "multi",
      id: "ch16-q5",
      difficulty: "intro",
      concept: "participant-assets",
      prompt:
        "A participant bank clears both a euro scheme and a dollar scheme. Which of the following are true? (Select all that apply.)",
      options: [
        "It holds two clearing-suspense accounts, one per asset",
        "It holds two reserve accounts at the central bank, one per asset",
        "It holds one suspense account that tracks euro and dollars separately inside it",
        "Its euro net position and its dollar net position are netted against each other before settlement",
      ],
      answers: [0, 1],
      explanation:
        "[[participant-assets|Internal accounts exist once per asset]] the bank operates in. Partly because [[asset|an account is bound to a single asset]], and partly because [[net-positions|netting]] a euro position against a dollar one does not give a smaller number — it gives a meaningless one. There is no rate in the system to net them with, and there should not be.",
      explore: { href: "/central-bank", label: "Central-bank reserves" },
    },
    {
      kind: "mc",
      id: "ch16-q6",
      difficulty: "core",
      concept: "per-asset-balance",
      prompt:
        "An amount is an integer in its asset's minor units. A transaction debits 10,000,000,000 from a EUR cash account (€100,000,000) and credits 10,000,000,000 to a customer's BTC account (100 BTC). Under a *global* debits-equals-credits check, what happens?",
      options: [
        "It is rejected, because the two legs are denominated in different assets",
        "It is rejected, because €100,000,000 and 100 BTC are not worth the same",
        "It posts: the integers are equal, so the check is satisfied, and roughly €93 million appears from nothing",
        "It posts, but is flagged for manual review because it crosses assets",
      ],
      answer: 2,
      explanation:
        "It **posts**. A global check sums amounts and has no idea which asset each belongs to, so it is satisfied whenever the *integers* match — and here they do. The bank has booked €100,000,000 of cash against an obligation of 100 BTC, worth roughly €6.5 million, so about **€93 million appears out of nothing**. Note what did the damage: not that the legs differ in value — the check never looks at value and has no rate with which to — but that equal integers in assets whose [[asset-scale|scales]] differ by a factor of a million are not equal amounts. Which is precisely why the rule has to be [[per-asset-balance|stated per asset]].",
    },
    {
      kind: "numeric",
      id: "ch16-q7",
      difficulty: "core",
      concept: "per-asset-balance",
      prompt:
        "A transaction has five legs: debit Alice EUR 7500, credit Bob EUR 5000, credit Fee Income EUR ?, debit Custody BTC 200000, credit Customer BTC 200000. What must the fee credit be, in cents, for the transaction to post?",
      answer: 2500,
      unit: { asset: "EUR", in: "minor" },
      tolerance: 0,
      explanation:
        "**2500.** The EUR legs must net to zero on their own: 7500 − 5000 = 2500 left to credit. The BTC legs already net to zero on their own, and they neither help nor hinder the euro side — [[per-asset-balance|each asset is checked separately]]. Note that no amount of bitcoin could ever fix a euro imbalance, which is the whole point.",
    },
    {
      kind: "mc",
      id: "ch16-q8",
      difficulty: "core",
      concept: "asset-scale",
      prompt: "Why is an asset's scale capped at 9 decimal places?",
      options: [
        "Because no real-world asset has more than 9 decimal places",
        "Because an amount is an int64, and at 18 decimal places it would hold only about 9 whole units",
        "Because Postgres `BIGINT` cannot store more than 9 significant digits",
        "Because floating-point arithmetic loses precision beyond 9 decimal places",
      ],
      answer: 1,
      explanation:
        "The cap is arithmetic. An [[asset-scale|amount is an `int64`]], topping out near 9.22 × 10¹⁸ — which at 18 decimals is 9.2 whole units. Ether and most ERC-20 tokens *do* use 18 decimals, so the first option is simply false; the constraint is the width of the integer, not the world. And the whole point of integers here is that there is no [[minor-units|floating-point arithmetic]] anywhere to lose precision.",
    },
    {
      kind: "numeric",
      id: "ch16-q9",
      difficulty: "core",
      concept: "asset-scale",
      prompt:
        "A signed 64-bit integer holds at most about 9.22 × 10¹⁸. If an asset had a scale of 18 (wei, ether's native precision), how many whole units could an amount hold? Round down.",
      answer: 9,
      tolerance: 0,
      explanation:
        "**9.** Not 9 billion, not 9 million — nine. 9.22 × 10¹⁸ divided by 10¹⁸ is 9.22. Native 18-decimal precision and an `int64` amount are [[asset-scale|mutually exclusive]], and the only real decision is which of the two to give up. This system keeps the `int64` and caps scale at 9. Because the assets are a list in code, the cap is a test over that list: an entry that would overflow fails the build rather than posting quietly and wrongly.",
    },
    {
      kind: "mc",
      id: "ch16-q10",
      difficulty: "core",
      concept: "fx-position-account",
      prompt:
        "Given that transactions balance per asset, how would a foreign-exchange trade — a customer selling €100 for bitcoin — be recorded?",
      options: [
        "As one transaction with a euro leg and a bitcoin leg, which the per-asset rule allows because the two legs are of equal value",
        "As one transaction, with the exchange rate stored on it so the two legs can be reconciled",
        "Each asset balances against a position account of its own asset, so the trade is two ordinary postings",
        "In a special FX ledger that is exempt from the balancing rule",
      ],
      answer: 2,
      explanation:
        "The obvious posting is exactly the one the invariant rejects. Instead [[fx-position-account|each asset balances through a position account]] of its own asset: the euro side debits the customer and credits EUR Position; the bitcoin side debits BTC Position and credits the customer. Both are ordinary, individually balanced postings. (FX is **not implemented** here — this is the shape the ledger was designed to accommodate.)",
    },
    {
      kind: "truefalse",
      id: "ch16-q11",
      difficulty: "core",
      concept: "fx-position-account",
      prompt:
        "Under the position-account design, the exchange rate used for a trade is stored in the ledger.",
      answer: false,
      explanation:
        "No rate appears anywhere. The rate is expressed **entirely in the choice of the two amounts** — 10000 cents against 153000 satoshi — and that choice is made above the ledger, by whatever quoted the price. [[fx-position-account|The ledger records the consequence of a price and never decides one]], which is the single most useful property the per-asset rule buys.",
    },
    {
      kind: "mc",
      id: "ch16-q12",
      difficulty: "core",
      concept: "scheme-asset",
      prompt:
        "SEPA Credit Transfer and SEPA Direct Debit both declare their asset as EUR. Why is that not a simplification of the model?",
      options: [
        "Because euro is the system's default asset and the schemes inherit it",
        "Because SEPA is the Single Euro Payments Area — its schemes carry euro and nothing else",
        "Because the model supports only one asset per clearing cycle, and euro was chosen",
        "Because the participants in the sample network happen to be euro-area banks",
      ],
      answer: 1,
      explanation:
        "SEPA is the *Single Euro Payments Area*, and a 'SEPA dollar transfer' is not a thing that exists. A scheme in another currency is **another scheme**, with its own rulebook, cycles and settlement arrangements — which is why [[scheme-asset|`Asset()` sits on the scheme]] rather than on the payment. It is a fact about the rails, not a limit of the model.",
      explore: { href: "/clearing-house/schemes", label: "Registered schemes" },
    },
    {
      kind: "mc",
      id: "ch16-q13",
      difficulty: "core",
      concept: "scheme-asset",
      prompt:
        "Suppose `ErrAssetMismatch` did not exist, and a payment from a euro account to a bitcoin account got past initiation. What would happen to it?",
      options: [
        "Nothing would catch it — each leg balances within its own asset, so the payment settles and 3000 satoshi appear out of 3000 cents",
        "It would survive initiation and fail at settlement with `ErrUnbalancedAsset`, failing the entire clearing cycle rather than just that payment",
        "The debtor leg would be refused at initiation, because its two entries are in different assets",
        "It would settle, and the imbalance would surface afterwards as a discrepancy in the central bank's reserve accounts",
      ],
      answer: 1,
      explanation:
        "The ledger does catch it — in the worst possible place. At initiation there is only the debtor leg, *debit Alice EUR / credit Suspense EUR*, which is impeccable [[double-entry]] within one asset and contains no claim that a posting in another bank's book is its other half. The creditor leg — posted by the payee's own bank once the cycle has settled — is built from the creditor's suspense account **in the scheme's asset**, so it comes out *debit Suspense EUR / credit Bob BTC*, and that does not balance. By then the payer has been debited for hours, the reserves have moved, and the error names an unbalanced asset rather than the payment behind it. [[scheme-asset|`ErrAssetMismatch`]] turns a late, batch-wide, misattributed failure into an immediate, correctly attributed one. The general rule: an invariant is enforceable where the whole of it is visible, and cheapest where it is visible earliest.",
    },
    {
      kind: "truefalse",
      id: "ch16-q14",
      difficulty: "core",
      concept: "participant-assets",
      prompt:
        "If a member has a net position in a clearing cycle but holds no accounts in that cycle's asset, settlement falls back to the member's euro accounts.",
      answer: false,
      explanation:
        "There is deliberately **no fallback**. The member fails the whole batch before anything is posted — exactly the treatment an underfunded member gets. [[participant-assets|Defaulting to euro would settle a dollar cycle in the wrong money]], quietly, in the one place in the system where money becomes final. The asset is resolved once from the cycle's scheme and used for every member in the batch.",
      explore: { href: "/clearing-house/settlements", label: "Settlements" },
    },
    {
      kind: "multi",
      id: "ch16-q15",
      difficulty: "core",
      concept: "asset",
      prompt:
        "Which of the following are true of assets in this system? (Select all that apply.)",
      options: [
        "The known assets are a list in Go, not rows in a table — adding one is a code change",
        "An asset records a code, a name, a scale and a class (Fiat or Crypto)",
        "Creating an account in an asset the system does not know fails with an error",
        "An account's asset can be changed later if the customer switches currency",
      ],
      answers: [0, 1, 2],
      explanation:
        "[[asset|An asset definition is defined in code]], not stored: 'BTC has 8 decimal places' is a fact about the world, true in every book, and storing it per book would only make disagreement representable. It is the same call [[scheme-asset|the payment schemes make]] — a scheme is a Go type, its settlements are rows. An account's asset is fixed at creation and never changes; a customer who wants euro *and* dollars gets a second account, which is why in practice they get a second IBAN. `AssetClass` carries no behaviour at all — it exists so a UI can tell a currency from a token without pattern-matching on the code.",
    },
    {
      kind: "mc",
      id: "ch16-q16",
      difficulty: "challenge",
      concept: "double-entry",
      prompt:
        "When a transaction fails to balance in one of its assets, the error returned wraps both `ErrUnbalancedAsset` and `ErrUnbalancedTransaction`. Why both?",
      options: [
        "So that a caller can match on whichever level it cares about — 'did this balance?' or 'which asset broke?'",
        "Because Go requires a sentinel error to be wrapped at least twice to be matchable",
        "So the error message is longer and therefore more helpful in logs",
        "Because the two sentinels are returned in different situations and the caller must distinguish them",
      ],
      answer: 0,
      explanation:
        "Both match under `errors.Is`. A caller that only wants to know whether the posting balanced checks the general sentinel; one that wants to report the offending asset checks the specific one — and neither has to know the other exists. This is [[double-entry]]'s invariant restated for a multi-asset book: the general fact and the specific diagnosis are the same failure at two levels of detail, not two different failures.",
    },
    {
      kind: "mc",
      id: "ch16-q17",
      difficulty: "challenge",
      concept: "fx-position-account",
      prompt: "What does the balance of a position account actually represent?",
      options: [
        "The profit or loss the bank has made trading that asset",
        "The bank's open position in that asset — how much of it it is long or short from the trades it has done",
        "The total volume of that asset the bank has traded over its lifetime",
        "The amount of that asset the bank holds on behalf of customers",
      ],
      answer: 1,
      explanation:
        "It is the **open position**: a bank that has bought and sold the same amount is *flat*, and the account reads zero. Trading profit and loss is specifically *not* in the ledger under this scheme — it appears only when positions are revalued at a current rate, which needs a price and therefore [[fx-position-account|lives above the ledger]] alongside whatever quoted the trade. Customer holdings are the customers' own [[account-type-liability|liability accounts]], a different thing entirely. (FX is **not implemented** here — this is the shape the ledger was designed to accommodate.)",
    },
    {
      kind: "mc",
      id: "ch16-q18",
      difficulty: "challenge",
      concept: "scheme-asset",
      prompt:
        "Why is sending euro to a dollar account described as two operations rather than one?",
      options: [
        "Because it is settled in two separate clearing cycles by convention",
        "Because it is a payment plus a foreign-exchange trade — somebody quotes a rate and takes a spread on it",
        "Because the debtor leg and the creditor leg are always separate postings",
        "Because the funds must be returned and re-sent in the correct currency",
      ],
      answer: 1,
      explanation:
        "It is a payment **and** a trade, and the trade is priced. That is why a cross-border transfer arrives at a worse rate than the one on the news: the FX leg is a separate, priced transaction bundled into the payment's presentation. Modelling it needs the [[fx-position-account|full FX machinery]] — position accounts, a rate source, a spread booked to revenue — none of which exists here, so [[scheme-asset|the mismatch is refused]] instead. (The debtor and creditor legs are indeed separate postings, but that is true of every payment and has nothing to do with currency.)",
      explore: { href: "/clearing-house/payments", label: "Payments" },
    },
    {
      kind: "truefalse",
      id: "ch16-q19",
      difficulty: "challenge",
      concept: "relational-mapping",
      prompt:
        "In the relational schema the asset is stored on the `entries` table, so each leg carries the asset it was posted in.",
      answer: false,
      explanation:
        "The asset is on **`accounts`**, and deliberately not on `entries`. An entry's asset is always its account's, so a column on `entries` would store the same fact twice and create the one thing a second copy always creates: the chance that the two disagree. Posting derives it instead, and derivation is free — the accounts have already been loaded for the sufficient-balance check. There is also no `CHECK` restricting `accounts.asset` to the known codes: 'the asset must be one the system knows' is a *domain* rule, and a constraint there would make the Postgres store refuse a write the in-memory store performs — and would turn a one-line change to a Go slice into a migration. See [[relational-mapping]].",
    },
    {
      kind: "numeric",
      id: "ch16-q20",
      difficulty: "challenge",
      concept: "minor-units",
      prompt:
        "A customer wants to send 0.00021 BTC. BTC has a scale of 8. What integer amount does the API receive?",
      answer: 21000,
      tolerance: 0,
      explanation:
        "**21000** satoshi: 0.00021 × 10⁸. The conversion is the asset's scale and nothing else — [[minor-units|there is no default scale]] anywhere in the frontend, and assuming 2 is precisely the bug this codebase once had, rendering 1 BTC (100000000 satoshi) as '1,000,000.00'. Every formatter takes the [[asset]] it is rendering.",
    },
  ],
};
