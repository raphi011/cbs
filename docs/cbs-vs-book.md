# `cbs` vs. *Building a Modern Core Banking Platform: A Guide for EU-Regulated Digital Banks*

Chapters read in full: **1** (core boundary), **2** (money/claims/legal time), **3** (reference architecture), **4** (posting model), **5** (GL integration & close), **6** (parties/mandates/lifecycle), **7** (deposit products), **8** (loans), **11** (transaction processing), **12** (payments), **13** (EU clearing & settlement), **16** (statements & certificates), **17** (audit & controls), **22** (scalability/correctness), **24** (product velocity), plus **Appendix B** (canonical posting patterns) and **Appendix C** (chart of accounts).

The book is a German-flavoured EU reference (fictional "Nordwind Bank GmbH", BaFin/Bundesbank, HGB/GoBD, MaRisk, DORA, PSD2, SEPA). It is opinionated and prescriptive in exactly the areas this repo has opinions about, which makes the comparison unusually sharp.

---

## Status, as of 2026-07-30

**This is a snapshot of the comparison as it read when written, not a live
checklist.** Seven of its findings have since been closed and the body below has
deliberately *not* been rewritten around them — the argument for each change is
worth keeping even where the change landed. Read §6's numbered list against this
table, not on its own.

| §6 item | Status | Where it landed |
|---|---|---|
| 1. *Balance Types* overstates the value-dated balance | **closed** — and by implementing it, not by hedging | `0bc633e`, `103323b` |
| 2. Snapshot-as-checkpoint claim | **closed** (docs) — *End-of-Day Snapshots* now says nothing reads one back | `45243d3` |
| 3. *Statements* specifies an unbuilt algorithm | **closed** (docs) — the section now names `web/src/lib/statement.ts` and what it does *not* build: no opening figure, no closing figure, no booking-date filtering | — |
| 4. *Next Work* is stale on account status | **closed** — bullet replaced by the creditor-leg gap | `67a92ec`, `02d0e7b` |
| 5. No-`CHECK` position is a dual-store consequence | **open** — the only docs item left. *The Asset Dimension in the Schema* argues the position well but never says a single-backend production bank would be advised the opposite (deferred balance triggers, revoked `UPDATE`/`DELETE` grants) |
| 6. Trial balance | **open** | — |
| 7. Value date on `Entry` | **closed** | `ledger/types.go`, resolved in `PostTransactionTx`; on the wire in `ce54510` |
| 8. `ValueDateBalance`, and accrual reading it | **closed, and overshot** — §2.2's premise no longer holds. Both accrual paths read `Tx.ValueDatedSeries`, `interest.AccrueSeries` accrues over a balance that *moves* within the window, and both paths recompute rather than increment | `9866c9d`, `8c1b1cd`, `b9de417`, `65b00d0`, `e108ba6` |
| 9. Reject future booking dates | **open** | — |
| 10. Hold expiry sweep | **open** — `HoldStatus` still has no expired state | — |
| 11. Effective-dated terms | **closed** — §4.3's premise no longer holds. An account's overdraft limit, both its rates and its day count — and a facility's rate and day count — are rows on a per-instance timeline, one per repricing, each carrying both the day it takes effect and the day it was entered, and a repricing appends a row rather than editing what an earlier day already said. `SetOverdraftLimitTx`, `SetOverdraftPricingOverlayTx` and `SetFacilityTermsTx` append rather than overwrite, and accrual resolves the row in force on each day it prices. `TermsEffectiveFrom` is gone from both types, and the recompute window opens at inception instead | `deposit/terms.go`, `lending/terms.go`, `overdraft_terms` / `facility_terms` |
| 11a. Product catalogue | **partly closed** — a deposit account is now opened *from* a named `product` whose pricing is an effective-dated timeline of immutable published versions; the rate floats and the limit is pinned, with a per-account overlay for the negotiated exception, and the content hash is verified on read. Open **by choice**: the exclusion constraint (replaced by the day key), the resolution log (deferred to checkpointing), maker-checker (item 17), and lending's binding. See §4.3 | `product/`, `products` / `product_versions` |
| 12. Allocation-order policy | **open** | — |
| 13. Inbound suspense + return | **half-closed** — the *destination* exists (`Unclaimed Balances (<asset>)`, one per bank per asset) and `PostCreditorLegTx` diverts to it; what resolves the balance afterwards does not. See §4.5 | `payment/`, `participant_assets.unclaimed` |
| 14. Savings product | **open** — `interest` has still never run on the liability side | — |
| 15–17. Party model, thin GL, maker-checker | **open**, and still deliberate non-goals | — |

Two things the comparison got incomplete, worth recording next to it:

