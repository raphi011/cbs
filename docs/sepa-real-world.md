# Who a real SEPA bank actually talks to

Real-world plumbing behind SEPA credit transfers and direct debits: the
institutions, the systems they operate, and which of them a given bank has an
actual connection to. This is background for the domain modelled in this
repository, not a description of it.

## The confusion, stated plainly

The names in this space belong to **three different kinds of thing**, and they
get listed side by side as if they were peers:

| Name | What kind of thing it is |
| --- | --- |
| EPC | An association. Writes rulebooks. Carries no traffic. |
| EBA CLEARING | A company. Owned by its member banks. |
| STEP2 | A *system* operated by EBA CLEARING. Bulk clearing. |
| RT1 | A *different system* operated by EBA CLEARING. Instant. |
| Eurosystem | ECB + the national central banks. |
| T2 | A *system* operated by the Eurosystem. Settlement. |
| TIPS | A *different system* operated by the Eurosystem. Instant. |
| SEPA-Clearer | A *system* operated by Deutsche Bundesbank. Bulk clearing. |
| Bundesbank | A national central bank. Wears three hats — see below. |

"Clearing house" is a **role**, not an institution. EBA CLEARING plays it with
STEP2 and RT1; Bundesbank plays it with SEPA-Clearer; Iberpay, STET,
equensWorldline and Nexi play it in their countries. The generic term for one
in the rulebooks is **CSM** — Clearing and Settlement Mechanism.

## The four layers

Every euro retail payment passes through all four. Nothing skips a layer. This
is a map of *what each system is for* — not a flow; the same bank sits at both
ends of a payment.

```mermaid
flowchart LR
    EPC["<b>1 · Rules</b><br/>EPC — European Payments Council<br/><i>SCT · SCT Inst · SDD Core · SDD B2B rulebooks</i>"]
    BANKS["<b>2 · Banks</b><br/>DbtrAgt and CdtrAgt<br/><i>hold the customer relationship</i>"]

    subgraph L3["3 · Clearing — CSMs: match, sort, net"]
        STEP2["STEP2<br/><i>EBA CLEARING</i><br/>bulk"]
        SEPACL["SEPA-Clearer<br/><i>Bundesbank RPS</i><br/>bulk"]
        NATL["National ACHs<br/><i>STET · Iberpay · Nexi</i><br/>bulk"]
        RT1["RT1<br/><i>EBA CLEARING</i><br/>instant"]
    end

    subgraph L4["4 · Settlement — central bank money, final"]
        T2["T2<br/><i>Eurosystem RTGS</i><br/>ex-TARGET2"]
        TIPS["TIPS<br/><i>Eurosystem</i><br/>clears AND settles"]
    end

    EPC -.->|"binds them, carries nothing"| BANKS
    BANKS -->|"a bank picks one or more"| L3
    STEP2 --> T2
    SEPACL --> T2
    NATL -->|"net positions"| T2
    RT1 -->|"prefunded liquidity held in"| TIPS
    TIPS <-->|"liquidity transfers"| T2
```

Two things to read off it:

- **Clearing and settlement are separate steps** for everything bulk. STEP2
  decides *who owes whom*; T2 is where the money actually moves, in central
  bank money, once per cycle, netted.
- **TIPS collapses the two.** There is no netting and no cycle — each payment
  settles individually in central bank money, in about a second. That is the
  whole reason instant is a different system rather than a faster STEP2.

## Bundesbank wears three hats

This is where most of the confusion comes from. A German bank talks to
Bundesbank in up to three unrelated capacities, and they have nothing to do
with each other.

```mermaid
flowchart LR
    BB["Deutsche<br/>Bundesbank"]

    BB --> H1["<b>Hat 1 — NCB</b><br/>Provides the bank's access to T2.<br/>Always. Every German bank.<br/>Not optional."]
    BB --> H2["<b>Hat 2 — CSM operator</b><br/>Runs the SEPA-Clearer.<br/>Only if the bank chose it<br/>as its clearing house."]
    BB --> H3["<b>Hat 3 — Eurosystem member</b><br/>Co-operates TIPS with the ECB<br/>and the other NCBs.<br/>Not a German thing."]
```

A bank that clears through STEP2 still deals with Bundesbank for hat 1. A bank
that clears through SEPA-Clearer deals with it for hats 1 *and* 2 — and those
are still two separate relationships, two separate connections, two separate
sets of files.

