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
  useAddressRoute,
  useAssetLookup,
  useBankPayment,
  useDepositAccount,
  useDepositBalance,
  useSchemes,
  useSubmitPayment,
} from "@/lib/api/hooks";
import { ApiError, describeError } from "@/lib/api/errors";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import { checkDigitsPass, compactIban, groupIban } from "@/lib/iban";

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
  // What the payer says about the payee is TWO things: the IBAN and the NAME.
  //
  // # There is no BIC field, and its absence is the whole lesson
  //
  // The payee's bank is derived, from the bank code inside the address, through
  // this bank's own copy of the scheme's routing directory. That is what
  // IBAN-only means, and it is what SEPA has been since February 2016 — not
  // because routing is computable from an address, but because every bank
  // subscribes to a table that pairs the two. A payer able to type the BIC is a
  // payer able to choose which bank receives their money, since that element is
  // what the clearing house relays on.
  //
  // # The NAME is still typed, and no lookup will ever fill it in
  //
  // Resolving the typed IBAN to a person would mean reading the payee's bank's
  // deposit register, which no bank here may do. So a real payer reads the name
  // off an invoice, and so does this one. What the resolution below CAN show is
  // the institution — and it shows a BIC and no name, because the copy holds
  // none, because the roster holds none, because the acknowledgement it is
  // written from delivers none.
  const [creditorName, setCreditorName] = useState("");
  // The identifier the bank accepted. Holding it is what makes this form the
  // shape 7b needs: the answer to "did it work?" is a second request, not a
  // return value.
  const [acceptedId, setAcceptedId] = useState<string | null>(null);

  const asset = account ? byCode.get(account.asset) : undefined;
  const scheme = schemes?.find((s) => s.id === SEND_SCHEME);
  const frozen = account?.status === "Frozen";
  const closed = account?.status === "Closed";
  const ownIban = account?.identifiers.find((i) => i.scheme === "IBAN");
  // Two payee mistakes this form catches on its own, and they are the only two
  // it can: both are answerable without reading anybody's register.
  //
  // The CHECK DIGITS are the whole reason an IBAN carries any. Running them
  // here, as the payer types, is where the offline half of addressing becomes
  // visible — a transposed pair is caught before a request exists, let alone a
  // message. What it cannot say is whether anybody holds the address; that is
  // the payee's bank's answer, and it comes back as a rejection.
  //
  // And paying YOURSELF, because the address typed is the address of the
  // account being paid from. Everything else about the payee is somebody else's
  // to answer.
  const typed = iban.trim();
  const malformed = typed !== "" && !checkDigitsPass(typed);
  const payingSelf =
    ownIban != null && typed !== "" && compactIban(ownIban.value) === compactIban(typed);

  // The routing half, and it runs only once the OFFLINE half has passed: the
  // check digits are what makes it worth asking anybody, so an address that
  // fails them costs no request at all. Debounced because the field is typed
  // into, and settled on the compact form so grouping does not make two cache
  // entries out of one address.
  //
  // This is the same read submission makes, out of the same copy, so the bank
  // this shows and the bank the payment reaches cannot disagree. What it cannot
  // show is a NAME — the copy holds none — which is the documented absence
  // arriving at the moment a payer most expects one.
  const settledIban = useDebouncedValue(
    !malformed && !payingSelf && typed !== "" ? compactIban(typed) : "",
    300,
  );
  const route = useAddressRoute(pid, settledIban);
  // A 422 from that lookup is an ANSWER — this bank's copy holds no entry for
  // the address's bank code — and it is the one refusal on this form worth
  // spelling out rather than summarising. Anything else is a failure to ask.
  const unroutable = route.error instanceof ApiError && route.error.status === 422;

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
    !malformed &&
    // Not a second copy of the rule: this IS the bank's own answer, already
    // fetched, out of the same copy submission will read. Pressing Send would
    // ask the same question again and get the same refusal.
    !unroutable &&
    typed !== "" &&
    amount != null &&
    amount > 0 &&
    creditorName.trim() !== "";

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!canSend) return;
    try {
      const accepted = await submit.mutateAsync({
        scheme: SEND_SCHEME,
        debtor: { account: did },
        // The payee is named by ADDRESS and by nothing else. The account is the
        // payee bank's own internal key and a payer has no way to know it;
        // filling it would take a directory sweep, which is a read no bank may
        // make. The receiving bank resolves the IBAN in its own register and
        // fills it in (payment.AcceptInboundTx).
        creditor: {
          account: "",
          // Sent as the payer typed it. A register compares addresses in
          // canonical form on both sides, so the grouping a customer copies off
          // an invoice resolves against the compact form the payee's bank
          // stored — there is nothing for this form to normalise away.
          identifier: { scheme: "IBAN", value: typed },
        },
        amount: amount!,
        description: reference.trim() || undefined,
        // The payer's own account of who they are paying. There is no bank
        // beside it: see the state declaration above.
        creditorName: creditorName.trim(),
      });
      setAcceptedId(accepted.paymentId);
      setIban("");
      setAmount(null);
      setReference("");
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
                placeholder="DE20 9990 0001 0000 0000 01"
                className="font-mono"
                disabled={frozen || closed}
                onChange={(e) => setIban(e.target.value)}
              />
              {payingSelf ? (
                <p className="text-xs text-destructive">
                  That is this account&apos;s own IBAN.
                </p>
              ) : malformed ? (
                <p className="text-xs text-destructive">
                  That is not a valid IBAN — its check digits do not match the
                  rest of it, so a character is wrong or two have been swapped.
                  Nothing has been sent. <Hint id="iban-check-digit" />
                </p>
              ) : typed !== "" ? (
                <p className="text-xs text-muted-foreground">
                  Check digits pass: {groupIban(typed)}. That says it was
                  probably typed correctly — not that anyone holds it.{" "}
                  <Hint id="iban-check-digit" />
                </p>
              ) : null}
              {/* The routing answer, one line under the address it came out
                  of. It is a BIC and it is not a name, and saying so is the
                  point: the copy this came from holds none, because the roster
                  holds none. A payer who expected "Banca Verde" is meeting the
                  documented absence at the moment it is sharpest. */}
              {settledIban !== "" && (
                <p className="text-xs text-muted-foreground">
                  {route.isPending ? (
                    "Asking your bank where this address goes…"
                  ) : unroutable ? (
                    // Deliberately NOT describeError: its 422 line offers
                    // "insufficient funds, frozen account", which is a list this
                    // refusal is not on. What a payer needs is the honest
                    // ambiguity, and the honest ambiguity is the whole point.
                    <span className="text-destructive">
                      Your bank cannot route this address. It routes from a copy
                      of the scheme&apos;s directory that it pulled, so it cannot
                      tell you whether no bank holds that code or whether its own
                      copy is simply behind — the remedy is a refresh, or a payee
                      who is not in this scheme. <Hint id="routing-directory" />
                    </span>
                  ) : route.error ? (
                    <span className="text-destructive">
                      {describeError(route.error)}
                    </span>
                  ) : route.data ? (
                    <>
                      Routes to <span className="font-mono">{route.data.bic}</span>{" "}
                      — a bank code, resolved in your bank&apos;s copy of the
                      scheme&apos;s directory. There is no name to show: nothing
                      ever told the scheme one. <Hint id="routing-directory" />
                    </>
                  ) : null}
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