- **§4.5's creditor-leg finding is not a one-line fix, and the reason is
  interesting.** Settlement is all-or-nothing across every book, so refusing a
  credit inside the batch fails the whole clearing cycle for one closed
  account — every other member's positions with it. That is the failure mode
  `ErrAssetMismatch` is placed at initiation to avoid. So the branch needs a
  destination *and* something that later resolves it, and it cannot reuse
  `Clearing Suspense (<asset>)`: that account means "a payment leg in flight",
  and pooling unapplicable credits into it would make one balance answer two
  questions. See README *Next Work*.
  *Closed by sub-project 8's Task 15 (2026-08-02), and the first sentence of the
  reasoning is what stopped being true.* Settlement is no longer all-or-nothing
  across every book: the central bank posts only its own netting transaction,
  and the creditor leg is the payee's bank's own unit of work, made on a
  per-payment advice after the cut-off has settled. So one closed account fails
  one payment at one bank instead of the cycle, and refusing became affordable.
  The destination is `Unclaimed Balances (<asset>)` — a liability per bank per
  asset, distinct from clearing suspense for exactly the reason above — and
  `PostCreditorLegTx` calls `CheckCreditTx` and diverts to it. What resolves the
  balance later is still not built: the money sits there until someone claims
  it. The mirror case on the RETURN path is also still open, and the README's
  *Next Work* records it.
- **A gap this comparison never found, and item 11 closed on its way past.**
  `DisburseTx` used to clamp `LastAccrualDate` forward to the wall clock before
  reopening the accrual window on a re-drawn facility. The clamp stopped a span
  being charged twice — and thereby stopped the span between the real last
  accrual and the re-disbursement being charged at all: the mirror image of the
  double-charge that was fixed, through the same code path. Effective-dated
  terms removed the clamp outright. The window opens at origination and every
  run re-derives the facility's whole life, so the skipped span is charged, and
  it still cannot be charged twice — a recomputation posts the CHANGE in the
  rounded value, not an increment.

---

## 1. Where the book independently validates this repo's design

These are not vague alignments — in several cases the book reaches the same conclusion via the same argument, occasionally in nearly the same words.