## What the SEPA-Clearer is

A central bank running a retail ACH. Most national central banks exited retail
clearing when SEPA arrived and left the field to EBA CLEARING; Bundesbank
stayed in. Consequences:

- **Open access.** Any credit institution with a Bundesbank account can join.
  No sponsor, no membership hurdle.
- **Cost-covering pricing by statute.** No margin. This is the main reason
  small and mid-size German banks use it.
- **Bulk only**, several settlement cycles per business day, settling in T2.
  It has no instant product — that is TIPS.
- **Reachability by interoperability.** SEPA-Clearer is linked to STEP2 and
  other European ACHs, so a bank clearing only through it still reaches every
  bank in SEPA. Without those links it would be a German island.

## Direct and indirect participation

A bank does not have to be a member of the CSM it clears through. Most are
not — volumes below a few hundred million transactions a year do not justify
STEP2's fees and collateral.

```mermaid
flowchart LR
    subgraph IND["Indirect — the common case"]
        SMALL["Sparkasse or<br/>Volksbank<br/>~1M customers"]
        GW["Sponsor / gateway<br/>DZ Bank · Landesbank<br/>Finanz Informatik · Atruvia"]
        SMALL -->|"EBICS"| GW
    end

    subgraph DIR["Direct — participant in its own right"]
        BIG["Deutsche Bank<br/>Commerzbank<br/>ING"]
        SMALL2["Mid-size private<br/>or direct bank"]
    end

    GW -->|"SIAnet / SWIFT FileAct"| S2["STEP2"]
    BIG -->|"SIAnet / SWIFT FileAct"| S2
    SMALL2 -->|"EBICS"| SC["SEPA-Clearer"]
```

The indirect bank has **one** counterparty — its gateway. The gateway fronts
the CSM. Two hops in the network, one relationship to manage.

The SEPA-Clearer's open access is what makes the bottom row possible: a
mid-size bank can be a *direct* participant somewhere it can afford, instead of
hanging off DZ Bank or a Landesbank.

Who fronts whom, in Germany:

| Bank | IT processor | Clearing gateway |
| --- | --- | --- |
| Sparkasse | Finanz Informatik | Landesbank / DekaBank → STEP2, or SEPA-Clearer direct |
| Volksbank / Raiffeisenbank | Atruvia | DZ Bank → STEP2 |
| Private or direct bank | own or outsourced | SEPA-Clearer direct, or a correspondent / equensWorldline → STEP2 |

## A bulk credit transfer, end to end

```mermaid
sequenceDiagram
    participant C as Payer
    participant DB as Debtor's bank
    participant GW as Gateway<br/>(if indirect)
    participant CSM as STEP2 / SEPA-Clearer
    participant T2 as T2
    participant CB as Creditor's bank
    participant B as Payee

    C->>DB: initiates — REST, pain.001 or XS2A
    Note over DB: debit payer, hold the funds
    DB->>GW: pacs.008 in a batch file, EBICS
    GW->>CSM: batch
    Note over CSM: validate, sort by receiving bank,<br/>net across the cycle
    CSM->>T2: net positions of every participant
    Note over T2: single debit / credit per bank<br/>in central bank money — final
    CSM->>CB: pacs.008 in the outbound batch
    CB->>B: credit, camt.054
```

The payee's bank is obliged to credit by the end of the next business day
(D+1). The netting is the point: a hundred thousand payments between two banks
become one T2 movement.

## The whole chain, and the two things that travel

Same journey as a flat sequence of hops:

```mermaid
flowchart TB
    A["Payer initiates<br/><i>REST · pain.001 · XS2A</i>"]
    B["Debtor's bank<br/>debits payer, holds the funds"]
    C["Payment hub<br/>batches until cutoff"]
    D["Gateway<br/>DZ · Landesbank · Bundesbank"]
    E["STEP2<br/>validate · sort · net the cycle"]
    F["T2<br/>net positions settle<br/><b>the only place money moves</b>"]
    G["STEP2<br/>releases the output files"]
    H["Creditor's bank<br/>receives pacs.008"]
    I["Payee credited<br/>camt.054"]

    A --> B --> C
    C -->|"EBICS"| D
    D -->|"SIAnet / FileAct"| E
    E -->|"one net figure per bank"| F
    F -->|"settlement is final"| G
    G --> H --> I
```

