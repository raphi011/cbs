# Task 17 — admission becomes a conversation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `AddParticipantTx` stops writing three institutions' things in one unit
of work. The bank founds itself in its own book, the central bank opens the
settlement accounts in its own, the clearing house writes the routing entry in
its own — and the three learn of one another only by message.

**Architecture:** the return's topology, reused. The bank composes an
`acmt.007`; the clearing house **relays** it to the settlement agent and holds
nothing; the central bank opens one settlement account per asset, records its
own member row and answers `acmt.010`; the clearing house writes its routing
entry **from that acknowledgement** and forwards it; the bank records its
settlement references and becomes a member. `payment.Participant` — a
cross-entity aggregate whose `Assets[…].Settlement` is the central bank's
account id carried on the clearing house's roster row — is dissolved into three
rows, one per owning entity.

**Tech stack:** Go (no new dependencies), Postgres (`store/pg`) and the
in-memory store (`store/mem`) behind one conformance suite (`store/storetest`),
Next.js/TS for the teaching layers.

**Design:** `docs/superpowers/specs/2026-08-02-db-per-entity-design.md`, section
`## Admission` and its `### Task 17's shape, settled 2026-08-05`. Where this plan
and the spec disagree, **the spec wins and the plan is wrong.**

## Global constraints

- **Every measurement move is watched failing first.** For every behavioural
  claim: break the code, watch that exact test fail with that exact message,
  restore. A test whose failure nobody has observed is not evidence. When a
  mutation does *not* produce a failure, the assertion is wrong until proven
  otherwise — but so is the mutation.
- **Write the final prose from the code, not from this plan.** Every comment
  below is a *prediction* made before the code exists. Seven of the eight review
  rounds on Task 16's branch were about prose asserting something the code does
  not do, much of it inherited verbatim from that plan. Read what you actually
  wrote, then write the comment.
- **Do not grep to sweep a claim.** Task 16d was sent back twice for this. When a
  claim has to be swept, read whole every comment block whose subject is that
  claim and **report which files you read in full**, not which pattern you
  searched. A list of files read is checkable; a grep pattern is not.
- **A comment asserting a property is not a check of it.** `camt053.go` carried
  "the field order is the schema's sequence order and must not be changed"
  directly above the field that broke it, and every camt.053 the system emitted
  was invalid for a whole sub-project. Where a comment claims conformance to
  something external, either a test checks it or the comment says nobody has.
- **A count in a comment goes stale; a description of what happens does not.**
  Where a comment says "three", "six", "two of them", rewrite it as what happens.
  `iso20022/doc.go`'s message list opens with **"Six."** and this task makes it
  nine — do not increment it, rewrite it (17f).
- **A comment written during a transition announces its own expiry.** 17c's
  transitional composition must say "the composition Task 17d deletes", and 17d's
  reviewer is to treat any surviving "Task 17d will…" as a finding.
- **Both stores, every task that touches one.** `go test ./...` runs on
  `store/mem`; `TEST_DATABASE_URL=… go test ./...` runs the same suites on
  `store/pg`. `store/pg` must never accept or refuse a write differently from
  `store/mem`; `store/storetest` is what enforces it. **`make test-pg` uses
  Docker and does not work on this machine** — use the explicit
  `TEST_DATABASE_URL` invocation below. The Postgres run takes 20–39s on the
  store-touching packages against 1–5s on `store/mem`; **a fast run skipped, and
  a skip is not a pass.**
- **There is one migration.** Edit `store/pg/schema/0001_init.sql` in place. (This
  rule expires at Task 17.1, the SQLite swap — not here.)
- **Documentation is domain content and a correction is a sweep, not an edit.**
  `README.md` is authoritative; `web/src/components/hint-content.ts`, the 18 quiz
  chapters under `web/src/lib/quiz/chapters/` and the comments in
  `store/pg/schema/0001_init.sql` duplicate it by design and must agree with it
  and with the code.
- **A `[[wiki-link]]` to a key missing from `hint-content.ts` throws at runtime**
  and takes every route down while `next build` stays green. `npm run test`
  catches it in hint bodies *and* in quiz explanations. Run it, and load a page.
- **`web/src/lib/quiz/diversity.test.ts`** holds each chapter to 18–22 questions,
  ≥8 distinct `concept` tags, no tag more than 3×, all three difficulty tiers.
  **Chapter 9 is at 22 — the maximum.** A new question there means replacing one.
- **No test here can catch a web rendering regression.** Any web change needs a
  screenshot, and somebody has to look at it.
- **Never hand-write a backend path** in web code; use `cb()` / `csm()` /
  `bank(pid, …)`.
- **`rm -rf web/.next` before `npm run typecheck`.**
- **`api/mesh_test.go` hardcodes seed-allocated `dep_` ids.** 17c and 17e both
  rewrite the account-creation loop, so these will shift. Fix them by reading ids
  from the seed rather than by re-hardcoding; the handoff has carried this as a
  defect for two tasks.

- **The ISO 20022 schemas are on disk and the check is runnable.**
  `iso20022/testdata/xsd/` holds all ten, `xmllint` is installed, and
  `make test-schemas` turns every skip into a failure. **They are gitignored**
  — not this repository's to vendor — so they exist on this machine and not in a
  fresh clone. Any task touching `iso20022` runs `make test-schemas` and expects
  **no skips**; a skip means a schema went missing and the check did not run.

**Verification, run in full at the end of every task:**