| Repo decision | Book |
|---|---|
| **Per-asset balance invariant**, not global. FX therefore runs through per-asset position accounts; the ledger never holds a rate. | §4.1.3 states the invariant "for each currency separately", gives the same reason ("the ledger has no exchange rate and **must not acquire one**"), and prescribes the same position-account construction. Appendix B.10 is the repo's `README` FX example with different numbers. §3.5.3: "FX is an exchange, not a conversion." |
| **Unsigned `Amount` + explicit `Direction`**, integers in the asset's minor units, scale from the asset definition. | §4.1.2 names exactly two defensible conventions and prefers this one ("makes accidental sign errors harder to write and easier to spot in review"), insists on integer minor units, and says the minor-unit count comes from the currency table — "do not hard-code 100." |
| **No `balance` column anywhere**; balance is `SUM(entries)` signed by the account's *normal direction*, passed as a query parameter. | §1.1.3, §4.4.1, §22.2.5. §4.4.1 flags the exact trap: the sign orientation is account-type dependent — "Pick one and write it down." |
| **Append-only; reversal is a new, equal-and-opposite transaction linked to the original.** | §4.1.4, §4.5.1 — and the legal grounding the repo doesn't state: HGB §239(3), AO §146(4), GoBD forbid *untraceable* alteration, not correction. |
| **Reversal carries the original's value date and today's booking date** (`ledger/book.go:812-813`). | §4.5.1, verbatim: "reversals value-date as the original, book-date as today." The repo got this right without stating why; the book's reason is that the customer's value-dated balance series must look as if the error never happened. |
| **Holds live off-ledger; only capture posts.** | §4.3.1: "Our strong recommendation: holds live in their own table with their own lifecycle, and only the release into a real posting touches the ledger." The repo's `README` argument ("the ledger stays a pure record of settled value") is the book's argument. Appendix B.3 is the repo's hold model. |
| `Available = Book − Holds + OverdraftLimit` | §4.3.6's formula, minus the block/earmark terms the repo doesn't have. |
| **Idempotency uniqueness enforced in the ledger schema itself**, via a partial unique index, and the *insert is the check* (no look-then-insert). | §4.1.3 ("the uniqueness constraint belongs in the ledger schema itself, because the ledger is the last line of defence"); §11.3.2 has the identical crash-safety argument for claiming the key in the same transaction as the postings. |
| **`ORDER BY id … FOR UPDATE`** — canonical lock ordering to prevent deadlock between transactions touching overlapping account sets. | §22.3.2, strategy 1: "a journal touching several accounts locks them in canonical order (ascending account ID), **always**." |
| **The three races the mutex was hiding** (balance-check/post, idempotency, double reversal) and the rule "never read, decide, then write — make the write the decision." | §22.3.2 diagnoses exactly the first as *write skew* under snapshot isolation, with the same two-concurrent-debits example. §11.3.2 and §22.3.5 cover the second. |
| **ID counters as ordinary rows, not `SEQUENCE`s**, because a sequence survives rollback and would burn a transaction number. | Not discussed for postings, but §16.1.4 makes precisely this argument for gapless statement numbering: "A plain database sequence will not do, because sequences deliberately tolerate gaps on rollback… a counter row per account per year, incremented inside the same transaction." |
| **`entries` needs an explicit `position` column** because a slice is ordered and a table is a set. | §4.1.5's schema has `ordinal SMALLINT NOT NULL -- stable order in journal`. |
| **`ORDER BY created_at, seq`, never `ORDER BY id`.** | §16.1.2: order by date then "a stable tiebreaker — the journal's monotonic sequence number… Never order by timestamp alone." |
| **Composite PK `(book_id, id)`**; chart-of-accounts numbers unique per book, never globally. | §3.5.2: "Every account and every posting carries entity and branch. Postings never balance across entities." §22.2.2: "By legal entity first, always." The repo's `BookID` is the book's entity partition. |
| **Boring replicated Postgres.** | §22.2.7 works the arithmetic for an 800k-customer bank and concludes a single PostgreSQL-class primary clears it with headroom: "Most banks that believe they need a planet-scale database need an index review and a fan-out audit." It names purpose-built ledgers as an optimisation, not a default. |
| **One asset per account; a "multi-currency account" is several accounts.** | §3.5.3: "A multi-currency account in the product sense is, in the ledger, a family of single-currency positions presented together." §4.1.3 advises against multi-currency accounts "at the ledger layer." |
| **Clearing ≠ settlement; banks meet only at the central bank; netting; suspense returns to zero; the bank's reserve asset mirrors the CB's reserve liability.** | §13.2.1 and §13.2.4 model exactly this, including the rule the repo follows: *the mirror account must move only in statement-shaped amounts*, with clearing accounts absorbing the difference. §13.2.2: "each of these central bank accounts gets exactly one mirror account." |
| **Settlement is one all-or-nothing unit of work in the central bank's own book** — over every member's settlement account *there*, and over no member's own ledger. *(This row read "spanning every book" and was true when written. Sub-project 8's Task 15 moved each member's mirror leg and creditor legs into that member's own unit of work, made on advice after the central bank has already committed; the batch is still whole or nothing at the settlement agent.)* | §13.2.4/§3.1.4. §3.1.4 adds the framing the repo used to only be able to admire and now implements: money in flight between consistency domains "must sit, visibly, in a named account… An unexplained gap between two systems is a finding; a balance on a clearly named clearing account with an ageing report is business as usual." That named account is `Clearing Suspense (<asset>)`, and the interval it is non-zero over is the unreconciled position — visible as that balance with **no** `settlement_advices` row against the cycle, since a member's row commits in the same unit of work as the mirror leg it records. What is still missing is the ageing report: Task 19. |
| **`Scheme` interface carrying `SettlementModel` (Net\|Gross).** | §13.1.2's STEP2-vs-TIPS distinction is precisely this axis, and the repo's *Next Work* note that gross settlement needs a different posting path matches §13.2.4 (TIPS skips the clearing account on the settled path). |
| **Overdraft: no facility record, no reclassification posting; the Asset-side figure is `Σ max(0, −balance)`.** | §7.1.2, and this is the strongest single corroboration in the book: "The account itself does not move in the subledger; the general ledger mapping does… **Do not try to solve this with paired shadow accounts in the subledger; banks that do spend the rest of their lives reconciling the pair.**" |
| **Arranged rate + higher unarranged surcharge on the excess.** | §7.1.2's *eingeräumte* (§504 BGB) vs *geduldete* (§505 BGB) Überziehung — the repo has the right two-rate shape, and the book confirms the excess is priced higher. §7.1.2 also notes capitalised interest may itself breach the limit, which the repo permits. |
| **Two GL accounts per facility (principal + accrued receivable) because repayment splits interest before principal.** | §8.1.4 and §8.3.1: "this is why the ledger needs separate accounts per claim type rather than one 'loan balance' — the bases genuinely differ." Appendix B.7 is the repo's repayment posting. |
| **Daily accrual at sub-minor-unit precision; post the delta of the rounded value.** | §7.4.2: "accrues daily per account at high precision — sub-cent, typically six or more decimal places, with rounding deferred to capitalisation." Six decimals is the repo's micro-minor-unit choice exactly. |
| **Day-count convention is a contract term, not a code path.** | §7.4.1: "the convention is a product parameter, never a code path chosen by product type… implement each convention once, in a single audited library with exhaustive tests around month ends, leap years, and February 28/29." That is the `interest` package's charter. |
| **Schedule is a plan; repayment settles what actually accrued; 30/360 exists so the two agree.** | §8.2.2 ("the schedule is data, not a formula… generate it eagerly at activation, store every row, and post against the stored rows") and §7.4.1's convention table. The repo's insight about 30/360 reconciling plan to accrual is correct and the book implies it without stating it as crisply. |
| **Last instalment absorbs the rounding residue.** | §8.2.2: "Nordwind adjusts the final instalment… whichever rule you pick must be deterministic, documented, and identical between the schedule shown to the customer at signature and the postings booked at runtime." |
| **DPD from the schedule's oldest unpaid instalment; buckets Current/1-29/30-59/60-89/90+; non-performing at 90.** | §8.4.1 uses *the same five buckets* and the same oldest-unmet-due-amount definition. §8.4.3 anchors 90 DPD in CRR Art. 178. |
| **Audit event written inside the operation's transaction, so a rollback leaves no record claiming it happened.** | §17.1.4: "emit audit events in-path, not best-effort. If the audit write fails, the action fails" — implemented as a durable local append in the action's own transaction (the outbox pattern applied to audit). |
| **Store-global monotonic `Seq` as the audit ordering/pagination key, not the timestamp.** | §17.1.2 ("a per-stream sequence number that makes gaps detectable") and §11.4.5 ("never use wall-clock timestamps to order ledger operations"). |
| **`store/storetest` — one conformance suite two backends must both pass.** | The book has no direct equivalent, but §5.4.2's *independent recomputation* control ("different code path, different author, ideally different team") is the same instinct. See §5 below. |

## 2. Direct disagreements

Six places where the book takes a position the repo contradicts, with a stated reason.

