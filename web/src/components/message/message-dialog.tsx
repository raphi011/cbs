"use client";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { CopyButton } from "@/components/copy-button";
import { ErrorState } from "@/components/error-state";
import { formatBusinessDate } from "@/lib/dates";
import { useMessageDocument } from "@/lib/api/hooks";
import { holderName, type LogHolder } from "@/lib/message";

// One document, as it travelled, out of the log of the institution that holds
// it. It is the only read in this app handed a file's bytes: every listing
// leaves the payload unread and carries its size instead.
//
// Rendering a document is not validating it. The schemas are ISO's to
// redistribute and are not in this repository, so nothing here — or anywhere
// else in it — checks a file against one, and the footer says so rather than
// letting a rendered document imply otherwise.

export function MessageDialog({
  holder,
  seq,
  onClose,
}: {
  holder: LogHolder | null;
  seq: number | null;
  onClose: () => void;
}) {
  const { data, isLoading, error, refetch } = useMessageDocument(holder, seq);

  return (
    <Dialog open={seq !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm">
            {data ? data.msgDefIdr : "Document"}
          </DialogTitle>
          <DialogDescription>
            {data && holder ? describe(holder, data.counterparty, data.direction, data.at, data.orderId) : "The file as it travelled."}
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <ErrorState error={error} onRetry={() => void refetch()} />
        ) : isLoading || !data ? (
          <Skeleton className="h-72 w-full" />
        ) : (
          <>
            <pre className="max-h-[60vh] overflow-auto rounded-md bg-muted p-3 font-mono text-xs leading-relaxed">
              {data.document}
            </pre>
            <div className="flex items-center justify-between gap-2">
              <p className="text-xs text-muted-foreground">
                {"The file as it travelled, message id "}
                <span className="font-mono">{data.msgId}</span>
                {". Nothing here checks it against a schema."}
              </p>
              <CopyButton value={data.document} label="document" />
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

// Whose copy this is, said in full. A crossing is two rows in two logs and the
// same bytes came out of one of them; a reader who cannot tell which cannot
// tell a delivered file from one the recipient never took.
function describe(
  holder: LogHolder,
  counterparty: string,
  direction: string,
  at: string,
  orderId?: string,
): string {
  const way = direction === "sent" ? `sent to ${counterparty}` : `received from ${counterparty}`;
  const order = orderId ? ` · order ${orderId}` : "";
  return `${holderName(holder)}'s log: ${way} on ${formatBusinessDate(at.slice(0, 10))}${order}`;
}