The step most people get wrong is `F`. **The payments do not go through T2.**
They never touch it. What settles there is one net figure per bank — an
aggregate that no longer knows which payments it came from. Two things travel,
on separate paths:

| | Carries | Travels via |
| --- | --- | --- |
| **Information** | `pacs.008` — who to credit, how much | STEP2, bank to bank |
| **Money** | one debit or credit per bank | T2, in central bank money |

And the ordering is load-bearing: **the cycle settles in T2 before STEP2
releases the output files.** The receiving bank gets its instructions only once
the funds behind them are final, so it never credits a customer against money
that has not settled. Reverse those two steps and you have invented settlement
risk.

Direct debits are the same picture with the arrow reversed — the creditor's
bank pushes `pacs.003` to pull. Same batching, same netting, same T2.

## Where the payment comes from

`pain.001` is a *file format for one channel*, not a required first step. Most
retail payments never involve one:

```mermaid
flowchart LR
    APP["Mobile / web app<br/><i>REST to the bank's own API</i>"]
    CORP["Corporate ERP<br/><i>pain.001 over EBICS</i>"]
    TPP["Third-party provider<br/><i>PSD2 XS2A, REST + OAuth</i>"]
    BRANCH["Branch / call centre<br/><i>internal system</i>"]
    STO["Standing order<br/><i>scheduler inside the bank</i>"]

    APP & CORP & TPP & BRANCH & STO --> HUB["Payment hub"]
    HUB -->|"pacs.008 — always"| OUT["The interbank rails"]
```

A retail app posts JSON to the bank's own internal API; the payment hub builds
the payment in the bank's model directly. `pain.001` belongs to the corporate
channel — payroll runs, supplier batches, an ERP shipping a file over EBICS.
PSD2 payment initiation is REST again, by a third party.

**The channel is the bank's own business; the rails are not.** Whatever the
front door, everything converges on the payment hub and leaves as `pacs.008`.
That is the leg the rulebooks govern, and it is the same for all five.

## An instant credit transfer

```mermaid
sequenceDiagram
    participant C as Payer
    participant DB as Debtor's bank
    participant TIPS as TIPS
    participant CB as Creditor's bank
    participant B as Payee

    C->>DB: initiates — usually REST from the app
    DB->>TIPS: pacs.008
    TIPS->>CB: pacs.008
    Note over CB: sanctions + account checks
    CB-->>TIPS: pacs.002 accept
    Note over TIPS: move funds between the two banks'<br/>prefunded balances — final, immediately
    TIPS-->>DB: pacs.002 confirm
    DB-->>C: confirmed
    CB->>B: credit
```

No batch, no cycle, no netting, no T2 step — TIPS holds prefunded balances and
moves between them. Whole round trip inside 10 seconds, 24/7/365.

Since **9 October 2025** (Instant Payments Regulation, EU 2024/886) euro-area
PSPs must both send and receive instant euro credit transfers. Reachability via
TIPS or RT1 is no longer optional. RT1, note, now holds its liquidity *in*
TIPS — the two are no longer independent.

## T2, and why netting exists at all

T2 is the Eurosystem's RTGS — **R**eal-**T**ime **G**ross **S**ettlement. *Gross*
means each payment settles on its own: immediately, individually, finally, with
no cycle to wait for and no way back.

It is not a large-value system by rule. There is no minimum amount; you could
send a euro through it. What keeps retail out is fees and good sense.

| | T2 · gross | STEP2 · deferred net |
| --- | --- | --- |
| Settles | per payment, instantly | per cycle, netted |
| Settlement risk | none — final on the spot | exposure builds until the cycle settles |
| Liquidity cost | high — the full amount, now | low — only the net |
| Volume | ~400k payments/day | tens of millions/day |
| Value | ~€2tn/day | small payments, huge count |

That table is the whole argument for having both. **RTGS buys away credit risk
by spending liquidity; netting does the reverse.** You cannot have both at once,
so Europe runs one of each and routes by what the payment needs.

T2's job is wider than large-value payments. It is the floor everything else
stands on:

- interbank large-value and urgent customer payments
- **ancillary system settlement** — STEP2's and SEPA-Clearer's net positions,
  CCP margin, CLS