### 2.1 Value date belongs on the entry, not the transaction

`ledger.Transaction.ValueDate` is one field for the whole posting. §4.1.5 puts `value_date` on `posting`, explicitly: *"value_date lives on the posting, not the journal, because the two legs of one event can legitimately value-date differently."* §4.2.1 gives the case — an inbound SEPA credit whose customer leg must be value-dated to receipt under PSD2 Art. 87 while the settlement leg carries the settlement date.

This is a schema decision that is cheap now and a migration later. It is also the one place where the repo's own "the asset lives on the account, not the entry, because a second copy can disagree" reasoning cuts the *other* way: an entry's value date is genuinely not derivable from its transaction.

### 2.2 There is no value-dated balance, and the book calls that the correctness baseline

`ledger.Book` exposes exactly one balance: `BookBalance`, a sum over all entries regardless of value date. Nothing anywhere computes a value-dated balance. Every accrual in the repo — `deposit.accrueOverdraftAccountTx`, `lending.accrueFacilityTx` — reads a *book* balance.

§4.2.2 is unusually blunt: *"Interest engines do not consume booked balances; they consume value-dated balances… Value-dated computation is not an optimisation; it is the correctness baseline."* Its worked example (Deniz, a delayed salary credit with a backdated value date) is three days of overdraft interest wrongly charged, multiplied across the book into "a remediation project with customer refunds, recalculated tax withholding, and a mandatory report to the regulator."

The repo *stores* `ValueDate` on every transaction and every layer sets it thoughtfully (`payment` value-dates the debtor leg to settlement; `lending` and `deposit` value-date accruals to the accrual day). The data is there. Nothing reads it.

**This is the largest single gap between the README's claims and the code.** The README's *Balance Types* section describes the value-date balance as one of three balances "a single account carries at any point in time" and says it is "what the bank uses to calculate interest, generate end-of-day snapshots, and produce regulatory reports." No such balance exists, and interest is calculated from the book balance.

### 2.3 A duplicate idempotency key should replay the stored outcome, not error

The repo returns `ErrDuplicateIdempotencyKey` and the README instructs the caller to "look up the original transaction by the key." §11.3.2 prescribes the opposite: store the *outcome* (response, resulting journal id, and a hash of the request payload); on a key hit with a matching payload hash **replay the stored response**; on a key hit with a *different* payload, reject loudly because the client has a bug. It also insists rejections are stored — *"If the first attempt failed on insufficient funds, the retry of that key must return 'insufficient funds' even if the account has since been topped up… Anything else makes outcomes depend on retry timing, which is exactly the nondeterminism idempotency exists to kill."*

The repo's behaviour is defensible for a library (the caller has a lookup path), but it does not give the caller the safety property idempotency exists for: an unconditionally safe retry.

### 2.4 Booking dates must not be assigned inside the posting path

`PostTransactionTx` does `bookingDate = s.now()` when the caller supplies none. §4.2.4: *"Booking dates advance by an explicit, logged system event — the business date roll — not by the server clock crossing midnight… the clean design assigns the booking date once, at command acceptance, from a business-calendar service, and carries it with the command thereafter; **nothing downstream ever calls `today()` for accounting purposes**."*

Relatedly, §4.1.3 forbids future booking dates outright — *"Postings dated in the future are a different object — a scheduled instruction — and must not sit in the ledger pretending to be facts"* — and the repo accepts any caller-supplied `BookingDate`, including tomorrow's, through `POST /participants/{pid}/transactions`.

There is no business date, no roll event, no banking calendar, and no TARGET/holiday handling anywhere in the repo. §11.4.3's worked example (a transfer submitted after cut-off on Maundy Thursday settling the following Tuesday, alongside an instant transfer one minute later settling immediately) is the kind of thing `lending.AddMonths` and the schedule generator will silently get wrong.

### 2.5 Repayment allocation order is a jurisdiction- and state-dependent policy, not a constant

`lending.RepayTx` hardcodes interest-before-principal. §8.2.4 says the order is statute:

- §367(1) BGB, the default: **costs → interest → principal**.
- §497(3) BGB, for *consumer loans in default*, inverts it: **costs → principal → interest** — deliberately, so a struggling consumer's payments shrink the interest-bearing base first. Plus: default interest does not compound and is tracked separately.

The book's verdict on the repo's shape is explicit: *"A hardcoded allocation order is a latent compliance defect. The moment Nordwind's Spanish branch books its first consumer loan… the hardcoded order is wrong for someone."* The prescribed model is typed open-item claim buckets with a product-version-pinned priority policy.

This is the sharpest criticism the book makes of something the repo actually implements (as opposed to omits).

### 2.6 Defence in depth in the schema, versus the repo's conformance-parity rule

`0001_init.sql` deliberately has **no** `CHECK` on the four asset columns, on the grounds that "the asset must be one the system knows" is a domain rule and a constraint would make `store/pg` refuse a write `store/mem` accepts. The reasoning is internally airtight and the conformance suite enforces it.

The book pushes the opposite way on every equivalent question. §4.1.5's schema carries `CHECK (direction IN ('D','C'))` and `CHECK (amount_minor > 0)`, and it recommends encoding the *balance* invariant as a deferred constraint trigger "so the database guarantees it even against buggy or malicious writers; the cost is small and the assurance is worth it." §22.2.3 goes further: *"the application's database role has INSERT and SELECT on posting tables, and no UPDATE or DELETE… the grant list is the enforcement, and it is also the first thing a competent IT auditor checks."* §4.1.4 wants triggers rejecting mutation of booked journals.

