# Package restructure: `cbs` module + dedicated `ledger` package

**Date:** 2026-06-24
**Status:** Approved (pending spec review)

## Goal

Improve the Go package structure of the core-banking-system repo:

1. Rename the Go module `github.com/raphi011/ledger` → `github.com/raphi011/cbs`.
   (The GitHub repo will be renamed to `cbs` to match.)
2. Move the root `ledger` package (currently at the repo root) into a dedicated
   `ledger/` subdirectory, making every domain layer a sibling.
3. Split the oversized `api/dto.go` (531 lines) into per-resource files, mirroring
   the existing `api/handlers_*.go` organization.

This is a pure restructuring. **No behavior changes.** The package name `ledger`
and all exported identifiers stay the same; only the directory and import paths
move.

## Why

The current layout has a naming smell: the **module** is named `ledger` and the
**root package** is also `ledger`, yet `ledger` is just one of five domain layers
(`ledger`, `deposit`, `payment`, `seed`, `api`) in a system whose repo is
`core-banking-system`. "ledger" is overloaded to mean both "the whole thing" and
"the general-ledger core."

Renaming the module to `cbs` and moving the GL core into `ledger/` resolves this:
the module name describes the system, and `ledger/` becomes a peer of the other
layers. The dependency graph is already a clean, acyclic, downward-only DAG, so
the move is almost entirely a mechanical import-path swap.

## Current state

```
github.com/raphi011/ledger   (module)
├── *.go              package ledger  (GL core: service, types, list, errors, doc)
├── deposit/          package deposit  → ledger
├── payment/          package payment  → ledger, deposit
├── seed/             package seed     → ledger, deposit, payment
├── api/              package api      → ledger, deposit, payment
├── cmd/server/       package main     → api, seed
├── book/             educational markdown ("the book")
├── README.md         project-wide readme (referenced by every package's doc.go)
├── web/              Next.js frontend (separate, not part of the Go module)
└── docs/             superpowers specs/plans
```

Dependency graph (no cycles, strictly downward):

```
cmd/server → api  ┐
            → seed ┼→ payment → deposit → ledger
                   └───────────┴──────────┘
```

## Target structure

```
github.com/raphi011/cbs   (module)
├── ledger/           package ledger  (MOVED from root: the 7 root *.go files)
├── deposit/          unchanged code, import path updated
├── payment/          unchanged code, import path updated
├── seed/             unchanged code, import path updated
├── api/              import paths updated + dto.go split (see below)
├── cmd/server/       import paths updated
├── book/             unchanged (stays at root)
├── README.md         unchanged location (stays at root)
├── web/              unchanged
└── docs/             unchanged
```

### Files moved into `ledger/`

The 7 root `*.go` files move verbatim (package declaration stays `package ledger`):

- `doc.go` → `ledger/doc.go`
- `errors.go` → `ledger/errors.go`
- `service.go` → `ledger/service.go`
- `service_test.go` → `ledger/service_test.go`
- `types.go` → `ledger/types.go`
- `list.go` → `ledger/list.go`
- `list_test.go` → `ledger/list_test.go`

Use `git mv` to preserve history.

`ledger/doc.go` keeps its `// See README.md …` line. This is now *consistent* with
`deposit/doc.go` and `payment/doc.go`, which already say the same from a
subdirectory and refer to the root `README.md`.

## Change 1 + 2: module rename & ledger move

### Import-path rewrite rules

There are exactly two source forms to rewrite. **Order matters** — do the exact
bare-root form first so it isn't caught by the prefix rule:

1. Exact root import (the GL core, imported by deposit/payment/seed/api):
   `"github.com/raphi011/ledger"` → `"github.com/raphi011/cbs/ledger"`
2. Sub-package prefix (everything else, incl. go.mod module line):
   `github.com/raphi011/ledger/` → `github.com/raphi011/cbs/`

After step 1, the rewritten core path is `…/cbs/ledger` (contains `raphi011/cbs/`,
not `raphi011/ledger/`), so step 2 cannot double-rewrite it.

`go.mod`'s `module github.com/raphi011/ledger` is covered by treating the module
line explicitly (it has no trailing slash) — handle it as its own edit.

### Scope of the rewrite

- 18 Go files reference the old module path (imports only — verified there are no
  string-literal/`go:embed`/reflection references to the path).
- `go.mod` module directive.
- The package name `ledger` and all symbols are unchanged, so no call sites change.

### Out of scope for the rewrite

- `docs/superpowers/plans/2026-06-23-*.md` and
  `docs/superpowers/specs/2026-06-23-*.md` reference the old path. These are
  **historical records** of past work — left unchanged (updating them would
  misrepresent what was true at the time).
- The root `README.md` does **not** reference the import path (verified), so no
  code-path edits there. A light prose scan during execution will catch any
  structure description that drifts, but no path edits are expected.

## Change 3: split `api/dto.go`

`api/dto.go` is the only file in `api/` not split per-resource (handlers are
already `handlers_ledger.go`, `handlers_deposit.go`, `handlers_payment.go`,
`handlers_participant.go`, `handlers_admin.go`). Split its 55 declarations to
mirror the three domain layers:

- **`dto_ledger.go`** — `ledgerDTO`, `subledgerDTO`, `accountDTO`, `entryDTO`,
  `transactionDTO`, `toLedgerAuditDTO`, `createAccountRequest`,
  `postTransactionRequest` (+ `toDomain`), `accountTypeFromString`,
  `directionFromString`.
- **`dto_deposit.go`** — `depositAccountDTO`, `holdDTO`, `balanceDTO`,
  `snapshotDTO`, `toDepositAuditDTO`, `openDepositAccountRequest`,
  `statusRequest`, `createHoldRequest`, `captureHoldRequest`, `snapshotRequest`,
  `fundRequest`.
- **`dto_payment.go`** — `participantDTO`, `reserveDTO`, `partyRefDTO`,
  `paymentDTO`, `mandateDTO`, `clearingCycleDTO`, `settlementDTO`,
  `positionsToMap`, `schemeDTO`, `createMandateRequest`,
  `initiatePaymentRequest` (+ `toDomain`), `openCycleRequest`.
  (Participant/reserve DTOs live here — participants are payment-network entities.)
- **`dto.go`** (shared, remainder) — the cross-cutting `auditEventDTO` type
  (rendered from both ledger and deposit audit events) and the generic request
  envelopes `nameRequest`, `descriptionRequest`, `reasonRequest`.

This is a mechanical move of declarations between files in the same package — no
identifier or behavior changes. Exact per-file boundaries may be refined during
implementation as long as the per-resource grouping principle holds.

## Verification

After each logical step (move, rewrite, dto split), the build and full test suite
must stay green:

```
go build ./...
go vet ./...
go test ./...
gofmt -l .        # expect no output
```

The existing test suite (`service_test.go`, `list_test.go`, `deposit/*_test.go`,
`payment/*_test.go`, `seed/*_test.go`, `api/server_test.go`) is the safety net;
because no behavior changes, all tests must pass unchanged after the restructure.

## Explicitly out of scope (considered, rejected as YAGNI)

- **Shared `clock` package** — `func() time.Time` injection is duplicated across
  Book/Register/Network/seed, but each is a one-liner; a package adds more
  ceremony than it removes.
- **Shared `audit` package** — `ledger` and `deposit` each own an
  `AuditEventType` + `appendAudit`. Extracting a shared package would couple
  layers that are deliberately independent. If desired, it is a separate, larger
  effort with its own design.
- **Updating historical superpowers docs** to the new module path (see above).
```
