"use client";

import { useState } from "react";
import { useParams } from "next/navigation";
import { Plus } from "lucide-react";
import { toast } from "sonner";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { FieldLabel } from "@/components/field-label";
import { Hint } from "@/components/hint";
import { CopyId } from "@/components/copy-id";
import { Money } from "@/components/money";
import { AccountTypeBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import {
  useAccountBalance,
  useAccounts,
  useCreateAccount,
  useCreateLedger,
  useCreateSubledger,
  useLedgers,
  useSubledgers,
} from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import { ACCOUNT_TYPES, type AccountType } from "@/lib/enums";
import type { Account, Ledger, Subledger } from "@/lib/types";

// Name-only create dialog (ledgers, subledgers).
function NameDialog({
  triggerLabel,
  title,
  label,
  pending,
  onCreate,
}: {
  triggerLabel: string;
  title: string;
  label: string;
  pending: boolean;
  onCreate: (name: string) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    try {
      await onCreate(name.trim());
      setName("");
      setOpen(false);
    } catch {
      /* toast handled by caller */
    }
  }
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <Plus className="size-4" />
          {triggerLabel}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
          </DialogHeader>
          <div className="space-y-2">
            <FieldLabel htmlFor="name-dialog" required>
              {label}
            </FieldLabel>
            <Input
              id="name-dialog"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <DialogFooter>
            <Button type="submit" disabled={pending || !name.trim()}>
              {pending ? "Creating…" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// Account create dialog (name + chart-of-accounts type).
function AccountDialog({
  pending,
  onCreate,
}: {
  pending: boolean;
  onCreate: (name: string, type: AccountType) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [type, setType] = useState<AccountType>("Asset");
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    try {
      await onCreate(name.trim(), type);
      setName("");
      setOpen(false);
    } catch {
      /* toast handled by caller */
    }
  }
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="ghost" size="sm">
          <Plus className="size-4" />
          Account
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>New account</DialogTitle>
            <DialogDescription>
              An account belongs to one of the five chart-of-accounts types,
              which sets its normal balance.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <FieldLabel htmlFor="acct-name" required>
              Name
            </FieldLabel>
            <Input
              id="acct-name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel hint="normal-balance">Type</FieldLabel>
            <Select value={type} onValueChange={(v) => setType(v as AccountType)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ACCOUNT_TYPES.map((t) => (
                  <SelectItem key={t} value={t}>
                    {t}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button type="submit" disabled={pending || !name.trim()}>
              {pending ? "Creating…" : "Create account"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function AccountRow({ pid, account }: { pid: string; account: Account }) {
  const { data } = useAccountBalance(pid, account.id);
  return (
    <div className="flex items-center justify-between gap-3 px-3 py-2">
      <span className="flex items-center gap-2">
        <span className="text-sm">{account.name}</span>
        <AccountTypeBadge type={account.type} />
        <CopyId id={account.id} />
      </span>
      <span className="text-sm font-medium">
        <Money cents={data?.balance ?? 0} />
      </span>
    </div>
  );
}

function SubledgerBlock({ pid, sub }: { pid: string; sub: Subledger }) {
  const accounts = useAccounts(pid, sub.id);
  const create = useCreateAccount(pid, sub.id);
  return (
    <div className="rounded-md border">
      <div className="flex items-center justify-between gap-2 border-b bg-muted/40 px-3 py-1.5">
        <span className="flex items-center gap-2 text-sm font-medium">
          {sub.name}
          <CopyId id={sub.id} />
        </span>
        <AccountDialog
          pending={create.isPending}
          onCreate={async (name, type) => {
            try {
              const a = await create.mutateAsync({ name, type });
              toast.success(`Created ${a.name}`);
            } catch (e) {
              toast.error(describeError(e));
              throw e;
            }
          }}
        />
      </div>
      <div className="divide-y">
        {accounts.isLoading ? (
          <div className="p-3">
            <Skeleton className="h-4 w-full" />
          </div>
        ) : accounts.data && accounts.data.length > 0 ? (
          accounts.data.map((a) => (
            <AccountRow key={a.id} pid={pid} account={a} />
          ))
        ) : (
          <p className="px-3 py-2 text-sm text-muted-foreground">
            No accounts yet.
          </p>
        )}
      </div>
    </div>
  );
}

function LedgerBlock({ pid, ledger }: { pid: string; ledger: Ledger }) {
  const subs = useSubledgers(pid, ledger.id);
  const create = useCreateSubledger(pid, ledger.id);
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2 text-base">
          {ledger.name}
          <CopyId id={ledger.id} />
        </CardTitle>
        <NameDialog
          triggerLabel="Subledger"
          title="New subledger"
          label="Subledger name"
          pending={create.isPending}
          onCreate={async (name) => {
            try {
              const sl = await create.mutateAsync({ name });
              toast.success(`Created ${sl.name}`);
            } catch (e) {
              toast.error(describeError(e));
              throw e;
            }
          }}
        />
      </CardHeader>
      <CardContent className="space-y-3">
        {subs.isLoading ? (
          <Skeleton className="h-12 w-full" />
        ) : subs.data && subs.data.length > 0 ? (
          subs.data.map((sl) => (
            <SubledgerBlock key={sl.id} pid={pid} sub={sl} />
          ))
        ) : (
          <p className="text-sm text-muted-foreground">No subledgers yet.</p>
        )}
      </CardContent>
    </Card>
  );
}

export default function LedgerPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const ledgers = useLedgers(pid);
  const create = useCreateLedger(pid);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
          Chart of accounts
          <Hint id="ledger-vs-subledger" />
        </h2>
        <NameDialog
          triggerLabel="Ledger"
          title="New ledger"
          label="Ledger name"
          pending={create.isPending}
          onCreate={async (name) => {
            try {
              const l = await create.mutateAsync({ name });
              toast.success(`Created ${l.name}`);
            } catch (e) {
              toast.error(describeError(e));
              throw e;
            }
          }}
        />
      </div>

      {ledgers.error ? (
        <ErrorState error={ledgers.error} onRetry={() => ledgers.refetch()} />
      ) : ledgers.isLoading ? (
        <Skeleton className="h-40 w-full" />
      ) : ledgers.data && ledgers.data.length > 0 ? (
        ledgers.data.map((l) => <LedgerBlock key={l.id} pid={pid} ledger={l} />)
      ) : (
        <p className="text-sm text-muted-foreground">
          No ledgers yet. Create one to start building the chart of accounts.
        </p>
      )}
    </div>
  );
}