This is a genuine philosophical divergence rather than a bug: the book optimises for auditor-demonstrable enforcement in a single-backend production bank; the repo optimises for two backends behaving identically, a constraint the book never faces. Worth stating as such in the README rather than leaving the impression that "validation belongs in the domain layer" is the industry position — it is a consequence of the repo's dual-store design.

## 3. Doc-vs-code gaps the book's standard exposes

Three README claims the code does not implement. Given `CLAUDE.md`'s rule that the README is authoritative and the layers must agree, these matter.

1. **Value-dated balance** — described in *Balance Types*, unimplemented (§2.2 above).

2. **Snapshots as balance checkpoints.** *A Balance Is an Aggregate, Not a Column* says: "Reading a balance costs an aggregate over every entry the account has ever had. The remedy is not to add the column back; it is to **checkpoint**. That is precisely what an end-of-day snapshot is for: a query starts from the nearest snapshot and replays only what came after it." No balance query reads a snapshot. `deposit.Snapshot` is written by `TakeEndOfDaySnapshot`, read only by `GetSnapshot`/`ListSnapshots`, and never consulted by `BookBalance` or `availableTx`. §22.2.5 describes the pattern the README claims, and adds two disciplines the repo would need: the snapshot must be anchored to a **ledger sequence position, never a wall-clock time** ("postings commit out of wall-clock order and a time-defined snapshot is ambiguous at the boundary"), and backdated postings must invalidate affected snapshots.

