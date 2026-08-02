"use client";

import { useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { toast } from "sonner";

import { Card, CardContent } from "@/components/ui/card";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { FieldLabel } from "@/components/field-label";
import { EnumBadge } from "@/components/enum-badge";
import { MoneyInput, Money } from "@/components/money";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import {
  useAssetLookup,
  useBankDirectory,
  useBankPayment,
  useDepositAccount,
  useDepositBalance,
  useParticipants,
  useSchemes,
  useSubmitPayment,
} from "@/lib/api/hooks";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import { describeError } from "@/lib/api/errors";

// A retail "send money" is a SEPA credit transfer: a push scheme needing no
// mandate, addressed by IBAN. Naming it here rather than offering a scheme picker
// is the point — a customer picks a payee, not a clearing arrangement.
const SEND_SCHEME = "sepa.ct";

export default function CustomerSend() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const did = typeof params.did === "string" ? params.did : "";

  const { data: account, error: accountError } = useDepositAccount(pid, did);
  const {
    data: balance,
    error: balanceError,
  } = useDepositBalance(pid, did);
  const { data: schemes } = useSchemes();
  const { byCode, error: assetError } = useAssetLookup();
  const submit = useSubmitPayment(pid);

  const [iban, setIban] = useState("");
  const [amount, setAmount] = useState<number | null>(null);
  const [reference, setReference] = useState("");
  // What the payer says about the payee. GET /directory
  // (api/handlers_directory.go's handleResolveIdentifier) resolves the typed
  // IBAN across the network and reads the resolved account — bank, asset and
  // name — off the payee's own bank's deposit register, but PayeeLine below
  // shows only the bank: rendering the resolved name would teach that the
  // payer's bank confirms who it is paying, and it does not — the name that
  // goes on the instruction is the one the payer typed. Neither field below is
  // populated from that answer: the payer types both independently, and the
  // request carries only what was typed.
  const [creditorAgent, setCreditorAgent] = useState("");
  const [creditorName, setCreditorName] = useState("");
  // The identifier the bank accepted. Holding it is what makes this form the
  // shape 7b needs: the answer to "did it work?" is a second request, not a
  // return value.
  const [acceptedId, setAcceptedId] = useState<string | null>(null);

  // Resolved live as you type, settled first so a keystroke is not a request.
  // Through the customer's own bank — a retail client has no CSM connection.
  const settledIban = useDebouncedValue(iban.trim(), 350);
  const payee = useBankDirectory(pid, settledIban ? "IBAN" : "", settledIban);

  const asset = account ? byCode.get(account.asset) : undefined;
  const scheme = schemes?.find((s) => s.id === SEND_SCHEME);
  const frozen = account?.status === "Frozen";
  const closed = account?.status === "Closed";
  const ownIban = account?.identifiers.find((i) => i.scheme === "IBAN");

  // Folding an assets failure into "still loading" would leave a customer
  // staring at a skeleton with no error and no retry — the account can be
  // fine while /assets is the thing that is down.
  if (accountError || assetError) return <ErrorState error={accountError ?? assetError} />;
  if (!account || !asset) return <Skeleton className="h-64 w-full" />;

  // The scheme settles in one asset and this account holds one; a mismatch is not
  // a form error the customer can fix, so it is stated rather than hidden.
  const assetMismatch = scheme != null && scheme.asset !== account.asset;
  const payingSelf = payee.data?.account === did && payee.data?.participant === pid;

  const canSend =
    !frozen &&
    !closed &&
    !assetMismatch &&
    payee.data != null &&
    !payingSelf &&
    amount != null &&
    amount > 0 &&
    creditorAgent.trim() !== "" &&
    creditorName.trim() !== "";

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!canSend || !payee.data) return;
    try {
      const accepted = await submit.mutateAsync({
        scheme: SEND_SCHEME,
        // Routing is by id, which is why the IBAN had to be resolved. The
        // identifier is quoted so the payment records the address it was reached
        // by; initiation would back-fill it either way.
        debtor: { participant: pid, account: did },
        creditor: {
          participant: payee.data.participant,
          account: payee.data.account,
          identifier: payee.data.identifier,
        },
        amount: amount!,
        description: reference.trim() || undefined,
        // The payer's own account of who they're paying — see the state
        // declarations above for why this bank cannot supply it instead.
        creditorAgent: creditorAgent.trim().toUpperCase(),
        creditorName: creditorName.trim(),
      });
      setAcceptedId(accepted.paymentId);
      setIban("");
      setAmount(null);
      setReference("");
      setCreditorAgent("");
      setCreditorName("");
    } catch (err) {
      toast.error(describeError(err));
    }
  }

  return (
    <div className="space-y-5">
      <h1 className="text-2xl font-semibold tracking-tight">Send money</h1>

      {frozen && (
        <Alert>
          <AlertTitle>This account is frozen</AlertTitle>
          <AlertDescription>
            Nothing can leave it until your bank unfreezes it. Money can still
            arrive.
          </AlertDescription>
        </Alert>
      )}
      {closed && (
        <Alert>
          <AlertTitle>This account is closed</AlertTitle>
          <AlertDescription>Closed is terminal — it cannot send or receive.</AlertDescription>
        </Alert>
      )}
      {assetMismatch && (
        <Alert>
          <AlertTitle>Nothing to send with</AlertTitle>
          <AlertDescription>
            This account holds {account.asset} and the transfer scheme settles in{" "}
            {scheme?.asset}. A payment settles in one asset, so there is no such
            transfer to make.
          </AlertDescription>
        </Alert>
      )}

      {acceptedId && (
        <Outcome pid={pid} did={did} payid={acceptedId} onDismiss={() => setAcceptedId(null)} />
      )}

      <Card>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            <div className="space-y-1.5">
              <FieldLabel htmlFor="send-iban" hint="account-addressing" required>
                Pay to (IBAN)
              </FieldLabel>
              <Input
                id="send-iban"
                value={iban}
                placeholder={ownIban ? ownIban.value.replace(/\d{4}$/, "0000") : "SE89-…"}
                className="font-mono"
                disabled={frozen || closed}
                onChange={(e) => setIban(e.target.value)}
              />
              <PayeeLine
                query={settledIban}
                isLoading={payee.isLoading}
                error={payee.error}
                bank={payee.data?.participant}
                payingSelf={payingSelf}
              />
            </div>

            <div className="space-y-1.5">
              <FieldLabel htmlFor="send-creditor-agent" required>
                Payee&apos;s bank (BIC)
              </FieldLabel>
              <Input
                id="send-creditor-agent"
                value={creditorAgent}
                placeholder="BNKADEFFXXX"
                className="font-mono uppercase"
                disabled={frozen || closed}
                onChange={(e) => setCreditorAgent(e.target.value)}
              />
            </div>

            <div className="space-y-1.5">
              <FieldLabel htmlFor="send-creditor-name" hint="counterparty-details" required>
                Payee&apos;s name
              </FieldLabel>
              <Input
                id="send-creditor-name"
                value={creditorName}
                disabled={frozen || closed}
                onChange={(e) => setCreditorName(e.target.value)}
              />
            </div>

            <div className="space-y-1.5">
              <FieldLabel htmlFor="send-amount" required>
                Amount
              </FieldLabel>
              <MoneyInput
                id="send-amount"
                value={amount}
                onChange={setAmount}
                asset={asset}
                disabled={frozen || closed}
              />
              <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <span>Available</span>
                {balanceError ? (
                  <span className="text-destructive">{describeError(balanceError)}</span>
                ) : balance ? (
                  <Money amount={balance.available} asset={asset} />
                ) : (
                  <Skeleton className="h-4 w-16" />
                )}
                <Hint id="balance-available" />
              </p>
            </div>

            <div className="space-y-1.5">
              <FieldLabel htmlFor="send-ref">Reference (optional)</FieldLabel>
              <Input
                id="send-ref"
                value={reference}
                disabled={frozen || closed}
                onChange={(e) => setReference(e.target.value)}
              />
            </div>

            <Button type="submit" disabled={!canSend || submit.isPending}>
              {submit.isPending ? "Sending…" : "Send"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}

// What the directory said about the address typed so far — the BANK it
// routes to, and nothing about who holds it: naming the payee is the payer's
// job, done in the two fields below this line, not something the directory
// answers for them. A miss is an answer and is stated plainly; an ambiguous
// address — two banks claiming it — is a 409 and describeError names it.
function PayeeLine({
  query,
  isLoading,
  error,
  bank,
  payingSelf,
}: {
  query: string;
  isLoading: boolean;
  error: unknown;
  bank?: string;
  payingSelf: boolean;
}) {
  // The directory answers with a participant id; the customer needs its name.
  const { data: participants } = useParticipants();
  if (!query) return null;
  if (isLoading) return <Skeleton className="h-4 w-40" />;
  if (error) return <p className="text-xs text-destructive">{describeError(error)}</p>;
  if (payingSelf) {
    return <p className="text-xs text-destructive">That is this account&apos;s own IBAN.</p>;
  }
  if (!bank) return null;
  const bankName = participants?.find((p) => p.id === bank)?.name ?? bank;
  return (
    <p className="text-xs text-muted-foreground">
      Routes to <span className="font-medium text-foreground">{bankName}</span>
    </p>
  );
}

// The second half of a 202. The bank answered with an identifier and no outcome,
// so the outcome is a second request — which is what this is.
//
// The wait is real since 7b: the payee's bank and then the clearing house answer
// with a pacs.002, at other actors, after this request finished — so this panel
// can show Initiated for a moment before it shows Accepted. Nothing here had to
// change when the behaviour caught up with the shape.
function Outcome({
  pid,
  did,
  payid,
  onDismiss,
}: {
  pid: string;
  did: string;
  payid: string;
  onDismiss: () => void;
}) {
  const { data, isLoading, error } = useBankPayment(pid, payid);
  const { byCode } = useAssetLookup();
  const asset = data ? byCode.get(data.asset) : undefined;

  return (
    <Alert>
      <AlertTitle className="flex items-center gap-2">
        Instruction accepted
        {data && <EnumBadge value={data.status} />}
      </AlertTitle>
      <AlertDescription className="space-y-2">
        {error ? (
          <span>{describeError(error)}</span>
        ) : isLoading || !data || !asset ? (
          <span>Your bank took the instruction and gave it a reference. Asking what became of it…</span>
        ) : (
          <span>
            <Money amount={data.amount} asset={asset} /> to{" "}
            {data.creditor.identifier?.value ?? data.creditor.account}. Your bank
            answered with a reference rather than an outcome, and this is the
            answer to asking again.
          </span>
        )}
        <span className="flex gap-3">
          <Link
            href={`/customer/${pid}/${did}/activity`}
            className="text-xs underline underline-offset-2"
          >
            See it on your activity
          </Link>
          <button type="button" onClick={onDismiss} className="text-xs underline underline-offset-2">
            Dismiss
          </button>
        </span>
      </AlertDescription>
    </Alert>
  );
}
