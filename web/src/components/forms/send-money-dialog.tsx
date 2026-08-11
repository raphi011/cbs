"use client";

import { useState } from "react";
import { Send } from "lucide-react";
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
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { FieldLabel } from "@/components/field-label";
import { EnumBadge } from "@/components/enum-badge";
import { MoneyInput, Money } from "@/components/money";
import { Hint } from "@/components/hint";
import {
  useAddressRoute,
  useAssetLookup,
  useBankDirectory,
  useBankPayment,
  useDepositBalance,
  useSchemes,
  useSubmitPayment,
  useTransfer,
} from "@/lib/api/hooks";
import { ApiError, describeError } from "@/lib/api/errors";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import { checkDigitsPass, compactIban, groupIban } from "@/lib/iban";
import type { Asset, DepositAccount, Transfer } from "@/lib/types";

// A retail "send money" is a SEPA credit transfer: a push scheme needing no
// mandate, addressed by IBAN. Naming it here rather than offering a scheme picker
// is the point — a customer picks a payee, not a clearing arrangement.
const SEND_SCHEME = "sepa.ct";

// The two acts this form can perform, told apart by whether the payee banks
// here. A payment's answer is an identifier to ask again with; a transfer's is
// the outcome itself, which is why one carries an id and the other a receipt.
export type SendResult =
  | { kind: "payment"; paymentId: string }
  | { kind: "transfer"; receipt: Transfer; amount: number };

