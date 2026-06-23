"use client";

import { useState } from "react";
import { useParams, useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { CopyId } from "@/components/copy-id";
import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { FieldLabel } from "@/components/field-label";
import { Hint } from "@/components/hint";
import { Money } from "@/components/money";
import { ConfirmAction } from "@/components/forms/confirm-action";
import { CreateHoldForm } from "@/components/forms/create-hold-form";
import { CaptureHoldForm } from "@/components/forms/capture-hold-form";
import { FundParticipantForm } from "@/components/forms/fund-participant-form";
import {
  useCloseDepositAccount,
  useDepositAccount,
  useDepositBalance,
  useHolds,
  useReleaseHold,
  useSetDepositStatus,
  useSnapshots,
  useTakeSnapshot,
} from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import { formatDate } from "@/lib/dates";
import type { DepositStatus, DepositStatusAction } from "@/lib/enums";
import type { Hold, Snapshot } from "@/lib/types";

// Which lifecycle transitions are offered from each status. The backend is the
// source of truth (it rejects invalid moves), but only showing the legal ones
// keeps the UI honest and teaches the state machine.
const STATUS_TRANSITIONS: Record<
  DepositStatus,
  { action: DepositStatusAction; label: string; description: string }[]
> = {
  Active: [
    {
      action: "freeze",
      label: "Freeze",
      description:
        "Makes the account view-only (a legal or fraud action). No debits or credits until unfrozen.",
    },
    {
      action: "markDormant",
      label: "Mark dormant",
      description:
        "Marks the account inactive. It accepts credits only; an incoming payment reactivates it.",
    },
  ],
  Frozen: [
    {
      action: "unfreeze",
      label: "Unfreeze",
      description: "Returns the account to Active, fully usable again.",
    },
  ],
  Dormant: [
    {
      action: "reactivate",
      label: "Reactivate",
      description: "Returns the dormant account to Active.",
    },
  ],
  Closed: [],
};

// --- Three-part balance ---------------------------------------------------

function BalanceCard({ pid, did }: { pid: string; did: string }) {
  const { data, isLoading } = useDepositBalance(pid, did);
  const stats: { label: string; hint: "balance-book" | "balance-holds" | "balance-available"; cents?: number }[] = [
    { label: "Book", hint: "balance-book", cents: data?.book },
    { label: "Holds", hint: "balance-holds", cents: data?.holds },
    { label: "Available", hint: "balance-available", cents: data?.available },
  ];
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Balance</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-3 divide-x">
          {stats.map((s) => (
            <div key={s.label} className="px-4 first:pl-0">
              <div className="flex items-center gap-1 text-xs text-muted-foreground">
                {s.label}
                <Hint id={s.hint} />
              </div>
              {isLoading ? (
                <Skeleton className="mt-1 h-6 w-20" />
              ) : (
                <div className="mt-0.5 text-lg font-semibold tabular-nums">
                  <Money cents={s.cents ?? 0} />
                </div>
              )}
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

// --- Holds ----------------------------------------------------------------

function HoldActions({ pid, hold }: { pid: string; hold: Hold }) {
  const release = useReleaseHold(pid);
  if (hold.status !== "Active") return <span className="text-muted-foreground">—</span>;
  return (
    <div className="flex items-center justify-end gap-2">
      <CaptureHoldForm pid={pid} hid={hold.id} heldAmount={hold.amount} />
      <ConfirmAction
        trigger={
          <Button variant="ghost" size="sm">
            Release
          </Button>
        }
        title="Release hold"
        description="Cancels the authorization. The available balance is restored and nothing is posted to the ledger."
        confirmLabel="Release"
        pending={release.isPending}
        onConfirm={async () => {
          await release.mutateAsync(hold.id, {
            onSuccess: () => toast.success("Hold released"),
            onError: (err) => toast.error(describeError(err)),
          });
        }}
      />
    </div>
  );
}

function HoldsCard({ pid, did }: { pid: string; did: string }) {
  const { data, isLoading, error, refetch } = useHolds(pid, did);
  const columns: Column<Hold>[] = [
    { key: "amount", header: "Amount", render: (h) => <Money cents={h.amount} /> },
    { key: "status", header: "Status", render: (h) => <EnumBadge value={h.status} /> },
    {
      key: "expiresAt",
      header: "Expires",
      render: (h) => formatDate(h.expiresAt),
    },
    {
      key: "description",
      header: "Description",
      render: (h) => h.description || "—",
    },
    {
      key: "actions",
      header: "",
      align: "right",
      render: (h) => <HoldActions pid={pid} hold={h} />,
    },
  ];
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-1.5 text-base">
          Holds
          <Hint id="holds" />
        </CardTitle>
        <CreateHoldForm pid={pid} did={did} />
      </CardHeader>
      <CardContent>
        {error ? (
          <ErrorState error={error} onRetry={() => refetch()} />
        ) : (
          <DataTable
            columns={columns}
            rows={data}
            rowKey={(h) => h.id}
            isLoading={isLoading}
            empty="No holds. Place one to see the available balance drop without moving the book balance."
          />
        )}
      </CardContent>
    </Card>
  );
}

// --- Snapshots ------------------------------------------------------------

function SnapshotsCard({ pid, did }: { pid: string; did: string }) {
  const { data, isLoading, error, refetch } = useSnapshots(pid, did);
  const take = useTakeSnapshot(pid, did);
  const [date, setDate] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!date) return;
    try {
      await take.mutateAsync({ date });
      toast.success(`Snapshot taken for ${date}`);
      setDate("");
    } catch (err) {
      toast.error(describeError(err));
    }
  }

  const columns: Column<Snapshot>[] = [
    { key: "date", header: "Date", render: (s) => formatDate(s.date) },
    { key: "book", header: "Book", render: (s) => <Money cents={s.balance.book} /> },
    { key: "holds", header: "Holds", render: (s) => <Money cents={s.balance.holds} /> },
    {
      key: "available",
      header: "Available",
      render: (s) => <Money cents={s.balance.available} />,
    },
  ];

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-3">
        <CardTitle className="flex items-center gap-1.5 text-base">
          End-of-day snapshots
          <Hint id="snapshot" />
        </CardTitle>
        <form onSubmit={submit} className="flex items-end gap-2">
          <div className="space-y-1">
            <FieldLabel htmlFor="snap-date">Date</FieldLabel>
            <Input
              id="snap-date"
              type="date"
              value={date}
              onChange={(e) => setDate(e.target.value)}
              className="h-9"
            />
          </div>
          <Button type="submit" size="sm" disabled={take.isPending || !date}>
            {take.isPending ? "Taking…" : "Take snapshot"}
          </Button>
        </form>
      </CardHeader>
      <CardContent>
        {error ? (
          <ErrorState error={error} onRetry={() => refetch()} />
        ) : (
          <DataTable
            columns={columns}
            rows={data}
            rowKey={(s) => s.date}
            isLoading={isLoading}
            empty="No snapshots yet. Take one to freeze the three-part balance for a calendar day."
          />
        )}
      </CardContent>
    </Card>
  );
}

// --- Page -----------------------------------------------------------------

export default function DepositAccountDetailPage() {
  const params = useParams();
  const router = useRouter();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const did = typeof params.did === "string" ? params.did : "";

  const { data: account, isLoading, error, refetch } = useDepositAccount(pid, did);
  const setStatus = useSetDepositStatus(pid, did);
  const close = useCloseDepositAccount(pid);

  const back = `/participants/${pid}/deposit-accounts`;

  if (error) {
    return (
      <div className="space-y-4">
        <Link
          href={back}
          className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-4" /> Deposit accounts
        </Link>
        <ErrorState error={error} onRetry={() => refetch()} />
      </div>
    );
  }

  const transitions = account ? STATUS_TRANSITIONS[account.status] : [];

  return (
    <div className="space-y-5">
      <Link
        href={back}
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-4" /> Deposit accounts
      </Link>

      {isLoading || !account ? (
        <Skeleton className="h-10 w-64" />
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <h2 className="text-xl font-semibold tracking-tight">
                {account.name}
              </h2>
              <EnumBadge value={account.status} />
              <CopyId id={account.id} />
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {account.status !== "Closed" && (
                <FundParticipantForm pid={pid} did={did} />
              )}
              {transitions.map((t) => (
                <ConfirmAction
                  key={t.action}
                  trigger={
                    <Button variant="outline" size="sm">
                      {t.label}
                    </Button>
                  }
                  title={t.label}
                  description={t.description}
                  confirmLabel={t.label}
                  pending={setStatus.isPending}
                  onConfirm={async () => {
                    await setStatus.mutateAsync(
                      { action: t.action },
                      {
                        onSuccess: () => toast.success(`Account ${t.label.toLowerCase()}d`),
                        onError: (err) => toast.error(describeError(err)),
                      },
                    );
                  }}
                />
              ))}
              {account.status !== "Closed" && (
                <ConfirmAction
                  trigger={
                    <Button variant="destructive" size="sm">
                      Close
                    </Button>
                  }
                  title="Close account"
                  description="Terminal and irreversible. Requires a zero balance — fully withdraw or transfer the funds first."
                  confirmLabel="Close"
                  destructive
                  pending={close.isPending}
                  onConfirm={async () => {
                    await close.mutateAsync(did, {
                      onSuccess: () => {
                        toast.success("Account closed");
                        router.push(back);
                      },
                      onError: (err) => toast.error(describeError(err)),
                    });
                  }}
                />
              )}
            </div>
          </div>

          <p className="flex items-center gap-1.5 text-sm text-muted-foreground">
            Backed by GL account <CopyId id={account.glAccount} /> · overdraft
            limit <Money cents={account.overdraftLimit} />
            <Hint id="overdraft" />
          </p>

          <BalanceCard pid={pid} did={did} />
          <HoldsCard pid={pid} did={did} />
          <SnapshotsCard pid={pid} did={did} />
        </>
      )}
    </div>
  );
}
