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
  useBankPayment,
  useDepositAccount,
  useDepositBalance,
  useSchemes,
  useSubmitPayment,
} from "@/lib/api/hooks";
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
  // What the payer says about the payee, and since Task 18a it is three things:
  // the IBAN, the NAME and the BIC.
  //
  // # The BIC field was removed and is back, and the reversal is the lesson
  //
  // This form used to ask for the payee's bank; Task 14 took the field away,
  // because the clearing house relays a pacs.008 on CdtrAgt and a wrong BIC here
  // sent the payment to the wrong bank. The submitting bank derived it instead,
  // from the payee's own bank record — which is what a SEPA originating bank
  // does, IBAN-only since 2016.
  //
  // That record belongs to the payee's bank, and under a store per entity a bank
  // holds only its own. There is no other source: the clearing house's roster is
  // keyed by the BIC being asked for, and this network has no IBAN-to-BIC
  // directory service — which is the thing SEPA's IBAN-only rule actually rests
  // on. So the address a payer gives is an IBAN AND a BIC, as it was before 2016
  // and as it still is for a payment outside SEPA.
  //
  // What makes that safe is not this form. A wrong BIC now sends the payment to
  // the bank that was named, and THAT bank refuses it — it holds no such IBAN in
  // its own register, so it answers AC01 and the payer's debit is reversed. It is
  // the narrowing of the address lookup, not the removal of the field, that
  // stopped a typed BIC being able to misapply somebody's money.
  //
  // # There is no payee lookup here at all any more
  //
  // There was: GET /directory resolved the typed IBAN across the whole network
  // and this form would not submit until it had. It cannot, because answering it
  // meant reading every bank's deposit register. A bank's own directory answers
  // only for its own customers now, so a lookup here would confirm exactly the
  // payees this form is not for.
  //
  // Nothing is lost that a payer really had. A real payer is not told their
  // payee's name back by their bank before they pay — they read it off an
  // invoice — and this form used to render the resolved bank one line above the
  // fields asking the payer to type the payee's details, which taught the
  // opposite of what the system does.
  const [creditorName, setCreditorName] = useState("");
  const [creditorAgent, setCreditorAgent] = useState("");
  // The identifier the bank accepted. Holding it is what makes this form the
  // shape 7b needs: the answer to "did it work?" is a second request, not a
  // return value.
  const [acceptedId, setAcceptedId] = useState<string | null>(null);

  const asset = account ? byCode.get(account.asset) : undefined;
  const scheme = schemes?.find((s) => s.id === SEND_SCHEME);
  const frozen = account?.status === "Frozen";
  const closed = account?.status === "Closed";
  const ownIban = account?.identifiers.find((i) => i.scheme === "IBAN");
  // Paying yourself is the one payee mistake this form can still catch on its
  // own, because it needs no register: the address typed is the address of the
  // account being paid FROM. Everything else about the payee is somebody else's
  // to answer, and the answer arrives as a rejection.
  const payingSelf =
    ownIban != null && iban.trim() !== "" && ownIban.value === iban.trim();

  // Folding an assets failure into "still loading" would leave a customer
  // staring at a skeleton with no error and no retry — the account can be
  // fine while /assets is the thing that is down.
  if (accountError || assetError) return <ErrorState error={accountError ?? assetError} />;
  if (!account || !asset) return <Skeleton className="h-64 w-full" />;

  // The scheme settles in one asset and this account holds one; a mismatch is not
  // a form error the customer can fix, so it is stated rather than hidden.
  const assetMismatch = scheme != null && scheme.asset !== account.asset;

  const canSend =
    !frozen &&
    !closed &&
    !assetMismatch &&
    !payingSelf &&
    iban.trim() !== "" &&
    amount != null &&
    amount > 0 &&
    creditorName.trim() !== "" &&
    creditorAgent.trim() !== "";

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!canSend) return;
    try {
      const accepted = await submit.mutateAsync({
        scheme: SEND_SCHEME,
        debtor: { participant: pid, account: did },
        // The payee is named by ADDRESS and by nothing else. participant and
        // account are the payee bank's own internal keys and a payer has never
        // had any way to know them — this form used to fill them from the
        // directory sweep, which is exactly the read that closed. The receiving
        // bank resolves the IBAN in its own register and fills them in
        // (payment.AcceptInboundTx).
        creditor: {
          participant: "",
          account: "",
          identifier: { scheme: "IBAN", value: iban.trim() },
        },
        amount: amount!,
        description: reference.trim() || undefined,
        // The payer's own account of who they are paying, and where. See the
        // state declaration above for why both are the payer's to give.
        creditorName: creditorName.trim(),
        creditorAgent: creditorAgent.trim(),
      });
      setAcceptedId(accepted.paymentId);
      setIban("");
      setAmount(null);
      setReference("");
      setCreditorName("");
      setCreditorAgent("");
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
              {payingSelf && (
                <p className="text-xs text-destructive">
                  That is this account&apos;s own IBAN.
                </p>
              )}
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
              <FieldLabel htmlFor="send-creditor-agent" hint="account-addressing" required>
                Payee&apos;s bank (BIC)
              </FieldLabel>
              <Input
                id="send-creditor-agent"
                value={creditorAgent}
                placeholder="VERDITMMXXX"
                className="font-mono"
                disabled={frozen || closed}
                onChange={(e) => setCreditorAgent(e.target.value.toUpperCase())}
              />
              <p className="text-xs text-muted-foreground">
                Off the invoice, with the IBAN. Your bank cannot look this up —
                nothing here holds an index of who banks where — and a payment
                sent to the wrong bank comes back refused.
              </p>
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