export function SendMoneyDialog({
  pid,
  did,
  account,
  asset,
  onSent,
}: {
  pid: string;
  did: string;
  account: DepositAccount;
  asset: Asset;
  onSent: (result: SendResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const { data: balance, error: balanceError } = useDepositBalance(pid, did);
  const { data: schemes } = useSchemes();
  const submit = useSubmitPayment(pid);
  const transfer = useTransfer(pid);

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

  const scheme = schemes?.find((s) => s.id === SEND_SCHEME);
  const frozen = account.status === "Frozen";
  const closed = account.status === "Closed";
  const ownIban = account.identifiers.find((i) => i.scheme === "IBAN");
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

  // The OTHER directory, and it is the one that decides which act this is: does
  // the typed address resolve in this customer's own bank's register. A hit
  // means the payee banks here, so nothing leaves the institution and there is
  // no payment to make — only a book transfer. A 404 is an answer and not a
  // failure: the payee is somebody else's customer.
  //
  // It runs BESIDE the routing lookup rather than before it. The two answer
  // different questions and only one of them can be answered out of a table this
  // bank owns; asking both at once costs a routing lookup on the addresses that
  // turn out to be on-us, and saves a round trip on every address that does not.
  //
  // This is the same question the bank asks itself at submission, and it is
  // asked of the REGISTER rather than by comparing bank codes: a code says which
  // institution issues an address, not that the institution holds the account.
  const own = useBankDirectory(pid, "IBAN", settledIban);
  const onUs = own.data != null;

  // The scheme settles in one asset and this account holds one; a mismatch is not
  // a form error the customer can fix, so it is stated rather than hidden.
  const assetMismatch = scheme != null && scheme.asset !== account.asset;

  // Neither of the two scheme rules applies to a transfer, and both would be
  // wrong about one. A scheme's asset governs what may cross between banks; a
  // transfer's rule is that the two ACCOUNTS agree, which is the bank's to check.
  // And an address that resolves here needs no routing at all.
  const canSend =
    !frozen &&
    !closed &&
    (onUs || !assetMismatch) &&
    !payingSelf &&
    !malformed &&
    // Not a second copy of the rule: this IS the bank's own answer, already
    // fetched, out of the same copy submission will read. Pressing Send would
    // ask the same question again and get the same refusal.
    (onUs || !unroutable) &&
    typed !== "" &&
    amount != null &&
    amount > 0 &&
    // The payee's NAME is a payment's field and not a transfer's: it goes on the
    // wire because nothing at the far end can be asked, and a transfer has no
    // wire and no far end.
    (onUs || creditorName.trim() !== "");

  function clear() {
    setIban("");
    setAmount(null);
    setReference("");
    setCreditorName("");
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!canSend) return;
    if (onUs) {
      try {
        // The payee is named by the ADDRESS and by nothing else here either.
        // The account id the register resolved is this bank's own internal key,
        // and the payer neither has it nor needs it.
        const receipt = await transfer.mutateAsync({
          from: did,
          to: typed,
          amount: amount!,
          description: reference.trim() || undefined,
        });
        // The amount travels with the receipt because the receipt carries only a
        // balance, and the fields it could be read off are cleared below.
        onSent({ kind: "transfer", receipt, amount: amount! });
        clear();
        setOpen(false);
      } catch (err) {
        toast.error(describeError(err));
      }
      return;
    }
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
      onSent({ kind: "payment", paymentId: accepted.paymentId });
      clear();
      setOpen(false);
    } catch (err) {
      toast.error(describeError(err));
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        {/* A frozen or closed account has nothing to send with, and the reason
            is on the page beside this button rather than behind it. */}
        <Button disabled={frozen || closed}>
          <Send className="size-4" />
          Send money
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={onSubmit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Send money</DialogTitle>
            <DialogDescription>
              A payee is named by their IBAN and their name. Which act this turns
              out to be — a payment or a book transfer — is your bank&apos;s
              answer, not yours.
            </DialogDescription>
          </DialogHeader>

          {assetMismatch && !onUs && (
            <Alert>
              <AlertTitle>Nothing to send with</AlertTitle>
              <AlertDescription>
                This account holds {account.asset} and the transfer scheme settles
                in {scheme?.asset}. A payment settles in one asset, so there is no
                such transfer to make.
              </AlertDescription>
            </Alert>
          )}

          <div className="space-y-1.5">
            <FieldLabel htmlFor="send-iban" hint="account-addressing" required>
              Pay to (IBAN)
            </FieldLabel>
            <Input
              id="send-iban"
              value={iban}
              placeholder="DE20 9990 0001 0000 0000 01"
              className="font-mono"
              onChange={(e) => setIban(e.target.value)}
            />
            {payingSelf ? (
              <p className="text-xs text-destructive">
                That is this account&apos;s own IBAN.
              </p>
            ) : malformed ? (
              <p className="text-xs text-destructive">
                That is not a valid IBAN — its check digits do not match the rest
                of it, so a character is wrong or two have been swapped. Nothing
                has been sent. <Hint id="iban-check-digit" />
              </p>
            ) : typed !== "" ? (
              <p className="text-xs text-muted-foreground">
                Check digits pass: {groupIban(typed)}. That says it was probably
                typed correctly — not that anyone holds it.{" "}
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
                {onUs ? (
                  // The register's answer wins over the directory's wherever
                  // both have one, because it is the stronger of the two: a
                  // routing entry says which institution ISSUES an address,
                  // and this says that this bank HOLDS the account.
                  <span>
                    That address is one of this bank&apos;s own accounts, so this
                    one does not leave the building. It is a book transfer, not a
                    payment: nothing crosses between institutions, so there is no
                    position to clear, no reserves to move and nobody to tell —
                    and it is finished the moment your bank posts it, rather than
                    answered later by somebody else. <Hint id="book-transfer" />
                  </span>
                ) : route.isPending || own.isPending ? (
                  "Asking your bank where this address goes…"
                ) : unroutable ? (
                  // Deliberately NOT describeError: its 422 line offers
                  // "insufficient funds, frozen account", which is a list this
                  // refusal is not on. What a payer needs is the honest
                  // ambiguity, and the honest ambiguity is the whole point.
                  <span className="text-destructive">
                    Your bank cannot route this address. It routes from a copy of
                    the scheme&apos;s directory that it pulled, so it cannot tell
                    you whether no bank holds that code or whether its own copy is
                    simply behind — the remedy is a refresh, or a payee who is not
                    in this scheme. <Hint id="routing-directory" />
                  </span>
                ) : route.error ? (
                  <span className="text-destructive">{describeError(route.error)}</span>
                ) : route.data ? (
                  <>
                    Routes to <span className="font-mono">{route.data.bic}</span> —
                    a bank code, resolved in your bank&apos;s copy of the
                    scheme&apos;s directory. There is no name to show: nothing ever
                    told the scheme one. <Hint id="routing-directory" />
                  </>
                ) : null}
              </p>
            )}
          </div>

          {/* Gone when the payee banks here, and its absence is the lesson.
              A payer types a name because it goes on the wire — nothing at
              the far end can be asked one — and a book transfer has no wire.
              Your bank knows who holds that address; it still will not tell
              you, because that is the payee's business and not yours. */}
          {!onUs && (
            <div className="space-y-1.5">
              <FieldLabel htmlFor="send-creditor-name" hint="counterparty-details" required>
                Payee&apos;s name
              </FieldLabel>
              <Input
                id="send-creditor-name"
                value={creditorName}
                onChange={(e) => setCreditorName(e.target.value)}
              />
            </div>
          )}

          <div className="space-y-1.5">
            <FieldLabel htmlFor="send-amount" required>
              Amount
            </FieldLabel>
            <MoneyInput id="send-amount" value={amount} onChange={setAmount} asset={asset} />
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
              onChange={(e) => setReference(e.target.value)}
            />
          </div>

          <DialogFooter>
            {/* The button says which of the two acts this is. Same form, same
                address, same amount — what changed is that the payee banks
                here, and so does what the word on the button can honestly
                promise: "Sent" is a reference and "Moved" is an outcome. */}
            <Button type="submit" disabled={!canSend || submit.isPending || transfer.isPending}>
              {onUs
                ? transfer.isPending
                  ? "Moving…"
                  : "Transfer"
                : submit.isPending
                  ? "Sending…"
                  : "Send"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// What became of the last thing sent, on the page the dialog closed back onto.
// The two acts answer differently and the panel says so rather than smoothing it
// over: a payment left with a reference, a transfer is over.
export function SendReceipt({
  pid,
  result,
  asset,
  onDismiss,
}: {
  pid: string;
  result: SendResult;
  asset: Asset;
  onDismiss: () => void;
}) {
  if (result.kind === "transfer") {
    return (
      <Alert>
        <AlertTitle>Moved</AlertTitle>
        <AlertDescription className="space-y-2">
          <span>
            <Money amount={result.amount} asset={asset} />{" "}
            is out of this account and in the payee&apos;s, and it is finished —
            there is no reference to ask about later, because nothing else is
            going to happen. Your balance is now{" "}
            <Money amount={result.receipt.balance.book} asset={asset} />.{" "}
            <Hint id="book-transfer" />
          </span>
          <Dismiss onDismiss={onDismiss} />
        </AlertDescription>
      </Alert>
    );
  }
  return <Outcome pid={pid} payid={result.paymentId} onDismiss={onDismiss} />;
}

// The second half of a 202. The bank answered with an identifier and no outcome,
// so the outcome is a second request — which is what this is.
//
// The wait is real: the payee's bank and then the clearing house answer with a
// pacs.002, at other actors, after this request finished — so this panel can show
// Initiated for a moment before it shows Accepted.
function Outcome({
  pid,
  payid,
  onDismiss,
}: {
  pid: string;
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
          <span>
            Your bank took the instruction and gave it a reference. Asking what
            became of it…
          </span>
        ) : (
          <span>
            <Money amount={data.amount} asset={asset} /> to{" "}
            {data.creditor.identifier?.value ?? data.creditor.account}. Your bank
            answered with a reference rather than an outcome, and this is the
            answer to asking again.
          </span>
        )}
        <Dismiss onDismiss={onDismiss} />
      </AlertDescription>
    </Alert>
  );
}

function Dismiss({ onDismiss }: { onDismiss: () => void }) {
  return (
    <span className="flex">
      <button type="button" onClick={onDismiss} className="text-xs underline underline-offset-2">
        Dismiss
      </button>
    </span>
  );
}