- monetary policy operations with the central bank
- liquidity transfers out to the TIPS and T2S accounts

The second one has a wrinkle worth noticing: a *net* figure moved by a *gross*
mechanism. STEP2 does the netting; T2 then makes one indivisible, final
movement of the result. Netting is an arrangement between banks, and it is not
money until T2 says so.

Since the March 2023 consolidation, "T2" is two components with accounts
hanging off them:

```mermaid
flowchart TB
    subgraph T2P["T2 platform"]
        CLM["CLM<br/><i>Central Liquidity Management</i><br/>holds the Main Cash Account"]
        RTGS["RTGS<br/>gross settlement of payments<br/>and of ancillary systems"]
    end

    DCA["<b>Dedicated Cash Accounts</b><br/>funded from the MCA<br/><i>TIPS DCA — instant, 24/7/365</i><br/><i>T2S DCA — securities settlement</i>"]

    CLM <-->|"intraday liquidity"| RTGS
    CLM <-->|"liquidity transfers"| DCA
```

Liquidity moves between these during the day, and a bank has to fund each one
for what it expects to need there. **T2 closes** — business days only, with a
nightly maintenance window. TIPS never does. That difference is the whole
reason instant needed a separate system instead of a faster T2: you cannot
promise a payer ten seconds on a Sunday from a system that is shut.

## Who pushes the button at the end of a cycle

Not the banks, and not T2. **The CSM does**, acting in T2's formal role of
*ancillary system*.

What makes that possible is a mandate granted at onboarding: joining a CSM
includes authorising it to debit your RTGS account for whatever net position
the cycle turns out to produce. You agree in advance to be debited by an amount
nobody knows yet. Without that, a third party instructing a movement on your
central bank account would be unthinkable.

```mermaid
sequenceDiagram
    participant BK as Participant banks
    participant AS as CSM<br/>(ancillary system)
    participant T2 as T2

    Note over AS: input cutoff — no more files
    Note over AS: compute multilateral net positions,<br/>one figure per participant
    AS->>BK: your position for this cycle
    Note over BK,AS: information period —<br/>the only window to react
    BK->>T2: fund the account if short
    AS->>T2: settlement instruction over the ASI
    Note over T2: all-or-nothing across every participant,<br/>usually via the CSM's technical account
    T2-->>AS: settled
    T2-->>BK: camt.054 per movement
    AS->>BK: output files released
```

Three properties of that picture are load-bearing:

- **The information period exists so a bank can fund.** It is told its position
  *before* the debit, not after. A treasury desk that ignores this discovers
  the problem at cutoff, when nothing can be done.
- **All-or-nothing.** The cycle settles entirely or not at all. There is no
  half-settled cycle and no per-payment failure at this stage.
- **A technical account.** The CSM typically holds one in T2: debits collect
  in, credits pay out, and it nets to zero. That is the mechanism that makes
  the batch atomic.

If a participant cannot fund, the rulebook decides what happens — an extended
window, a guarantee fund, or removal of the failing participant and
recomputation of everyone's position. The detail varies by CSM; the
all-or-nothing property does not.

For the SEPA-Clearer, **Bundesbank appears on both sides of this diagram** — as
the ancillary system instructing settlement, and as the NCB providing the
account being instructed. Two of the three hats, in one cycle, and still two
separate systems.

One caveat: TARGET2 numbered its ancillary system settlement procedures 1–6 and
the 2023 consolidation restructured them. The current labels are in the T2
UDFS; they are not reproduced here because getting them subtly wrong is worse
than not stating them.

## How a bank sees its central bank balance

Not from Bundesbank, strictly — **from T2**. The NCB provides the account and
the relationship; the platform generates the reporting. The same split as the
three hats.

It arrives over **ESMIG** in A2A mode, through the bank's network service
provider, as ISO 20022 `camt` messages. Three mechanisms, and a real treasury
operation uses all three:

```mermaid
flowchart LR
    T2["T2<br/><i>CLM · RTGS</i>"]

    T2 -->|"push, per movement"| P1["camt.054<br/><i>debit / credit notification</i>"]
    T2 -->|"push, end of day"| P2["camt.053<br/><i>statement</i>"]
    T2 -->|"pull, on demand"| P3["camt.003 → camt.004<br/><i>GetAccount → ReturnAccount</i>"]
    T2 -->|"threshold breached"| P4["floor / ceiling alert<br/><i>optional automatic<br/>liquidity transfer</i>"]

    P1 & P2 & P3 & P4 --> LM["The bank's<br/>liquidity management"]
```