```bash
gofmt -l . && go vet ./... && go build ./...
go test ./... -count=1
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./... -count=1
go test ./mesh/ ./api/ ./cmd/... -race -count=1
make test-schemas
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

**Workspace:**

```bash
git -C ~/Git/cbs fetch --all
git worktree add ~/Git/cbs-task-17 -b spec/task-17-admission main
cp -R ~/Git/cbs/iso20022/testdata/xsd ~/Git/cbs-task-17/iso20022/testdata/
```

The `cp` is not optional and not decoration: the schemas are gitignored, so a new
worktree has none, and `make test-schemas` in it would skip every subtest and
still print PASS.

Task 16 is merged; `main` is at `8819619` plus this sub-project's spec and plan
commits and the camt.053 schema fix at `e899066`. Everything below is against
that.

---

## File structure

| file | what changes |
|---|---|
| `iso20022/acmt.go` | **new.** `Acmt007`, `Acmt010`, `Acmt011` and the elements they share |
| `iso20022/acmt_test.go` | **new.** Round-trip, `validate()`, the choice constraints |
| `iso20022/testdata/acmt007.xml`, `acmt010.xml`, `acmt011.xml` | **new.** Golden files |
| `iso20022/xmllint_test.go` | the `files` map gains the three new documents |
| `iso20022/testdata/README.md` | the download list gains the three acmt schemas |
| `iso20022/doc.go` | the message list stops counting; the scope claim in the package's first sentence is reversed |
| `payment/bank.go` | **new**, from `participant.go`. `Bank`, `BankAccounts`, `BankStatus`, `AccountsFor`, `OpenCustomerAccount`, `RunEndOfDay` |
| `payment/roster.go` | **new.** `RosterEntry`, `SettlementMember` |
| `payment/participant.go` | **deleted** — its contents move to the two files above |
| `payment/store.go` | `PutParticipant`/`GetParticipant`/`ListParticipants` → `PutBank`/`GetBank`/`ListBanks`; six new methods for the two new rows |
| `payment/system.go` | `AddParticipantTx` becomes `FoundBankTx` + `OpenSettlementAccountTx` + `AdmitMemberTx` + `RecordMembershipTx`; `SettleCycleTx`, `SettleReturnTx`, `ReserveBalance`, `DepositTx` re-routed |
| `payment/translate.go` | `AdmissionRequest`/`AdmissionAcknowledgement` messages and their readers |
| `payment/errors.go` | `ErrBankNotFounded`, `ErrBICAlreadyAdmitted`, `ErrNotThisBanksAdmission`, `ErrSettlementMemberNotFound`, `ErrRosterEntryNotFound`, and their `reasonTable` entries |
| `store/mem/mem.go`, `store/mem/tx_payment.go` | the rename and the two new row kinds |
| `store/pg/tx_payment.go` | the same |
| `store/pg/schema/0001_init.sql` | `participants`→`banks`, `participant_assets`→`bank_assets` (+`status`), new `settlement_members`, `settlement_member_accounts`, `roster_entries` |
| `store/storetest/payment.go` | round-trip and scoping coverage for all three rows |
| `mesh/ops.go` | `bankOps`, `csmOps`, `settlementOps` gain admission; `GetParticipant` narrows to a roster entry |
| `mesh/mesh.go` | `Mesh.Admit`; `AddBank` becomes address reservation; `joinRoster` reads roster entries |
| `mesh/bank.go` | `apply` (the synchronous half's send) and `receiveAdmission` |
| `mesh/csm.go` | `relayAdmission` and `receiveAdmissionStatus` |
| `mesh/centralbank.go` | `receiveAdmission` |
| `mesh/books_test.go` | `TestWhichBooksAdmissionReaches`; `structCarriedBooks` entries; `TestFundingAReserveReachesTwoBooks` |
| `api/handlers_participant.go`, `api/dto_payment.go`, `api/server.go` | the handler answers 202-shaped; DTO gains `status`; `Reset` stops calling `JoinRoster` |
| `seed/seed.go` | `Populate` gains the mesh and admits through it |
| `README.md`, `web/src/components/hint-content.ts`, `web/src/lib/quiz/chapters/*.ts`, `web/src/lib/types.ts` | the domain content this adds |

---

## Task 17a: the acmt family

The three messages admission travels on. Nothing else in this task depends on
the domain, so it lands first and alone.

**Files:**
- Create: `iso20022/acmt.go`, `iso20022/acmt_test.go`
- Create: `iso20022/testdata/acmt007.xml`, `acmt010.xml`, `acmt011.xml`
- Modify: `iso20022/doc.go` (the first sentence at `:1`, and the `# Messages`
  list at `:72`)

**Interfaces produced:** `iso20022.Acmt007`, `Acmt010`, `Acmt011`, and
`AccountRequestReferences`, `AccountOwner`, `RequestedAccount`, `OpenedAccount`,
`AccountRejectionReason`.

`Acmt011` mirrors `Acmt010` and carries a reason instead of accounts:

```go
type Acmt011 struct {
	XMLName    xml.Name                 `xml:"urn:iso:std:iso:20022:tech:xsd:acmt.011.001.03 Document"`
	AcctReqRjctn AccountRequestRejection `xml:"AcctReqRjctn"`
}

type AccountRequestRejection struct {
	Refs       AccountRequestReferences `xml:"Refs"`
	AcctSvcrId BranchAndFinancialInstitution `xml:"AcctSvcrId"`
	Org        AccountOwner             `xml:"Org"`
	RjctnRsn   AccountRejectionReason   `xml:"RjctnRsn"`
}

// AccountRejectionReason is why the servicer will not open the account.
//
// Free text and not a code set, and that is a narrowing this repository makes
// rather than one the standard does — say so in the type doc. The refusals this
// system can produce (an asset nobody issues, a message it cannot read) have no
// entry in any account-management code set, and inventing one would be teaching
// a code that does not exist. Contrast iso20022.StatusReason, which is a real
// code set and is used as one.
type AccountRejectionReason struct {
	Rsn         string `xml:"Rsn"`
	AddtlInf    string `xml:"AddtlInf,omitempty"`
}
```

`Rsn` is validated as present; a rejection that says nothing is a rejection
nobody can act on.

### The element names are checkable now, and must be checked

**The schemas are on disk.** `iso20022/testdata/xsd/` holds all ten, including
`acmt.007.001.03.xsd`, `acmt.010.001.03.xsd` and `acmt.011.001.03.xsd`. They are
gitignored — not this repository's to vendor — so a fresh clone does not have
them and `testdata/README.md` says how to get them.

That changes this task's shape from what it would have been. The struct shapes in
Step 3 are this plan's **prediction**; the XSD is the authority, and it is
sitting there. Read it.

The reason to take that seriously is one week old. The first time the schema
check ever ran (`e899066`), it found that **every camt.053 this system had
emitted was invalid** on two counts: `AddtlNtryInf` six elements out of position
— under a comment asserting the field order was the schema's — and `BkTxCd`,
the one mandatory child of an entry, missing outright. Both shipped with Task 15
and survived a per-task review, a documentation sweep and a whole-branch review
with probes. **No probe finds this class of defect. Only the schema does.**

- [ ] **Step 1: Read the three schemas and write down the element paths**

```bash
python3 - <<'PY'
import re
for m in ["acmt.007.001.03", "acmt.010.001.03", "acmt.011.001.03"]:
    s = open(f"iso20022/testdata/xsd/{m}.xsd").read()
    root = re.search(r'<xs:element name="(\w+)" type="Document"', s)
    print(f"--- {m}  root element: {root.group(1) if root else '?'}")
PY
```

Then, for each message, extract the top-level `complexType`'s sequence the way
the camt.053 investigation did, and write into a scratch file the **exact**
element path and its `minOccurs` for:

1. the message root element under `<Document>`;
2. the reference block carrying the message identifier;
3. where the **account servicer** is named;
4. where the **account owner** is named, and where a BIC sits inside it;
5. where the requested **currency** sits;
6. where an **opened account's identifier** sits on the acknowledgement;
7. where a **rejection reason** sits on the rejection.

**Where the schema disagrees with this plan, the schema wins and the plan is
wrong.** Change the struct and say so in the type doc.

Two traps this task's own history has already sprung:

- **Every mandatory element must be in the struct.** `BkTxCd` was missed because
  nobody enumerated `minOccurs`. Enumerate it. An element with no `minOccurs`
  attribute is mandatory.
- **Sequence order is part of the schema and a comment claiming it is not
  evidence.** `camt053.go` carried exactly that comment above the field that
  broke it.

- [ ] **Step 2: Write the failing round-trip test**

`iso20022/acmt_test.go`:

```go
// TestAcmt007RoundTrips is the same shape as the other message round-trips: a
// document built in Go, marshalled, unmarshalled, and compared. It is what
// catches an element whose XML tag does not match the field it was written to.
func TestAcmt007RoundTrips(t *testing.T) {
	req := &Acmt007{AcctOpngReq: AccountOpeningRequest{
		Refs:       AccountRequestReferences{MsgId: "AURODEFFXXX-1", CreDtTm: ISODateTime{Time: testTime}},
		AcctSvcrId: NewAgent("CBLICBLIXXX"),
		Org:        AccountOwner{FullLglNm: "Aurora Bank", AnyBIC: "AURODEFFXXX"},
		Acct:       RequestedAccount{Ccy: []string{"EUR"}},
	}}
	raw, err := Marshal(Envelope{AppHdr: testHdr("acmt.007.001.03"), Document: req})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	env, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, ok := env.Document.(*Acmt007)
	if !ok {
		t.Fatalf("Unmarshal gave %T, want *Acmt007", env.Document)
	}
	if got.AcctOpngReq.Org.AnyBIC != "AURODEFFXXX" {
		t.Errorf("account owner BIC round-tripped as %q", got.AcctOpngReq.Org.AnyBIC)
	}
	if len(got.AcctOpngReq.Acct.Ccy) != 1 || got.AcctOpngReq.Acct.Ccy[0] != "EUR" {
		t.Errorf("requested currencies round-tripped as %v", got.AcctOpngReq.Acct.Ccy)
	}
}

// TestAcmt010CarriesOneAccountPerCurrency is the property the conversation
// rests on: the acknowledgement is what the CLEARING HOUSE writes its routing
// entry from and what the BANK learns its settlement references from, so an
// acknowledgement that named the accounts without their currencies would leave
// both readers guessing which is which.
func TestAcmt010CarriesOneAccountPerCurrency(t *testing.T) {
	ack := &Acmt010{AcctReqAck: AccountRequestAcknowledgement{
		Refs:       AccountRequestReferences{MsgId: "CBLICBLIXXX-1", CreDtTm: ISODateTime{Time: testTime}},
		AcctSvcrId: NewAgent("CBLICBLIXXX"),
		Org:        AccountOwner{FullLglNm: "Aurora Bank", AnyBIC: "AURODEFFXXX"},
		Acct: []OpenedAccount{
			{Id: "200.100.001", Ccy: "EUR"},
			{Id: "200.100.002", Ccy: "USD"},
		},
	}}
	raw, err := Marshal(Envelope{AppHdr: testHdr("acmt.010.001.03"), Document: ack})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	env, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := env.Document.(*Acmt010).AcctReqAck
	if len(got.Acct) != 2 || got.Acct[1].Ccy != "USD" || got.Acct[1].Id != "200.100.002" {
		t.Errorf("accounts round-tripped as %+v", got.Acct)
	}
}

// TestAcmt007WithNoCurrencyIsRefused pins the one element without which the
// settlement agent cannot act. A request naming no currency asks for no
// account, and answering it would mean opening an arbitrary one.
func TestAcmt007WithNoCurrencyIsRefused(t *testing.T) {
	req := &Acmt007{AcctOpngReq: AccountOpeningRequest{
		Refs:       AccountRequestReferences{MsgId: "AURODEFFXXX-1", CreDtTm: ISODateTime{Time: testTime}},
		AcctSvcrId: NewAgent("CBLICBLIXXX"),
		Org:        AccountOwner{FullLglNm: "Aurora Bank", AnyBIC: "AURODEFFXXX"},
	}}
	if _, err := Marshal(Envelope{AppHdr: testHdr("acmt.007.001.03"), Document: req}); !errors.Is(err, ErrMissingElement) {
		t.Fatalf("Marshal of a request with no currency: %v, want ErrMissingElement", err)
	}
}

// TestAcmt007WithNoOwnerBICIsRefused is the other one. The acknowledgement's
// route home and the roster entry's key are both this BIC; a request without it
// names no bank at all.
func TestAcmt007WithNoOwnerBICIsRefused(t *testing.T) {
	req := &Acmt007{AcctOpngReq: AccountOpeningRequest{
		Refs:       AccountRequestReferences{MsgId: "AURODEFFXXX-1", CreDtTm: ISODateTime{Time: testTime}},
		AcctSvcrId: NewAgent("CBLICBLIXXX"),
		Org:        AccountOwner{FullLglNm: "Aurora Bank"},
		Acct:       RequestedAccount{Ccy: []string{"EUR"}},
	}}
	if _, err := Marshal(Envelope{AppHdr: testHdr("acmt.007.001.03"), Document: req}); !errors.Is(err, ErrMissingElement) {
		t.Fatalf("Marshal of a request with no owner BIC: %v, want ErrMissingElement", err)
	}
}
```

Check `testHdr` and `testTime` against the existing message tests
(`iso20022/pacs004_test.go`, `camt053_test.go`) and use whatever those helpers
are actually called; do not add a second helper doing the same job.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./iso20022/ -run 'Acmt' -v`
Expected: FAIL — `undefined: Acmt007`.

- [ ] **Step 4: Write `iso20022/acmt.go`**

Follow `camt053.go`'s file shape exactly: namespace constant, `init` calling
`registerDocument`, the document type, `MessageDefinitionIdentifier`,
`namespace`, `validate`, then the body types each with their own `validate`.
Field order is the schema's sequence order and must not be reordered.

```go
const (
	acmt007Namespace = "urn:iso:std:iso:20022:tech:xsd:acmt.007.001.03"
	acmt010Namespace = "urn:iso:std:iso:20022:tech:xsd:acmt.010.001.03"
	acmt011Namespace = "urn:iso:std:iso:20022:tech:xsd:acmt.011.001.03"
)

// AccountRequestReferences is the message identifier and creation instant every
// message in this family carries.
type AccountRequestReferences struct {
	MsgId   string      `xml:"MsgId"`
	CreDtTm ISODateTime `xml:"CreDtTm"`
}

// AccountOwner is the institution asking for the account: its legal name and
// its BIC.
//
// The BIC is the load-bearing element and the name is not. This message is
// carried to a settlement agent that holds no roster, and the BIC is what it
// keys its member record by, what the clearing house keys its routing entry by,
// and what the acknowledgement is addressed back on.
type AccountOwner struct {
	FullLglNm string `xml:"FullLglNm"`
	AnyBIC    BIC    `xml:"AnyBIC"`
}

// RequestedAccount is which currencies the applicant wants an account in.
//
// A list, because a bank clearing a euro and a dollar scheme needs a settlement
// account in each: those are two accounts and not one account holding two
// currencies, for the reason payment.BankAccounts gives about its own four.
type RequestedAccount struct {
	Ccy []string `xml:"Ccy"`
}

// OpenedAccount is one account the servicer opened, and the currency that says
// which one it is.
type OpenedAccount struct {
	Id  string `xml:"Id"`
	Ccy string `xml:"Ccy"`
}
```

Each `validate()` returns `fmt.Errorf("%w: <path>", ErrMissingElement)` for the
elements the tests above pin. **Only those**, plus whatever the fetched document
in Step 1 marks mandatory — an element the standard leaves optional is not made
mandatory here without a sentence saying who requires it, which is the rule
`Document.validate`'s own doc states.

The type doc on `Acmt007` must carry, in this order:

1. what the message is;
2. **that this use of it is not how the real thing works.** `acmt` is eBAM,
   designed for a corporate opening an account at its bank. A bank's RTGS account
   at its central bank is created through reference data — CRDM static data and
   `reda` messages in TARGET — not through eBAM. This system models admission as a
   conversation because the sequence and the ownership are what it teaches, and
   this is the closest true family rather than the actual one;
3. that scheme membership is contractual in life and is not messaged at all, so
   what travels here is the settlement-account request and the routing entry
   falls out of its acknowledgement;
4. what is deliberately omitted and legal in the standard, element by element, in
   the form `camt053.go` uses.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./iso20022/ -run 'Acmt' -v`
Expected: PASS.

- [ ] **Step 6: Write the golden files and validate them for real**

`iso20022/testdata/acmt007.xml`, `acmt010.xml`, `acmt011.xml`, following the
existing goldens' shape. Check how `iso20022/golden_test.go` discovers them —
whether it walks the directory or holds a list — and register them if it holds a
list.

Three places must gain the new messages, and missing any one of them is a check
that silently does not run:

1. the `files` map in `iso20022/xmllint_test.go` (`doc → schema`);
2. the download list in `iso20022/testdata/README.md` — that list was missing
   `camt.053.001.08.xsd` for a whole task because nobody kept it in step with the
   map, and the README now says in bold that a message added to the map is a line
   added there;
3. whatever `golden_test.go` uses to find goldens.

Then run it as a **required** check:

```bash
make test-schemas
```

Expected: PASS, with subtests for all nine documents and their headers, and **no
skips**. A skip here means a schema is missing from `testdata/xsd/` and the check
did not run.

- [ ] **Step 7: Reverse the package's scope claim in `doc.go`**

Two edits, and they are both reversals rather than additions.

The first sentence says the package *"implements the SEPA interbank messages of
the ISO 20022 standard"*. `acmt` is neither SEPA nor interbank, and the
package's whole "the standard is a superset and a scheme narrows it" framing
does not apply to it — the EPC profiles no part of the family. Rewrite the
sentence, and add a paragraph saying which claims in the file are the scheme's
and which are now only the standard's.

The `# Messages` list opens with **"Six."** Rewrite it as a description of what
the messages are for rather than a count. Do not write "Nine."

- [ ] **Step 8: Run the full verification and commit**

```bash
gofmt -l . && go vet ./... && go build ./... && go test ./... -count=1
git add iso20022/ && git commit -m "feat(iso20022): the acmt family, and the scope claim it reverses"
```

---

## Task 17b: the three rows

`payment.Participant` is dissolved. This task is types and storage only: at the
end of it nothing behaves differently and every existing test passes, which is
what makes it reviewable on its own.

**Files:**
- Create: `payment/bank.go`, `payment/roster.go`
- Delete: `payment/participant.go`
- Modify: `payment/store.go`, `payment/system.go`, `payment/errors.go`
- Modify: `store/mem/mem.go`, `store/mem/tx_payment.go`, `store/pg/tx_payment.go`
- Modify: `store/pg/schema/0001_init.sql`
- Modify: `store/storetest/payment.go`
- Modify: `mesh/ops.go`
- Test: `store/storetest/payment.go`, `payment/system_test.go`

**Interfaces produced:**

```go
// payment/bank.go
type BankStatus string

const (
	BankFounded BankStatus = "Founded"
	BankMember  BankStatus = "Member"
)

type BankAccounts struct {
	Suspense          ledger.AccountID
	Reserve           ledger.AccountID
	Unclaimed         ledger.AccountID
	ReturnsReceivable ledger.AccountID
	Settlement        ledger.AccountID
}

type Bank struct {
	ID                ParticipantID
	Name              string
	BIC               iso20022.BIC
	BookID            ledger.BookID
	CustomerSubledger ledger.SubledgerID
	ProductID         product.ID
	Status            BankStatus
	Assets            map[ledger.AssetCode]BankAccounts
	CreatedAt         time.Time

	Ledger    *ledger.Book       `json:"-"`
	Deposit   *deposit.Register  `json:"-"`
	Lending   *lending.Portfolio `json:"-"`
	Catalogue *product.Catalogue `json:"-"`
}

func (b *Bank) AccountsFor(asset ledger.AssetCode) (BankAccounts, error)
func (b *Bank) OpenCustomerAccount(ctx context.Context, name string, asset ledger.AssetCode) (deposit.Account, error)
func (b *Bank) RunEndOfDay(ctx context.Context, date time.Time) error

// payment/roster.go
type SettlementMember struct {
	BIC      iso20022.BIC
	Name     string
	Accounts map[ledger.AssetCode]ledger.AccountID
	OpenedAt time.Time
}

type RosterEntry struct {
	BIC        iso20022.BIC
	Name       string
	Assets     []ledger.AssetCode
	AdmittedAt time.Time
}

// payment/store.go — replacing the three participant methods
PutBank(ctx context.Context, b Bank) error
GetBank(ctx context.Context, id ParticipantID) (Bank, error)
ListBanks(ctx context.Context) ([]Bank, error)

PutSettlementMember(ctx context.Context, m SettlementMember) error
GetSettlementMember(ctx context.Context, bic iso20022.BIC) (SettlementMember, error)
ListSettlementMembers(ctx context.Context) ([]SettlementMember, error)

PutRosterEntry(ctx context.Context, e RosterEntry) error
GetRosterEntry(ctx context.Context, bic iso20022.BIC) (RosterEntry, error)
ListRosterEntries(ctx context.Context) ([]RosterEntry, error)
```

### `ParticipantID` keeps its name, and that is a decision

The id type is on `Payment.Debtor.Participant`, on every API path, in
`storetest`, and in the web types. Renaming it is a mechanical diff across the
whole repository for no behavioural change, and it would bury this task's real
content. It names a bank; the row it keys is now called `Bank`; Task 18 may
rename it when the rows move. **Write that down where the type is declared** —
an unexplained mismatch between `ParticipantID` and `Bank` is exactly the kind of
thing the next reader takes for an oversight.

- [ ] **Step 1: Write the failing storetest cases**

In `store/storetest/payment.go`, beside the existing participant cases. Read the
file first: it has a house shape for these (`assertEqual`, `ids`, `assertOrder`)
and a registry of case functions that new cases must be added to.

```go
// BankRoundTripsAndDropsLiveHandles is ParticipantRoundTripsAndDropsLiveHandles
// renamed, plus Status. The handles are still dropped for the reason they always
// were: a Store returns a Bank with Ledger, Deposit, Lending and Catalogue nil,
// and the Network binds them on the way out.
//
// Status is checked explicitly because it is the field with no default that is
// safe: a Bank read back with Status "" is neither Founded nor a Member, and the
// two readers that care — Mesh.Submit and the clearing house's routing — would
// both take the wrong branch.

// SettlementMemberIsKeyedByBIC is the central bank's own record, and the point
// of the case is the key. The settlement agent holds no roster and no
// participant ids; what it is told in an acmt.007 is a BIC, so a lookup by
// anything else is a lookup it could not make.

// SettlementMemberKeepsOneAccountPerAsset pins that the map survives the round
// trip with its keys. A member read back with an empty map settles nothing, and
// under store/pg that is a second table.

// RosterEntryCarriesNoAccountIdentifiers is the case that makes the split a
// claim about the code. The clearing house routes; it has no business holding a
// bank's subledger, its product, or its account at the central bank. If a field
// like that ever appears on RosterEntry this case has to be deleted to compile,
// which is the point.
```

Write these as real cases in the file's existing style, with a
`RosterEntryCarriesNoAccountIdentifiers` that constructs a `RosterEntry`,
round-trips it, and asserts on the **struct's field set** via a table of the
field names it is allowed to have — a reflection check, so that adding a field
fails the case rather than passing it silently.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./store/... -count=1`
Expected: FAIL — `undefined: payment.Bank`.

- [ ] **Step 3: Create `payment/bank.go` and `payment/roster.go`; delete `participant.go`**

Move the doc comments across rather than rewriting them; they are domain content
and most of them are still true. Three that are **not** and must change:

- `Participant`'s doc says "The books all live in the network's single store,
  told apart by BookID — which is what lets one transaction span several banks".
  That is the property this sub-project is removing. Say what is true today and
  name what removes it.
- `ParticipantAccounts`'s doc says the fifth account "is the central bank's row
  for it, and is named here because settlement needs both ends". After this task
  settlement does **not** read it here — the central bank reads its own member
  row. What it is now is the account holder's record of its own account number,
  and `DepositTx` is the one reader.
- `AccountsFor`'s doc is unchanged and correct; check it rather than assume it.

`SettlementMember` and `RosterEntry` each need a doc saying **which entity owns
it and which store it moves to at Task 18**, because that is the whole reason
they exist as separate rows while there is still one store underneath.

- [ ] **Step 4: Rename the store methods and add the six new ones**

`payment/store.go`, then `store/mem/tx_payment.go`, `store/mem/mem.go`
(`kindParticipant` → `kindBank`, plus `kindSettlementMember` and
`kindRosterEntry`; the `insertSeq`/`sortRows` ordering machinery applies to all
three), then `store/pg/tx_payment.go`.

`copyParticipant` becomes `copyBank`; the two new rows carry a map and a slice
respectively and need copies of their own, for the reason the file already
states — a stored row handed back by reference is one a caller can mutate
in place.

- [ ] **Step 5: Edit `0001_init.sql` in place**

`participants` → `banks` with a `status TEXT NOT NULL`; `participant_assets` →
`bank_assets`; two new tables for the settlement member and its per-asset
accounts; one for the roster entry.

The comments in this file are domain content, not implementation notes. Four
things must be said, and the last two are new claims about the schema:

1. `banks.status`: the two values and why there is no third — an `Applied` state
   would be a field nothing reads.
2. `bank_assets.settlement`: it is the bank's record of its account **in another
   institution's book**, which is why it is the one column here that names an id
   this table's owner did not allocate.
3. `settlement_members` is keyed by **BIC and not by a bank id**, because the
   settlement agent holds no roster and is told a BIC.
4. `roster_entries` carries **no account identifier of any kind**, and the
   comment should say what would be wrong if it did.

Follow the sub-project's rule for this file: write the **reasoning**, not the
Postgres mechanism, wherever there is a choice. Task 17.1 translates this file
whole into SQLite and the spellings will not survive; the arguments will.

- [ ] **Step 6: Narrow `GetParticipant` in `mesh/ops.go`**

This is the crossing `ops.go` names about itself:

> GetParticipant is the hole … *payment.Network binds live handles onto what it
> returns (Network.bind), so the value carries another bank's Ledger, Deposit and
> Catalogue with it. … closing it needs a narrower return, which is payment's to
> give and sub-project 8's to want.

Replace it on `bankOps` and `csmOps` with

```go
GetRosterEntry(ctx context.Context, bic iso20022.BIC) (payment.RosterEntry, error)
```

and change the callers. `csm` uses it to address a status back to the bank that
submitted a payment, which needs a BIC and a name; `bank` uses it to decide
whether a status is about a payment whose payer banks elsewhere, which needs a
BIC. Neither needs a ledger handle, which is exactly the point.

Where a caller has a `ParticipantID` and needs a BIC, it goes through the roster
by way of the bank row — **and that is a crossing this task does not close.**
Write it down beside the call; do not quietly launder it.

Rewrite the two long notes in `ops.go` that describe the hole. They are the
file's most-cited passages and they will be false the moment this compiles;
leaving them is the exact defect seven of Task 16's eight review rounds were
about.

- [ ] **Step 7: Keep `api`, `seed` and `web` compiling, and nothing more**

The rename breaks `api/dto_payment.go`, `api/handlers_participant.go`,
`seed/seed.go` and every `*payment.Participant` in a signature. Make the
**mechanical** edits only: the type name, the method name, the field access.

**This is the 17b/17e boundary and it is stated so that neither task assumes the
other owns it.** 17b owns `payment` and `store`; 17e owns the rewiring the
conversation makes necessary — the handler's new answer, the DTO's new field, the
seed's admission, the web types. If a change here needs a decision rather than a
rename, it belongs to 17e; leave it and say so.

- [ ] **Step 8: Run everything, both stores**

```bash
go test ./... -count=1
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./... -count=1
```

Expected: PASS on both, **with no test edited** — this task changes no
behaviour. The Postgres run must take 20–39s on the store-touching packages. **If
it is fast, it skipped, and a skip is not a pass.**

- [ ] **Step 9: Watch the new cases fail**

For each of the four storetest cases: break the code it covers, run it, read the
failure message, restore. `RosterEntryCarriesNoAccountIdentifiers` is watched by
adding a `Settlement ledger.AccountID` field to `RosterEntry` and confirming the
reflection check fails by name.

- [ ] **Step 10: Commit**

```bash
git add -A && git commit -m "refactor(payment): one row per entity, and ops.go's hole closes with it"
```

---

## Task 17c: the three acts

The domain side of the conversation: four functions where there was one. No
messages yet — `AddParticipantTx` stays alive as a composition of them, so the
branch builds and every existing admission test becomes a check that the acts
compose to what the atomic version did.

**Files:**
- Modify: `payment/system.go` (`AddParticipantTx` at `:405`, `SettleCycleTx` at
  `:795`, `DepositTx` at `:578`, `ReserveBalance` at `:2955`, `SettleReturnTx` at
  `:2302`)
- Modify: `payment/errors.go`
- Test: `payment/system_test.go`

**Interfaces produced:**

```go
// The bank's own act. Its book, its chart of accounts, its default product, its
// row. Status is BankFounded. It writes nothing outside its own book except the
// network-scoped id allocation and audit event every row here needs.
func (s *Network) FoundBankTx(ctx context.Context, tx Tx, name string, bic iso20022.BIC, assets []ledger.AssetCode) (*Bank, error)

// The central bank's own act. One settlement account per asset in
// CentralBankBook, and its own member row keyed by BIC. Idempotent per BIC: a
// second call for a BIC it already holds returns the accounts it already
// opened and opens none.
func (s *Network) OpenSettlementAccountTx(ctx context.Context, tx Tx, in AdmissionRequest) (SettlementMember, error)

// The clearing house's own act. The routing entry, written from what the
// acknowledgement says. Refuses a BIC already in the roster.
func (s *Network) AdmitMemberTx(ctx context.Context, tx Tx, in AdmissionAcknowledgement) (RosterEntry, error)

// The bank's second act: recording what it was told. Status becomes BankMember
// and the settlement references land on its own row.
func (s *Network) RecordMembershipTx(ctx context.Context, tx Tx, by ParticipantID, in AdmissionAcknowledgement) (*Bank, error)
```

`AdmissionRequest` and `AdmissionAcknowledgement` are 17d's message readers;
declare them here as plain structs (`Name`, `BIC`, `Assets` /
`Name`, `BIC`, `Accounts map[ledger.AssetCode]ledger.AccountID`) so this task can
be tested without any XML, and have 17d add the readers that produce them.

### Which act may refuse, and where

| act | refuses | sentinel |
|---|---|---|
| `FoundBankTx` | a malformed BIC, an unknown asset code, a repeated asset code | the existing ones |
| `OpenSettlementAccountTx` | an unknown asset code | the existing one |
| `AdmitMemberTx` | a BIC already in the roster | `ErrBICAlreadyAdmitted` |
| `RecordMembershipTx` | a bank that is not `BankFounded`; an acknowledgement whose BIC is not this bank's | `ErrBankNotFounded`, `ErrNotThisBanksAdmission` |

`RecordMembershipTx`'s second refusal is the one worth writing a test for
first — it is `PostSettlementAdviceTx`'s `ErrStatementNotForThisBank` in a new
costume, and the reason is the same: the actor passes its own id, so nothing in
the signature stops a caller naming another bank's.

Add each new sentinel to `payment`'s `reasonTable`. Read that table's own doc
before choosing a code: a sentinel classified as never reaching a counterparty
becomes a dead letter rather than an answer, and getting that wrong is how a
refusal reaches nobody at all.

- [ ] **Step 1: Write the failing tests for the three acts in isolation**

```go
// TestFoundingABankTouchesNoOtherInstitution is the whole claim of this task,
// made about the first act. A founded bank has a book, a chart of accounts and a
// product; the central bank has opened it nothing and the roster has never heard
// of it.
func TestFoundingABankTouchesNoOtherInstitution(t *testing.T) {
	sys, ctx := newTestNetwork(t)
	var b *payment.Bank
	if err := sys.Store().Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		var err error
		b, err = sys.FoundBankTx(ctx, tx, "Aurora Bank", "AURODEFFXXX", euroOnly)
		return err
	}); err != nil {
		t.Fatalf("FoundBankTx: %v", err)
	}
	if b.Status != payment.BankFounded {
		t.Errorf("a founded bank has status %q, want %q", b.Status, payment.BankFounded)
	}
	if got := b.Assets["EUR"].Settlement; got != "" {
		t.Errorf("a founded bank names settlement account %q; it has not asked for one yet", got)
	}
	if err := sys.Store().View(ctx, func(ctx context.Context, tx payment.Tx) error {
		if _, err := tx.GetSettlementMember(ctx, "AURODEFFXXX"); !errors.Is(err, payment.ErrSettlementMemberNotFound) {
			t.Errorf("the central bank has a member row for a bank that only founded itself: %v", err)
		}
		if _, err := tx.GetRosterEntry(ctx, "AURODEFFXXX"); !errors.Is(err, payment.ErrRosterEntryNotFound) {
			t.Errorf("the clearing house has a routing entry for a bank that has not applied: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}
}

// TestOpeningASettlementAccountTwiceOpensOne is what makes a retried admission
// safe. The mesh delivers exactly once, so this is not reachable through the
// transport — it is reachable through the OPERATOR, who re-drives an admission
// that failed after the accounts were opened, and that is this system's own
// documented route out of a half-happened admission.
func TestOpeningASettlementAccountTwiceOpensOne(t *testing.T) {
	sys, ctx := newTestNetwork(t)
	in := payment.AdmissionRequest{Name: "Aurora Bank", BIC: "AURODEFFXXX", Assets: euroOnly}
	var first, second payment.SettlementMember
	mustUpdate(t, sys, ctx, func(ctx context.Context, tx payment.Tx) (err error) {
		first, err = sys.OpenSettlementAccountTx(ctx, tx, in)
		return err
	})
	mustUpdate(t, sys, ctx, func(ctx context.Context, tx payment.Tx) (err error) {
		second, err = sys.OpenSettlementAccountTx(ctx, tx, in)
		return err
	})
	if first.Accounts["EUR"] != second.Accounts["EUR"] {
		t.Errorf("a second request opened a second account: %q then %q", first.Accounts["EUR"], second.Accounts["EUR"])
	}
	// And the central bank's book holds ONE, which the ids above would not catch
	// if the second call created an account and then returned the first's id.
	assertCentralBankReserveAccountCount(t, sys, ctx, "Aurora Bank", 1)
}

// TestAdmittingABICTwiceIsRefused is the address clash, decided by the entity
// that owns routing. The mesh's actor map refuses a taken address too; that one
// is a statement about connectivity, and this is the statement about membership.
func TestAdmittingABICTwiceIsRefused(t *testing.T) {
	sys, ctx := newTestNetwork(t)
	ack := payment.AdmissionAcknowledgement{
		Name: "Aurora Bank", BIC: "AURODEFFXXX",
		Accounts: map[ledger.AssetCode]ledger.AccountID{"EUR": "200.100.001"},
	}
	mustUpdate(t, sys, ctx, func(ctx context.Context, tx payment.Tx) error {
		_, err := sys.AdmitMemberTx(ctx, tx, ack)
		return err
	})

	clash := ack
	clash.Name = "Impostor Bank"
	clash.Accounts = map[ledger.AssetCode]ledger.AccountID{"EUR": "200.100.009"}
	err := sys.Store().Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		_, err := sys.AdmitMemberTx(ctx, tx, clash)
		return err
	})
	if !errors.Is(err, payment.ErrBICAlreadyAdmitted) {
		t.Fatalf("admitting a second bank on a taken BIC: %v, want ErrBICAlreadyAdmitted", err)
	}
	// And the roster still says what it said. A refusal that overwrote the entry
	// and then reported failure would leave routing pointing at the impostor.
	if err := sys.Store().View(ctx, func(ctx context.Context, tx payment.Tx) error {
		e, err := tx.GetRosterEntry(ctx, "AURODEFFXXX")
		if err != nil {
			return err
		}
		if e.Name != "Aurora Bank" {
			t.Errorf("the roster entry now names %q; the refusal overwrote it", e.Name)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}
}

// TestABankCannotRecordAnotherBanksMembership is ErrStatementNotForThisBank's
// shape, one flow over. The actor passes its own id and the domain refuses the
// one that is not its business, because nothing in the signature can.
func TestABankCannotRecordAnotherBanksMembership(t *testing.T) {
	sys, ctx := newTestNetwork(t)
	var aurora, verde *payment.Bank
	mustUpdate(t, sys, ctx, func(ctx context.Context, tx payment.Tx) (err error) {
		if aurora, err = sys.FoundBankTx(ctx, tx, "Aurora Bank", "AURODEFFXXX", euroOnly); err != nil {
			return err
		}
		verde, err = sys.FoundBankTx(ctx, tx, "Banca Verde", "VERDITMMXXX", euroOnly)
		return err
	})

	// The acknowledgement is Aurora's; Verde tries to record it as its own.
	ack := payment.AdmissionAcknowledgement{
		Name: "Aurora Bank", BIC: aurora.BIC,
		Accounts: map[ledger.AssetCode]ledger.AccountID{"EUR": "200.100.001"},
	}
	err := sys.Store().Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		_, err := sys.RecordMembershipTx(ctx, tx, verde.ID, ack)
		return err
	})
	if !errors.Is(err, payment.ErrNotThisBanksAdmission) {
		t.Fatalf("recording another bank's admission: %v, want ErrNotThisBanksAdmission", err)
	}
	// Neither bank moved. Verde is not a member, and Aurora did not become one
	// because somebody else read its acknowledgement.
	for _, want := range []*payment.Bank{aurora, verde} {
		got := mustGetBank(t, sys, ctx, want.ID)
		if got.Status != payment.BankFounded {
			t.Errorf("%s is %q after a refused recording, want %q", got.Name, got.Status, payment.BankFounded)
		}
		if got.Assets["EUR"].Settlement != "" {
			t.Errorf("%s now names settlement account %q", got.Name, got.Assets["EUR"].Settlement)
		}
	}
}
```

Write `assertCentralBankReserveAccountCount` as a helper that lists
`CentralBankBook` and counts accounts whose name contains the bank's name. Check
`payment/system_test.go` for the existing helpers (`setupTwoBanks`,
`newTestNetwork`) and use those rather than adding parallel ones.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./payment/ -run 'Found|Settlement|Admitting|Membership' -v`
Expected: FAIL — `undefined: FoundBankTx`.

- [ ] **Step 3: Split `AddParticipantTx` into the four acts**

Cut the existing body along the entity lines it already has:

- everything from `NextID(…, "bank")` through the product publish, plus the row
  write and the audit event, is `FoundBankTx`, with `Assets[asset].Settlement`
  left empty;
- `centralBankChartTx`, `centralBankAssetsAccountIn` and the `cbReserve`
  creation are `OpenSettlementAccountTx`, plus its member row;
- `AdmitMemberTx` and `RecordMembershipTx` are new.

`centralBankAssetsAccountIn` exists because `AddParticipantTx` needed one per
asset without re-listing the chart. Its doc says the staleness is safe "because
the match is on the asset and AddParticipantTx handles each asset exactly once;
a caller that could ask twice for the same asset would create a second account
and must list again." **`OpenSettlementAccountTx` is idempotent, so it is
precisely the caller that doc warns about.** Check that argument holds under
re-entry, or list again.

- [ ] **Step 4: Keep `AddParticipantTx` alive as a composition**

```go
// AddParticipantTx is the transitional composition Task 17d deletes.
//
// The three acts above are three institutions' units of work and this calls all
// four in one, which is the thing this task exists to stop. It stays for exactly
// one task's length because deleting it here would leave the branch un-buildable
// between two tasks, so neither could be verified or reviewed on its own — the
// mistake Task 16's plan made and its implementer corrected.
//
// What it is worth beyond compiling: every existing admission test now runs
// through the three acts, so this is a check that they compose to what the
// atomic version did.
func (s *Network) AddParticipantTx(ctx context.Context, tx Tx, name string, bic iso20022.BIC, assets []ledger.AssetCode) (*Bank, error)
```

- [ ] **Step 5: Re-route the four readers of the settlement account**

Per the spec's table:

- `SettleCycleTx` (`:953`, `:966`, `:991`, `:998`) and `SettleReturnTx` (`:2326`,
  `:2344`, `:2376`) read the central bank's `SettlementMember`.
- `DepositTx` (`:628`) reads the funded bank's own `Assets[asset].Settlement`.
- `ReserveBalance` (`:2959`) reads the central bank's `SettlementMember`.

`SettleCycleTx` has to turn a cycle's net positions — keyed by participant —
into BICs before it can ask, and it does that through the bank row. **Write that
down as a crossing this task does not close**, with the reason it does not: the
pacs.009's legs already carry BICs, so closing it means the settlement agent
settling from the message rather than the cycle row, which is the gap
`mesh/centralbank.go`'s own doc already assigns to Task 18.

`DepositTx`'s comment must say that the read is now legitimate — an account
holder quoting its own account number — and that **the crossing that remains is
the posting**, which no re-routing of a lookup changes.

- [ ] **Step 6: Run to verify the tests pass and nothing else moved**

```bash
go test ./... -count=1
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./... -count=1
```

Every pre-existing test must still pass **unchanged**. If one needs editing, that
is a behaviour change this task did not intend — stop and find out what moved.

- [ ] **Step 7: Watch each new test fail**

Four mutations, one per test. The one that matters most:
`TestOpeningASettlementAccountTwiceOpensOne` — remove the idempotency check and
confirm the account count assertion, not just the id comparison, is what fails.
Task 16's lesson: a fixture can pass for the wrong reason, and the assertion that
looks redundant is often the only one doing work.

- [ ] **Step 8: Commit**

```bash
git add -A && git commit -m "feat(payment): admission is three acts, composed for one task longer"
```

---

## Task 17d: the conversation

The messages, the handlers, the entry point. `AddParticipantTx` dies here.

**Files:**
- Modify: `payment/translate.go` (the message builders and readers)
- Modify: `mesh/ops.go`, `mesh/mesh.go`, `mesh/bank.go`, `mesh/csm.go`,
  `mesh/centralbank.go`, `mesh/doc.go`
- Modify: `payment/system.go` (delete `AddParticipantTx`)
- Test: `mesh/admission_test.go` (new), `payment/message_test.go`

**Interfaces produced:**

```go
// payment/translate.go
func (s *Network) AdmissionMessage(b *Bank, mc MessageContext) (iso20022.Envelope, error)
func ReadAdmissionRequest(doc *iso20022.Acmt007) (AdmissionRequest, error)
func (s *Network) AdmissionAcknowledgementMessage(m SettlementMember, mc MessageContext) (iso20022.Envelope, error)
func ReadAdmissionAcknowledgement(doc *iso20022.Acmt010) (AdmissionAcknowledgement, error)
func (s *Network) AdmissionRejectionMessage(bic iso20022.BIC, reason string, mc MessageContext) (iso20022.Envelope, error)

// mesh
func (m *Mesh) Admit(ctx context.Context, name string, bic iso20022.BIC, assets []ledger.AssetCode) (*payment.Bank, error)
```

### The flow, and which actor does what

```
1  Mesh.Admit          reserve the BIC at the mesh
                       FoundBankTx in the bank's own unit of work
                       on failure: drop the actor again
                       bank.apply() sends the acmt.007
2  csm.relayAdmission  relays to the central bank. Holds NOTHING.
3  cb.receiveAdmission OpenSettlementAccountTx; answers acmt.010 (or acmt.011)
4  csm.receiveAdmissionStatus
                       AdmitMemberTx from the acknowledgement; forwards it
5  bank.receiveAdmission
                       RecordMembershipTx; Status becomes Member
```

- [ ] **Step 1: Write the failing conversation test**

`mesh/admission_test.go`:

```go
// TestAdmissionIsAConversation is the task's headline, asserted as the state
// each institution ends up in rather than as the messages that got there —
// TestTheMessagesAnAdmissionPutsOnTheWire below is what asserts those.
func TestAdmissionIsAConversation(t *testing.T) {
	h := newHarness(t)
	joiner, err := h.mesh.Admit(context.Background(), "Nordhaven Bank", "NORDSESSXXX", euroOnly)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if joiner.Status != payment.BankFounded {
		t.Errorf("Admit returned status %q; the scheme's answer arrives as a message, not from this call", joiner.Status)
	}
	if err := h.mesh.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	// The bank is a member and knows its own account at the central bank.
	after := h.getBank(t, joiner.ID)
	if after.Status != payment.BankMember {
		t.Errorf("after the conversation the bank's status is %q, want %q", after.Status, payment.BankMember)
	}
	settlement := after.Assets["EUR"].Settlement
	if settlement == "" {
		t.Fatal("the bank was admitted and does not know its settlement account")
	}
	// The central bank's own record agrees with it, and is the row settlement
	// will actually read.
	member := h.getSettlementMember(t, "NORDSESSXXX")
	if member.Accounts["EUR"] != settlement {
		t.Errorf("the bank thinks its settlement account is %q; the central bank opened %q",
			settlement, member.Accounts["EUR"])
	}
	// And the clearing house can route to it.
	if _, err := h.getRosterEntry("NORDSESSXXX"); err != nil {
		t.Errorf("the bank was admitted and the clearing house cannot route to it: %v", err)
	}
}

// TestARefusedAdmissionLeavesAFoundedBank is the orphan defect, inverted. What
// used to be left behind was a roster row for a bank that could neither pay nor
// be paid, with no way back. What is left behind now is a bank that exists and
// has not joined — which is a true state, and which re-calling Admit re-drives.
func TestARefusedAdmissionLeavesAFoundedBank(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// An asset nobody issues. The central bank refuses it, which is the one
	// refusal on this path that reaches a message rather than the caller.
	joiner, err := h.mesh.Admit(ctx, "Nordhaven Bank", "NORDSESSXXX", []ledger.AssetCode{"XYZ"})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := h.mesh.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	after := h.getBank(t, joiner.ID)
	if after.Status != payment.BankFounded {
		t.Errorf("a refused admission left the bank %q, want %q", after.Status, payment.BankFounded)
	}
	// It exists, with a working book, and it is not in the roster. Those two
	// together are the whole of what replaced the orphan row.
	if _, err := h.getRosterEntry("NORDSESSXXX"); !errors.Is(err, payment.ErrRosterEntryNotFound) {
		t.Errorf("a refused admission put the bank in the roster: %v", err)
	}
	if _, err := after.OpenCustomerAccount(ctx, "Ada", "EUR"); err != nil {
		t.Errorf("a founded bank cannot open a customer account: %v", err)
	}
	// And re-driving it works, which is what makes the state recoverable rather
	// than merely honest. This time with an asset that exists.
	if _, err := h.mesh.Admit(ctx, "Nordhaven Bank", "NORDSESSXXX", euroOnly); err != nil {
		t.Fatalf("re-driving a refused admission: %v", err)
	}
	if err := h.mesh.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := h.getBank(t, joiner.ID); got.Status != payment.BankMember {
		t.Errorf("after re-driving, the bank is %q, want %q", got.Status, payment.BankMember)
	}
	// One bank, not two. The retry must not have founded a second one.
	assertBankCount(t, h, "NORDSESSXXX", 1)
}

// TestAdmissionRefusesAnAddressAnotherBankAnswersTo pins the resolution of the
// two authorities. The roster is the domain's truth; the actor map is the
// transport's. A BIC belonging to a bank this mesh founded but never admitted is
// a RETRY and is not refused — get that backwards and a founded bank can never
// join.
func TestAdmissionRefusesAnAddressAnotherBankAnswersTo(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// h.debtorBIC belongs to a bank the harness already admitted.
	if _, err := h.mesh.Admit(ctx, "Impostor Bank", h.debtorBIC, euroOnly); err == nil {
		t.Fatal("admitting a second institution on a member's BIC succeeded")
	} else if !errors.Is(err, mesh.ErrAddressTaken) {
		t.Fatalf("admitting on a member's BIC: %v, want ErrAddressTaken", err)
	}
	// The member is untouched: same name, still a member, still routable.
	if e, err := h.getRosterEntry(h.debtorBIC); err != nil {
		t.Fatalf("the incumbent is no longer in the roster: %v", err)
	} else if e.Name == "Impostor Bank" {
		t.Error("the refused admission overwrote the incumbent's roster entry")
	}
}

// TestNothingIsWrittenWhenTheAddressIsRefused is the ordering, made falsifiable.
// The address is reserved before the bank's unit of work runs, so a clash writes
// no row at all — where today AddParticipant writes the row and THEN asks.
func TestNothingIsWrittenWhenTheAddressIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	before := h.bankCount(t)
	if _, err := h.mesh.Admit(ctx, "Impostor Bank", h.debtorBIC, euroOnly); err == nil {
		t.Fatal("Admit on a taken address succeeded")
	}
	if after := h.bankCount(t); after != before {
		t.Errorf("a refused address wrote %d bank row(s); the address is reserved BEFORE the unit of work runs",
			after-before)
	}
	// The old order — write the row, then ask for the address — is what left a
	// bank in the roster that could neither pay nor be paid. Reverse Admit's two
	// steps and this is the test that fails.
}

// TestTheMessagesAnAdmissionPutsOnTheWire is the counterpart of
// TestTheMessagesACutOffPutsOnTheWire and TestTheMessagesAReturnPutsOnTheWire:
// four messages, in order, between the institutions that send them.
func TestTheMessagesAnAdmissionPutsOnTheWire(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.recordMessages() // whatever the existing wire recorder is called
	if _, err := h.mesh.Admit(ctx, "Nordhaven Bank", "NORDSESSXXX", euroOnly); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := h.mesh.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	want := []wireMessage{
		{From: "NORDSESSXXX", To: h.cfg.ClearingHouseBIC, Def: "acmt.007.001.03"},
		{From: h.cfg.ClearingHouseBIC, To: h.cfg.CentralBankBIC, Def: "acmt.007.001.03"},
		{From: h.cfg.CentralBankBIC, To: h.cfg.ClearingHouseBIC, Def: "acmt.010.001.03"},
		{From: h.cfg.ClearingHouseBIC, To: "NORDSESSXXX", Def: "acmt.010.001.03"},
	}
	assertWire(t, h.wire(), want)
}
```

`recordMessages`, `wireMessage`, `wire()` and `assertWire` are this plan's names
for whatever `TestTheMessagesACutOffPutsOnTheWire` and
`TestTheMessagesAReturnPutsOnTheWire` already use. **Read those two tests and
reuse their machinery**; do not add a second wire recorder.

Read `mesh/harness_test.go` first. It builds two banks through
`h.net.AddParticipant` at `:266`; that call is going away, and the harness is
what every mesh test is built on. Change it to `h.mesh.Admit` **plus a drain**,
and expect fallout: any test asserting on a bank before the conversation has
finished will now see `BankFounded`.

The harness also needs four small readers this plan's tests use: `h.getBank`,
`h.getSettlementMember`, `h.getRosterEntry`, `h.bankCount` — plus
`assertBankCount`. Add them beside the harness's existing accessors rather than
in the test file.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./mesh/ -run Admission -v`
Expected: FAIL — `h.mesh.Admit undefined`.

- [ ] **Step 3: The message builders and readers**

In `payment/translate.go`, beside `ReadReturn` and `ReadSettlement`. Two guards
are load-bearing and both have precedent on this branch:

- `ReadAdmissionRequest` refuses an empty owner BIC. `ReadReturn`'s equivalent —
  a pacs.004 with an empty `OrgnlTxId` — would have settled real reserves under
  the idempotency key `":return-settle"` for a payment nobody could name. Here an
  empty BIC would open a settlement account for a bank nobody can address, and
  key the central bank's member row under `""`.
- `ReadAdmissionAcknowledgement` refuses an account with an empty currency, for
  the same reason: it decides which asset's account this is.

Guard in the reader **and** in the domain act, so the reader's guard is defence
in depth rather than the only line. That is exactly what Task 16e did with
`ReadReturn` and `SettleReturnTx`, after an implementer found the hole outside
its brief.

- [ ] **Step 4: The three handlers**

`csm.relayAdmission` uses the existing `relay` helper
(`mesh/csm.go:375`), which already turns `ErrUnknownBIC` into an RC01 answer.
**It holds nothing** — write the contrast with `csm.held` into the function's
doc, because a reader who knows the return will expect a hold here and the
absence needs a reason: the acknowledgement names the bank, its assets and its
accounts, so there is nothing to remember.

`centralBank.receiveAdmission` follows `receiveReturn`'s shape: read the message,
run the domain act in one unit of work, answer. It answers **acmt.011 and not
pacs.002** — a status report is about a payment transaction and this is not one.

`csm.receiveAdmissionStatus` runs `AdmitMemberTx` and then forwards the
acknowledgement to the owner BIC the message names. **Write the routing entry
before forwarding**, so a bank that is told it is a member is one the clearing
house can already route to; the reverse order is the settlement path's
statement-before-answer rule in a new place, and it is load-bearing for the same
reason.

`bank.receiveAdmission` runs `RecordMembershipTx` with its own `b.pid`.

- [ ] **Step 5: `Mesh.Admit`, and `AddBank` becomes address reservation**

```go
// Admit brings a bank into being and applies to the scheme for it.
//
// Its synchronous half is Mesh.Submit's: the bank's own work, on the caller's
// goroutine, marked as the bank's so the recorder attributes it correctly, and
// committed before anything is sent. What it does NOT answer is whether the
// scheme accepted — that arrives later, at two other actors, as a message. The
// bank it returns is Founded.
//
// # The address is reserved first, and that is the orphan defect's fix
//
// A BIC is the only thing about an admission that can clash, and it used to be
// checked LAST: api.handleAddParticipant wrote the participant row and then
// asked the mesh for the address, so a refusal left a bank in the roster that
// could neither pay nor be paid, with no way back. Reversed here. The actor is
// registered before the bank's unit of work runs and dropped again if that unit
// of work fails, because an in-memory rollback is reliable and a rollback of a
// committed transaction is not.
//
// # A taken address is two situations
//
// If it belongs to a bank this mesh founded that the roster has no entry for,
// the operator is re-driving an interrupted admission: nothing is founded twice
// and the acmt.007 goes out again. If it belongs to anybody else it is refused.
// Both directions of getting this wrong have a name — refuse the first and a
// founded bank can never join; accept the second and admission overwrites an
// institution.
func (m *Mesh) Admit(ctx context.Context, name string, bic iso20022.BIC, assets []ledger.AssetCode) (*payment.Bank, error)
```

`Mesh.AddBank` keeps its job for `joinRoster` and loses its role as the thing
admission calls afterwards. Its doc is four paragraphs about the orphan and must
be rewritten, not amended: the defect it describes is gone, and a doc describing
a fixed defect in the present tense is worse than no doc.

`joinRoster` reads **roster entries** rather than banks — the roster is what says
who is a member. A founded, unadmitted bank gets no actor at startup, which is
correct and is a behaviour change worth its own test.

- [ ] **Step 6: Delete `AddParticipantTx` and sweep its claims**

Delete it and `Network.AddParticipant`. Then **read whole** — do not grep — every
comment block whose subject is admission, and report the list of files you read
in full:

`payment/system.go`, `payment/bank.go`, `payment/roster.go`, `payment/doc.go`,
`payment/store.go`, `mesh/doc.go`, `mesh/mesh.go`, `mesh/ops.go`,
`mesh/bank.go`, `mesh/csm.go`, `mesh/centralbank.go`,
`api/handlers_participant.go`, `store/pg/schema/0001_init.sql`, `seed/seed.go`.

Known false-after-this-task claims, as a starting point and **not** as the list:

- `mesh/mesh.go:30` and `:603` and `:614` — three passages about the orphaned
  row and about admission being "the other door".
- `mesh/harness_test.go:246` — "AddParticipant call provisions a suspense, a
  reserve and a central-bank …".
- `store/pg/schema/0001_init.sql:537` — "the Basic product AddParticipant creates
  for every member".
- `seed/seed.go:199` and `:532`.
- `payment/system.go:1125` and `:1595`.

- [ ] **Step 7: `mesh/doc.go` gains admission**

A section beside `# The credit transfer flow`, `# Settlement` and `# The return`.
It must say the thing the other three do not: this is the only flow that **brings
an actor into existence**, and it is the only one whose first message is sent by
a party that was not addressable a moment earlier.

- [ ] **Step 8: Run everything, both stores, plus race**

```bash
go test ./... -count=1
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./... -count=1
go test ./mesh/ ./api/ ./cmd/... -race -count=1
```

The race run matters here more than usual: `Admit` writes `m.actors` from the
caller's goroutine while actors are running, which is the interleaving
`joinRoster`'s doc already describes at length.

- [ ] **Step 9: Watch the conversation's ordering fail**

Reverse `csm.receiveAdmissionStatus` so it forwards before it writes the routing
entry, and confirm a test fails. Task 15 and 16 both recorded that the equivalent
swap leaves a race the sender usually wins, so **only a swap plus a delay inverts
it deterministically** — if a plain swap stays green, add the delay before
concluding the assertion is wrong.

- [ ] **Step 10: Commit**

```bash
git add -A && git commit -m "feat(mesh): admission is a conversation, and AddParticipantTx is gone"
```

---

## Task 17e: the rewiring, and the sixth crossing measured

**Files:**
- Modify: `api/handlers_participant.go`, `api/dto_payment.go`, `api/server.go`
- Modify: `seed/seed.go`
- Modify: `mesh/books_test.go`
- Modify: `web/src/lib/types.ts`, `web/src/components/create-participant-dialog.tsx`
- Test: `api/*_test.go`, `mesh/books_test.go`

- [ ] **Step 1: Write the failing recorder tests**

```go
// TestWhichBooksAdmissionReaches is the counterpart the sub-project's Tasks
// table asks for. One call used to reach the bank's book, the central bank's and
// the network's; now three institutions reach three, one each.
func TestWhichBooksAdmissionReaches(t *testing.T) {
	// … admit, drain, then per-actor:
	assertBooksTouched(t, "the joining bank", h.booksTouchedBy("NORDSESSXXX"),
		[]ledger.BookID{joinerBook, ledger.NetworkBook})
	assertBooksTouched(t, "the central bank", h.booksTouchedBy(h.cfg.CentralBankBIC),
		[]ledger.BookID{payment.CentralBankBook})
	assertBooksTouched(t, "the clearing house", h.booksTouchedBy(h.cfg.ClearingHouseBIC),
		[]ledger.BookID{ledger.NetworkBook})
}
```

The joining bank's set includes `NetworkBook` and that is not a defect: the
recorder's own note (`mesh/books_test.go:641`) explains that every id allocation
and every audit event is network-scoped, so no version of creating a row avoids
it. **Correct this plan's want list against what the recorder measures — do not
correct the domain to match this plan.** Task 16's plan predicted
`[CentralBankBook, Network]` for the return and was wrong; the measurement won.

```go
// TestFundingAReserveReachesTwoBooks pins crossing 6 as a fact rather than a
// note. DepositTx posts the bank's reserve mirror and the central bank's leg in
// ONE unit of work, and it is the only way reserves are funded in this system.
// Neither instrument this sub-project has could ever have found it — the
// recorder watches actors, and funding never becomes a message.
//
// It is expected to PASS today and to FAIL the day the stores split. That
// failure is the signal, exactly as TestWhichBooksTheCentralBankReachesWhenItSettles
// was for Task 15.
func TestFundingAReserveReachesTwoBooks(t *testing.T) {
	clock := func() time.Time { return testTime }
	rec := newRecordingStore(testenv.New(t, clock).Payment())
	sys := payment.New(rec, clock) // match whatever the constructor here is called
	ctx := context.Background()

	bank := admitOneBank(t, sys, ctx, "Aurora Bank", "AURODEFFXXX")
	acct, err := bank.OpenCustomerAccount(ctx, "Ada", "EUR")
	if err != nil {
		t.Fatalf("OpenCustomerAccount: %v", err)
	}
	rec.reset() // forget everything admission touched; only the funding is under test
	if err := sys.Deposit(ctx, bank.ID, acct.ID, 100_000, "cash in"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	// Two books, one unit of work. This is expected to PASS today: it is a
	// measurement of the crossing, not a demand that it be closed.
	assertBooksTouched(t, "funding a reserve", rec.touched(),
		[]ledger.BookID{bank.BookID, payment.CentralBankBook, ledger.NetworkBook})
}
```

This one cannot use `booksTouchedBy`, because funding has no actor. Drive
`recordingStore` directly, the way `TestWritingAParticipantTouchesNoBankBook`
does at `mesh/books_test.go:1512`. `rec.reset()` and `rec.touched()` are this
plan's names for whatever the recorder actually exposes — read it and use its
own; if it has no reset, take a snapshot before the deposit and diff.

`ledger.NetworkBook` is in the want list because the audit event for the deposit
is network-scoped, per the recorder's own note at `mesh/books_test.go:610`.
**Correct the want list against what the recorder measures, not the other way
round.**

- [ ] **Step 2: Add the `structCarriedBooks` entries**

`PutSettlementMember` and `PutRosterEntry` carry no book at all, so they may not
be candidates — check what the parser actually finds before writing entries.
`PutBank` inherits `PutParticipant`'s entry, whose `Why` cites
`TestWritingAParticipantTouchesNoBankBook` by name; rename both together, and
keep the evidence claim pointing at a test that exists.

- [ ] **Step 3: `seed` admits through the mesh**

`Populate(ctx, net)` becomes `Populate(ctx, net, m *mesh.Mesh)`, and the four
`AddParticipant` calls at `seed/seed.go:536-539` become `m.Admit` followed by
**one** `m.Drain` before any funding. Funding needs a settlement account, and a
settlement account is what the conversation produces.

This removes a wart rather than adding one: the handoff records that "`seed`
reaches `payment.SubmitPaymentTx` directly and so bypasses the on-us refusal at
`Mesh.Submit`", so the seeded dataset has never exercised the mesh's own doors.
Admission is now one it does.

Callers: `api/server.go`'s `populate` field and `cmd/server`. Check both.

- [ ] **Step 4: `api.Server.Reset` stops calling `JoinRoster`**

`Reset` forgets the banks, truncates, repopulates and then calls `JoinRoster`.
With `populate` admitting through the mesh, the banks already have actors, and
`JoinRoster` "expects to find no member banks already registered" and refuses
all-or-none. Skip it on the path where populate ran, and **run
`TestResetDrainsBeforeTruncating` and its neighbours** — `api/server.go:150` says
that test fails on the swap, so it is watching this ordering already.

- [ ] **Step 5: The handler and the DTO**

`handleAddParticipant` calls `Mesh.Admit` and answers **202 Accepted** with the
founded bank, not 201 Created — the scheme has not answered yet, and `Mesh.Submit`
already established that shape and the reason for it.

Its four-paragraph doc is about the orphan and the two steps that "are not one,
and cannot be". That is no longer the design. Rewrite it: what an operator now
gets is a founded bank, an answer that arrives as a message, and a state they can
re-drive.

`participantDTO` gains `status`. `web/src/lib/types.ts` and the create dialog
follow; the dialog must not claim the bank can pay before it is a member.

- [ ] **Step 6: Fix `api/mesh_test.go`'s hardcoded ids properly**

They will shift again — `FoundBankTx` allocates in a different order from
`AddParticipantTx`. Read the ids from the seed rather than re-hardcoding them.
The handoff has carried this as a defect for two tasks; do not carry it a third.

- [ ] **Step 7: Screenshot the web change**

No test here can catch a rendering regression. Start the app, load the
participant list and the create dialog, and **look at the screenshot**.

- [ ] **Step 8: Full verification and commit**

Everything, including `cd web && rm -rf .next && npm run typecheck && npm run
lint && npm run test`.

```bash
git add -A && git commit -m "feat(api,seed): admission through the mesh, and crossing 6 measured"
```

---

## Task 17f: the documentation sweep

Six layers, and the same task that exists to stop a false claim is the one Task
16 found a false claim inside. Its sweep **added** a chapter 9 question to teach
the return's finality and scored the inverse of the branch's headline fact as
correct. Assume this task will do the same unless it checks.

**Files:**
- Modify: `README.md`
- Modify: `web/src/components/hint-content.ts`
- Modify: `web/src/lib/quiz/chapters/*.ts` — chapters 9, 11, 15, 16
- Verify: `store/pg/schema/0001_init.sql`, `iso20022/doc.go`, `payment/doc.go`,
  `mesh/doc.go` (all edited in earlier tasks; this task checks them against the
  code rather than re-editing blind)

**The domain content this task adds:**

1. A bank exists before it joins a scheme. Founding and membership are different
   things, and a founded bank can take deposits and cannot pay. **This is the
   reversal of `AddParticipantTx`'s "so a bank can never exist without the
   accounts it needs"**, and it is the reversal that carries domain content: the
   guarantee that atomic write bought was never one a real admission has. The
   spec lists it as a reversed ruling, and a reversed ruling that reads like an
   oversight is worse than the original.
2. A bank's settlement account is opened **by the central bank, in the central
   bank's book**. The bank knows its number the way an account holder knows their
   IBAN; it does not hold the account.
3. The scheme's routing directory is the clearing house's, and it says who may be
   addressed — not who exists.
4. Admission is a real sequence: settlement account first, routing entry second,
   because a scheme will not route to a member that cannot settle.
5. What is **not** modelled: scheme membership is contractual in life and travels
   on no message; a real central bank creates an RTGS account through reference
   data (CRDM, `reda`) and not through eBAM.

- [ ] **Step 1: Read the four `doc.go` files and the schema in full, and check them against the code**

Not grep. Read them whole and report which you read. Every one was edited by an
earlier task in this plan, and every one of those edits was written from a
prediction.

- [ ] **Step 2: `README.md` first, then the layers that copy from it**

`README.md` is authoritative. Write it there, then bring `hint-content.ts` into
line with it, then the quiz.

- [ ] **Step 3: The quiz, and the trap**

Chapters 9 (clearing and settlement), 11 (payment schemes), 15 and 16
(persistence). **Chapter 9 is at 22 questions, the maximum `diversity.test.ts`
allows** — a new question there means replacing one.

For every question this task adds or edits, **check the scored-correct answer
against the code, not against this plan**. Task 16f's regression was exactly
this: six other layers had the ordering right and the new question stated its
inverse.

- [ ] **Step 4: Run the web tests, and load a page**

```bash
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

`npm run test` catches a `[[wiki-link]]` to a missing `hint-content.ts` key in
hint bodies *and* in quiz explanations. It is the only thing that does — the
runtime guard does not scan explanations, and `next build` stays green while
every route throws. **Load a page.**

- [ ] **Step 5: Full verification and commit**

```bash
git add -A && git commit -m "docs: a bank exists before it joins, and joining is a conversation"
```

---

## After 17f, before the merge

Neither of these is optional, and the handoff is emphatic about both.

1. **The whole-branch review, with probes required.** It has found a Critical
   money defect on two consecutive sub-projects, both invisible to a clean
   per-task history, both found by a reviewer that **built a probe and ran it**
   rather than reading a diff. Budget for it.
2. **Run the verification yourself before merging.** A subagent reporting it
   green is a claim, not evidence. Two of the runs are checkable without trusting
   anyone: the Postgres run takes 20–39s on the store-touching packages against
   1–5s on `store/mem`, and `make test-schemas` must report **no skips**. Both
   failure modes look like a pass from a distance.

Three things this task's handoff must say that no earlier one could:

- **The schemas are real now, and they are local.** `iso20022/testdata/xsd/` is
  gitignored, so the check runs on this machine and on no fresh clone and in no
  CI job. Whoever picks up Task 17.1 or Task 18 has to copy them into their
  worktree or `make test-schemas` will skip everything and still say PASS. Say
  where they came from and what the first run found.

- **The `TEST_DATABASE_URL` verification line and the "there is one migration"
  rule both expire at Task 17.1**, the SQLite swap. Task 16's handoff was
  explicit that Task 17's own handoff must say so rather than copy the rule list
  forward. Say it.
- **Crossings 2 and 6 are what remain**, and both are invisible to every
  instrument this sub-project has, because neither operation becomes a message.
  Task 18's reconciliation harness is the successor instrument.

## Follow-up work this task does not do

- **Vault cash and the lodgement.** Cash paid in does not move central-bank
  reserves; the honest fix for crossing 6 is vault cash in the bank's own book
  plus a separate lodgement, which is a real conversation (`camt.050` in
  TARGET2). Deliberately not folded into the task that adds a message family and
  dissolves the central domain type.
- **The settlement agent settling from the message.** `SettleCycleTx` still turns
  net positions into BICs through the roster. The pacs.009 already carries them.
- **The on-us book transfer** and **the zero-net strand**, both carried from Task
  16 and both money that stops.