3. **Statements.** The README has a full *Statements* section specifying the algorithm (filter → project onto the account's leg → sign by normal balance → accumulate; final running balance must equal the book balance as a reconciliation check). There is no statement type, no generator, no endpoint, no test. Chapter 16 is entirely about what this would need: gapless per-account-per-year numbering enforced by a counter row, opening balance chained to the previous statement's closing balance and asserted against the ledger at generation time, a generation cut-off after the business date is sealed, **derived fields captured at generation time rather than looked up at render time** ("the merchant renames itself… and the regenerated 2026 statement says something different in 2029"), retention of *both* the dataset and the rendered artefact, and a version column so re-issuance is explicit and the original is superseded, never overwritten.

Also stale: the README's *Next Work* says "enforcing **account status** on the debit path (a `Frozen` deposit account should block a card authorisation)" is future work. It is already done — `payment/scheme.go:85` calls `deposit.CheckWithdrawalTx`, which is status-aware (`deposit/register.go:766`). This sentence used to end "the real remaining hole is the **creditor** side"; that hole is half-filled since Task 15 — the creditor leg checks and diverts, and what is still missing is what resolves the diverted balance later. See §4.5.

## 4. Structural gaps — whole chapters with no counterpart

Ranked by how load-bearing the book says they are.

### 4.1 The party model (Chapter 6) — the book's "cheap now, ruinous later" case

The repo has no party, customer, or account-holder entity. A `deposit.Account` has a `Name string`. The roadmap records "No customer entity" as a deliberate multi-asset-scope decision.

The book calls separating party / customer / account-holder *"the single most useful modelling decision in this domain"* and says retrofitting it "is one of the hardest migrations in banking… designing it correctly up front is comparatively cheap." Everything downstream depends on it: joint accounts (Oder-/Und-Konto as *disposal rights on holder roles*, not an account subtype — §6.1.3 warns that modelling it as a subtype explodes combinatorially), minors and guardians, powers of attorney with death-behaviour, beneficial owners, deposit-guarantee single-customer view (§7.3.4), interest certificates that are per-customer-per-year while postings are per-account (§16.2.3), and customer-level limits (§6.2.3 — "split one person into two parties and their effective limit doubles").

Note the terminology trap the book flags in §6.2.1: SEPA "mandate" (a debtor's authorisation to a creditor) and account "mandate" (disposal authority) are different things. The repo uses only the first sense, which is correct for the `payment` layer, and has none of the second.

### 4.2 Subledger → general ledger, and financial close (Chapter 5)

The repo is a single flat book per bank with subledgers as a grouping level. There is no thin GL, no posting-rules engine, no per-key-per-day aggregation, no control accounts, no trial balance, no suspense register with ageing and attestation, no period close, no closed-period lock, no reconciliation. `docs/deposit-accounts-vs-subledger.md` §7 already frames this ("Pattern A — separate books" vs "Pattern B — one unified ledger") and the choice is deliberate and well argued.

The book's Chapter 5 is a strong endorsement of *why* the pattern exists and a catalogue of what the repo forgoes. Two items are cheap and would be high-value teaching material given the repo's design:

- **Trial balance.** §5.2.1. In a system where every posting is forced to balance, the trial balance "can never actually fail arithmetically" — which is exactly why it is worth computing: it is a control on the *pipeline*, not the arithmetic. For this repo the natural form is: per book, per asset, `Σ balance signed by normal direction == 0`, plus roll-forward integrity. A handful of lines, and it would catch a bad migration or a direct store write.
- **The balance-side-dependent GL mapping** that the overdraft argument depends on (§7.1.2). The repo's `deposit.Totals` (`register.go:1172`) is the stand-in and the book confirms the aggregation is the right answer — it just locates it in the subledger-to-GL summarisation the repo doesn't have.

Also from Chapter 5: **suspense discipline**. The repo's payment layer has a clearing-suspense account per participant per asset, and it returns to zero by construction. The book's §5.2.2/§5.2.3 regime — a suspense register with a named owner, item-level ageing buckets, clearance deadlines, monthly attestation, and maker-checker on any clearance to P&L — is what a real bank wraps around exactly that account.

### 4.3 Product definitions as versioned, effective-dated, immutable artefacts (Chapters 3.4, 7.8, 8.6, 24)

*Written when product terms were mutable columns on the instance — `Account.Rate`, `UnarrangedRate`, `DayCount`, `OverdraftLimit`; `Facility.Rate`, `Method`, `MinPayment` — and `SetOverdraftTermsTx` overwrote the rate in place. The argument below is kept because it is why the work was done; the status note at the end says where it now stands.*

§24.1.1 makes the argument that lands hardest here: every financial calculation is a function of account state, event history, *and configuration*; Chapters 4 and 5 make the first two immutable and replayable; *"if the third input can be edited in place, that entire investment is undermined, because 'what did this account's product say on 15 July 2027?' no longer has a stable answer."*

Concretely: after `SetOverdraftTerms` runs, an accrual posted six months ago cannot be reproduced from stored state. The audit event does carry the full `Account` payload, so the history is *recoverable* by replaying the log — but it is not *resolvable*, and no code does it. This is the one place where the repo's own governing principle (never mutate history; every number must be derivable) is broken by its own code, and it is cheap to fix at this size.

**Status: partly closed.** Both halves have now landed, and what remains open is open by choice rather than by omission.

*The effective-dated record* (the first half) is closed: terms are rows on a per-instance timeline, one per repricing, and accrual resolves the row in force on each day it prices. Item 11 above.

*The catalogue* (the second half) is closed for deposits. A deposit account is opened **from** a named `product` whose pricing is a timeline of immutable published **versions**; `product.Version` carries a content hash; §24.1.3's per-parameter-group **pinned vs floating** binding is implemented as the central distinction — the rate floats with the product, the limit is pinned to the account, and `product.OverdraftPricing` has no limit field, so "the limit does not float" is enforced by the compiler rather than by a rule; and §24.1.4's **overlays** are the per-account negotiated pricing, carried on the account's own terms timeline rather than in a second table so that "the terms in force on day D" stays unique by construction.

Four things the book has that this does not, each with its reason:

- **The non-overlapping-interval exclusion constraint** is replaced by a `(product, day)` primary key, which makes the row in force on a day unique by construction rather than by a constraint. A `tstzrange` with a `GiST` exclusion is better in a single-backend bank and unavailable here: `store/mem` cannot implement a range exclusion, and `store/storetest` would have nothing to pin. This is the same dual-store trade as the missing `CHECK`s (item 5).
- **The resolution log** — §24.1's per-calculation record of which version was used — is not written. The pair (`ProductID` on the account's terms row, `EffectiveFrom` on the version timeline) makes the resolution re-derivable from stored rows, which is the property the log exists to provide; a log would make it *cheap to query* rather than *possible*, which is a checkpointing concern and belongs with that successor.
- **Maker-checker on publication** is absent, and publishing is the highest-blast-radius write in the system. What ships instead is **forward-only publication**: a version effective before today is refused outright, so a mispublished rate cannot silently move interest already charged across a whole book. Retroactivity stays with the per-account overlay, where the blast radius is one named customer. This is deliberately weaker than the book and is stated as such in the README. Comparison item 17.
- **Lending binds to nothing.** `lending.Facility` keeps its own `FacilityTerms` timeline and is not opened from a product. A term loan **cannot float at all** — repricing a disbursed one is refused with `ErrScheduleWouldDiverge` — so a term-loan product version would be a pure origination default, a second binding model. Two binding semantics under one type in one pass is the ambiguity that makes a design rot.

Also still absent, and named in §4.4 rather than here: fee schedules, tiered and bonus rates, and capitalisation frequency. A catalogue that carries only the parameters the code actually reads is the honest version; widening it is a deposit-products topic.

### 4.4 Deposit products (Chapter 7) — the deposit layer never pays interest

The repo has current accounts only. There is no savings account, no time deposit, no notice period, no tiered or bonus rate, no capitalisation frequency, no fee schedule, no waivers, and **no credit interest on deposits at all**. The `interest` package is used only on the asset side: `deposit.AccrueOverdraft` debits a receivable and credits interest income (the bank earns), and `lending` does the same.

This is worth naming because the README says lending "is the mirror image of the deposit layer" and is "the best available proof that the GL really generalizes." It is — but the mirror is only half-built: the liability-side accrual (debit interest *expense*, credit accrued interest *payable*, then capitalise to the customer) has no implementation, and Appendix B.4 and B.12 are its canonical shape. Adding a savings product would exercise `interest` in the direction it has never run and cost very little, since the arithmetic is asset-agnostic and already tested.

Also absent and cheap-ish: **fees**. The README's own usage example posts a transfer fee by hand, and §11.1.4's worked example puts the fee leg *in the same journal as the payment* — "the fee is not a second, later transaction that might fail independently and leave revenue uncollected." Appendix B.5 covers charge and refund, and §4.5.2's distinction is worth having: a fee *reversal* debits fee income (the charge was an error), a goodwill *waiver* debits a goodwill expense account (the charge was valid; the bank chose to give it back) — "misclassifying adjustments as corrections quietly falsifies both."

### 4.5 Payments: R-transactions, inbound suspense, and the creditor leg

The repo implements Reject (pre-clearing) and Return (post-settlement). §12.4.1 enumerates six distinct flows with different initiators, timelines, messages, and posting consequences: **reject, return, refund, refusal/cancellation request, reversal, recall**. Two specific findings:

- **The creditor leg bypassed the deposit layer entirely.** *Closed by sub-project 8's Task 15 (2026-08-02); recorded here as it stood, because the reasoning is what changed.* The finding was that `SettleCycleTx` resolved `creditor.glAccountTx(...)` and posted straight into the GL account, so a payee whose account was frozen or closed between initiation and settlement was credited anyway — and that the repo "has the suspense account already; it lacks the branch." Both halves are now wrong about the code. `SettleCycleTx` posts no creditor leg at all: that leg is the payee's bank's own act, `PostCreditorLegTx`, in its own unit of work on a per-payment advice after the cut-off has settled. It calls `Deposit.CheckCreditTx` and, for the one refusal that check makes, diverts to **Unclaimed Balances (`<asset>`)** — see the *Status* note above, which records why the destination could not be clearing suspense. A **frozen** payee is still credited, and that is deliberate rather than missing: the freeze modelled here is a debit block. §12.2.3's remaining half is what resolves the balance afterwards — a return initiated within the rulebook window (three banking business days for SCT), with "inbound suspense older than the return window is a reconciliation break by definition" as the control on it. That is still not built; the money sits in Unclaimed until someone claims it, and no clock watches it. Comparison item 13 is therefore **half-closed**, not open.
- **Returns must post what actually moved.** §12.4.3's worked example is a recall returned net of a €12.50 handling fee: *"A naive 'reverse the original journal' implementation posts EUR 1,200.00 and leaves a EUR 12.50 hole in the clearing account that reconciliation finds three weeks later."* This is precisely why the book insists exception legs are new linked events, never Chapter-4.5-style reversals of the original — which the repo already does correctly (`ReturnPayment` posts compensating transactions rather than reversing).

Two design notes the repo would benefit from recording: §12.1.5's **pattern A (debit-before-submit) vs pattern B (earmark, post-on-settlement)** — the repo is unconditionally pattern A, and the book says pattern B is the right choice for instant rails, which is directly relevant to the *Next Work* gross-settlement plan. And §12.3.3's **identifier chain** (end-to-end ID / interbank transaction ID / internal reference); the repo has `EndToEndID` and internal IDs, which is two of the three and adequate at this scope.

### 4.6 Controls: maker-checker, actor attribution, and the audit-vs-accounting split

`ledger.AuditEvent` has an `Actor` field, documented as "empty until authentication exists." §17.1.1 draws the line the repo's log currently straddles: the *ledger* answers "what happened to the money", the *audit log* answers "who did what, when, from where, and under which authority." The repo's log is closer to a change-data-capture stream of accounting events than to an audit log — which is fine and honest, but it means two book requirements are unmet by construction:

- **Failed and rejected attempts are not recorded.** The repo writes audit events only inside the successful transaction — correct for accounting completeness, and the README argues it well. §17.1.2: *"Failed and rejected attempts are some of the most valuable events in the log — a pattern of rejected self-approvals is exactly what an internal auditor or a fraud investigator needs to see — and they are precisely the events an implementation wired only to the happy path will miss."* The resolution in the book is that these are two different logs with different retention and access, joined by a command identifier.
- **`POST /participants/{pid}/transactions` is an unrestricted free-form posting endpoint.** §17.2.2 calls the manual posting function *"the single most dangerous capability in the bank, because it can, by construction, do anything the ledger can represent"* and puts it first on the mandatory dual-control list, with a maker, a checker, a reason code, and an incident reference. §17.2.3's implementation notes are the interesting part for a teaching repo: self-approval blocked *at the API layer regardless of roles held*; the checker approves a **payload hash**, so any post-submission edit voids the approval; pending actions expire; rejections are first-class records.

### 4.7 Blocks vs holds; hold expiry

Two smaller items from Chapter 4/6:

- **Hold expiry is lazy.** Expired holds stop counting toward `Holds` in `availableTx` but keep `Active` status and emit no event. §4.3.2: *"Expiry deserves active engineering, not a comment in the schema. Holds must expire by a sweep or scheduled job that transitions state and emits an event."* The repo's arithmetic is right; the state machine is less honest than it looks, and the population-by-age metering the book wants isn't possible.
- **Blocks are a single status field, so they cannot stack or carry direction.** The repo has one `AccountStatus` (Active/Dormant/Frozen/Closed). §4.3.4 and §6.3.4 want blocks as *rows* with typed reason, authority, legal basis, **scope** (single transaction / amount / whole account / whole customer) and **direction** (debit-only / credit-only / both), because *"'amount-based hold' cannot express 'no debits of any kind, but credits post normally', which is exactly what an investigation block usually wants"* — and because releasing one block must not release the others. §6.3.4 also gives the posting-attempt evaluation order the repo half-implements: lifecycle state → blocks → mandate/signatory → limits → available balance.

### 4.8 Not attempted, correctly out of scope

Listed for completeness, since the book devotes real space to each and the repo's roadmap already defers most: ECL/IFRS 9 provisioning and HGB EWB/PWB (Ch 8.5), write-off vs derecognition (§8.4.4), forbearance and restructuring with probation clocks (§8.4.3), default interest as a separate accrual stream (§8.3.2), early-repayment compensation (§8.3.4), termination guards under §498 BGB, withholding tax (§7.4.5, Appendix B.13), tax and fee certificates (Ch 16.2), regulatory reporting/FINREP/COREP/AnaCredit (Ch 15, 18), GDPR-vs-immutability (Ch 19), DORA (Ch 20), AML/sanctions (Ch 21), securities (Ch 9), crypto (Ch 10), treasury and intraday liquidity (Ch 14), migrations (Ch 23).

## 5. Where the repo goes beyond the book

- **`store/storetest` as a conformance suite.** The book never poses the two-backends problem, so it has no answer to it. The repo's rule — *`store/pg` must never accept or refuse a write that `store/mem` handles differently* — and its consequences (no `UNIQUE (book_id, name)`, no asset `CHECK`, `ErrDuplicateIdempotencyKey` costing one `SAVEPOINT` rather than the whole transaction) are sharper reasoning than anything in the book on schema-constraint placement. It is essentially §5.4.2's "independent recomputation" control turned into a design constraint.
- **`ledger.ValidateText`.** The NUL-byte-in-`jsonb` story and the resulting single domain rule ("valid UTF-8, no control characters") is a class of bug the book does not discuss at all, and the reasoning — *"a rule that can only be stated by naming a database is not a domain rule"* — is good.
- **The `interest.Accrued` precision split with the posted delta**, pinned by a worked table. The book says "sub-cent, six or more decimal places, rounding deferred" and stops. The repo's insight that *posting the change in the rounded value* is what makes drift structurally impossible, and that the residue can legitimately disagree with the ledger by a sub-minor-unit amount (hence `Close` testing the receivable's ledger balance rather than the record), is more precise than the book's treatment.
- **Explaining the absence of a constraint inside the database with `COMMENT ON COLUMN`.** The book wants `Verfahrensdokumentation` (§17.1.5) and versioned schema docs; putting the reasoning where the next author will actually read it is better.