| Message | When | What it is |
| --- | --- | --- |
| `camt.054` | per movement, pushed | debit / credit notification — the real-time one |
| `camt.052` | intraday, scheduled or on demand | account report |
| `camt.053` | end of day, pushed | statement of the account |
| `camt.003` → `camt.004` | on demand | balance query and its answer |

**Floor and ceiling** are worth knowing about. CLM lets a participant set
thresholds on an account; breaching one raises a notification and can trigger a
rule-based liquidity transfer that tops the account up or sweeps it down
without anyone intervening. This is the mechanism that stops a bank finding out
at cutoff that it cannot fund a settlement.

Two things that catch people out:

- **Reporting is per account, not per institution.** The MCA in CLM, the RTGS
  DCA, the TIPS DCA, the T2S DCA — each is subscribed to and reported
  separately. A bank watching only its MCA can be blind to its TIPS balance
  draining overnight, which matters precisely because TIPS runs when nothing
  else does.
- **A received payment is itself a notification.** When another bank sends a
  `pacs.009`, that message *is* the news that the balance moved. The `camt.054`
  confirms it against the account, but the payment got there first.

There is also **U2A** — the ESMIG portal, a screen for humans. Useful to an
operator investigating something, never how the bank's systems learn.

## How it moves: files, messages, and EBICS

There is no message queue between a bank and its bulk clearer. The split runs
exactly along bulk versus instant:

```mermaid
flowchart LR
    subgraph INSIDE["Inside the bank — message-oriented all day"]
        CORE["Core banking"] --> HUB["Payment hub"]
        SCR["Sanctions · fraud<br/>screening"] <--> HUB
    end

    HUB -->|"accumulate until cutoff"| FILE["Batch file<br/>thousands of pacs.008"]
    FILE -->|"EBICS · polled, no push"| CSM["STEP2 · SEPA-Clearer<br/><i>file-based, cyclical</i>"]

    HUB -->|"one payment, one message"| MSG["Single pacs.008"]
    MSG -->|"ESMIG A2A · real-time"| TIPS["TIPS · RT1<br/><i>message-based, 24/7</i>"]
```

- **Bulk** is store-and-forward file transfer against a cutoff clock. EBICS has
  no push at all — the bank *polls* for its downloads.
- **Instant** is genuinely message-oriented, request and response, reached
  through **ESMIG** (Eurosystem Single Market Infrastructure Gateway) in A2A
  mode over a network service provider — SWIFT or Nexi/SIAnet. It has to be:
  the payer is standing there waiting.
- **T2 is message-based too.** Gross settlement is real-time by definition. The
  batch-ness belongs to the CSM, never to T2.

Two qualifications. The transport under bulk is queue-*like*: SWIFT FIN and
FileAct in store-and-forward mode hold messages until the receiver collects
them, with delivery guarantees and non-repudiation. And the message queue you
expect **does** exist — inside the bank. IBM MQ or Kafka carries individual
payments between core banking, the payment hub and screening all day. The
payment hub is the impedance matcher: messages on one side, a file at cutoff on
the other.

### EBICS

**E**lectronic **B**anking **I**nternet **C**ommunication **S**tandard. A
file-transfer protocol, not a payment format — the envelope, where pain/pacs/camt
are the letter inside.

German in origin: it replaced BCS-FTAM in 2008 under the DFÜ-Abkommen. France
adopted it in 2010 in place of ETEBAC, Switzerland from 2012, Austria too. It is
now governed by **EBICS SCRL**, owned jointly by the German, French, Swiss and
Austrian banking associations.

XML over HTTPS, batch-oriented. The interesting part is that **its security does
not rest on TLS**. Each subscriber holds three RSA key pairs:

| Key | Purpose |
| --- | --- |
| `A006` | electronic signature — authorises the order |
| `E002` | encrypts the payload |
| `X002` | authenticates the request |

The file is signed and encrypted at the application layer, so breaking the
transport does not let anyone forge an order.

- **Enrolment (INI/HIA)** is deliberately half-offline. The customer sends
  public keys electronically, then *mails a paper letter* carrying the key
  hashes, hand-signed. The bank compares. One step that is not online, on
  purpose.
- **Distributed signature (VEU)** puts four-eyes in the protocol rather than in
  an application: one subscriber uploads, another authorises. Signature classes
  are `E` (may sign alone), `A`/`B` (needs a co-signer) and `T` — transport
  only, which may ship a file but authorise nothing.
- **Order types** were three-letter codes (`CCT` credit transfer, `CDD` direct
  debit, `STA`/`C53` statements, `HAC` acknowledgements). EBICS **3.0** (2021)
  replaced them with **BTF**, a structured descriptor instead of an opaque
  triple. France mandated 3.0; Germany is migrating.

EBICS is not PSD2/XS2A. Those are REST and OAuth, for third parties reaching
retail accounts. EBICS is corporate-to-bank and bank-to-gateway, file-based, and
much older.

## Returns and the other R-transactions

The rule that removes most of the mystery: **an R-transaction goes back the way
the original came.** No separate channel, no separate counterparty. Arrived via
STEP2, returns via STEP2.

```mermaid
flowchart LR
    A["Original<br/>pacs.008 / pacs.003"] --> B{"Where did it<br/>get stopped?"}
    B -->|"before settlement"| R1["<b>Reject</b><br/>pacs.002"]
    B -->|"after settlement,<br/>≤5 TARGET days"| R2["<b>Return</b><br/>pacs.004"]
    B -->|"debtor disputes an SDD,<br/>≤8 weeks authorised<br/>≤13 months unauthorised"| R3["<b>Refund</b><br/>pacs.004, MD06"]
    B -->|"creditor pulls back<br/>its own SDD"| R4["<b>Reversal</b><br/>pacs.007"]
    B -->|"sending bank wants<br/>a CT back"| R5["<b>Recall</b><br/>camt.056 → camt.029"]

    R1 & R2 & R3 & R4 & R5 --> OUT["same CSM,<br/>same gateway,<br/>opposite direction"]
```

Common reason codes: `AC01` account identifier incorrect, `AC04` account
closed, `AC06` account blocked, `AM04` insufficient funds, `MD01` no mandate,
`MD06` refund requested by debtor, `MD07` debtor deceased.

## The message set

| Message | Direction | What it is |
| --- | --- | --- |
| `pain.001` | customer → bank | credit transfer initiation |
| `pain.008` | customer → bank | direct debit collection order |
| `pain.002` | bank → customer | status of the above |
| `pacs.008` | bank → bank | the credit transfer itself |
| `pacs.003` | bank → bank | the direct debit collection |
| `pacs.002` | bank → bank | status / reject |
| `pacs.004` | bank → bank | return or refund |
| `pacs.007` | bank → bank | reversal |
| `camt.056` | bank → bank | cancellation request (recall) |
| `camt.029` | bank → bank | resolution of the above |
| `camt.053` | bank → customer | statement |
| `camt.054` | bank → customer | debit / credit notification |

`pain` = **pa**yment **in**itiation, customer-to-bank. `pacs` = **pa**yments
**c**learing and **s**ettlement, bank-to-bank. `camt` = **ca**sh
**m**anage**m**en**t**, reporting and exceptions.

## One bank's whole picture

A German bank with roughly a million customers, drawn as the set of connections
it actually operates:

```mermaid
flowchart TB
    BANK["<b>The bank</b><br/>~1M customers<br/>core banking + payment hub"]

    BANK -->|"adheres to rulebooks"| EPC["EPC"]
    BANK -->|"EBICS · bulk SCT + SDD"| CSM["SEPA-Clearer<br/><i>or</i> DZ Bank / Landesbank → STEP2"]
    BANK -->|"via a direct participant"| TIPS["TIPS<br/>instant"]
    BANK -->|"holds its RTGS account"| T2["T2<br/>via Bundesbank as NCB"]

    CSM -->|"net positions"| T2
    TIPS <-->|"liquidity"| T2

    style EPC stroke-dasharray: 5 5
```

Four relationships. One is paperwork (EPC). One is where the files go (the
CSM). One is instant. One is the account where money is actually held and
everything ultimately settles.

Everything else in this document is detail about which box goes in slot two.