## 6. Suggested actions, in rough order of value per unit of effort

**Correct the record (docs only):**
1. README *Balance Types*: say plainly that the value-dated balance is described but not computed, and that interest accrues on the book balance. Or implement it (below).
2. README *A Balance Is an Aggregate*: the snapshot-as-checkpoint sentence describes an unimplemented optimisation. Mark it as such, or implement it.
3. README *Statements*: the section specifies an algorithm with no implementation. Either say so, or build it — it is a very good fit for this codebase and would exercise the ledger's ordering guarantees.
4. README *Next Work*: account-status enforcement on the debit path is already done. ~~The live gap is the creditor leg at settlement.~~ **Half-done (Task 15, 2026-08-02):** the creditor leg now checks and diverts to Unclaimed Balances; the README records what is left, which is a return path for the diverted balance. See §4.5.
5. README *Two Stores, One Conformance Suite*: note that the no-`CHECK` position is a consequence of the dual-store constraint, and that a single-backend production system would be advised the opposite (defence in depth, revoked `UPDATE`/`DELETE` grants, deferred balance triggers).

**Small, high-teaching-value code:**
6. **Trial balance** per book per asset (`Σ balance signed by normal direction == 0`), plus a roll-forward check. Cheap; catches a class of bug nothing currently catches; directly teachable from §5.2.1's "why compute a check that cannot fail?"
7. **Value date on `Entry`**, defaulting to the transaction's. Schema change is trivial now and a migration later, and it unblocks item 8.
8. **`ValueDateBalance(ctx, accountID, asOf)`** and point the accrual paths at it. This is the correctness baseline the book insists on, and the repo already stores every input.
9. **Reject future booking dates** in `PostTransactionTx`, per §4.1.3.
10. **Hold expiry sweep** that transitions status and emits `hold.expired`.

**Medium, and each is a good self-contained chapter:**
11. **Effective-dated terms** on deposit accounts and facilities (a small `terms` history table replacing in-place mutation), so any past accrual is reproducible from stored state.
12. **Allocation-order policy** on the facility, with §367 BGB as default and a `§497(3)` variant, replacing the hardcoded interest-first.
13. **Inbound suspense + return** when the creditor leg cannot be applied at settlement. **Half-done (Task 15, 2026-08-02):** the destination exists and the leg diverts to it. What remains is the *return* half — a rulebook-window clock over the Unclaimed balance, and an outbound return that clears it.
14. **A savings product** — the one change that would make the deposit layer a real mirror of lending, exercising `interest` on the liability side with capitalisation.

**Large, and probably deliberate non-goals:**
15. A party model (Ch 6) — the book's strongest "do it now" argument, and the repo's most expensive omission if it is ever wanted.
16. A thin GL with per-key-per-day aggregation and a posting-rules engine (Ch 5) — but `docs/deposit-accounts-vs-subledger.md` already argues this away coherently.
17. Maker-checker on the manual posting endpoint, and an actor-bearing audit log distinct from the accounting log (Ch 17).
