"use client";

import { DataTable, type Column } from "@/components/data-table";
import { Hint } from "@/components/hint";
import { Money } from "@/components/money";
import { formatDate } from "@/lib/dates";
import type { AssetScale } from "@/lib/money";
import { cn } from "@/lib/utils";
import type { Installment } from "@/lib/types";

// AmortizationSchedule is a facility's instalment plan, as it was generated at
// disbursement (or, for a revolving line, appended one row per billing
// cycle). `outstanding` is each row's OWN unpaid remainder — see the
// Installment type's doc comment — not the facility's overall balance, so a
// fully-paid early row and an unpaid later one can both be on screen at once.
//
// An instalment is overdue when it is still owed past its due date; those
// rows are tinted rather than hidden, because a repayment settles accrued
// interest before the schedule (see the README's Lending section), so an
// earlier row can stay overdue even once later ones look current.
//
// Reuses DataTable, which already scrolls its own table body in an
// `overflow-x-auto` container — this schedule can run to dozens of rows
// without widening the page.
export function AmortizationSchedule({
  installments,
  asset,
  isLoading,
}: {
  installments: Installment[] | undefined;
  asset: AssetScale;
  isLoading?: boolean;
}) {
  const today = new Date().toISOString().slice(0, 10);

  function isOverdue(i: Installment): boolean {
    return i.outstanding > 0 && i.dueDate.slice(0, 10) < today;
  }

  const columns: Column<Installment>[] = [
    { key: "seq", header: "#", render: (i) => i.seq },
    { key: "dueDate", header: "Due date", render: (i) => formatDate(i.dueDate) },
    {
      key: "principal",
      header: "Principal",
      align: "right",
      render: (i) => <Money amount={i.principal} asset={asset} />,
    },
    {
      key: "interest",
      header: "Interest",
      align: "right",
      render: (i) => <Money amount={i.interest} asset={asset} />,
    },
    {
      key: "total",
      header: "Total",
      align: "right",
      render: (i) => <Money amount={i.principal + i.interest} asset={asset} />,
    },
    {
      key: "paid",
      header: "Paid",
      align: "right",
      render: (i) => (
        <Money amount={i.paidPrincipal + i.paidInterest} asset={asset} />
      ),
    },
    {
      key: "outstanding",
      header: "Outstanding",
      align: "right",
      render: (i) => <Money amount={i.outstanding} asset={asset} />,
    },
  ];

  return (
    <div className="space-y-1.5">
      <h3 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
        Amortization schedule
        <Hint id="amortization" />
      </h3>
      <DataTable
        columns={columns}
        rows={installments}
        rowKey={(i) => String(i.seq)}
        rowClassName={(i) => cn(isOverdue(i) && "bg-red-50 dark:bg-red-950/30")}
        isLoading={isLoading}
        empty="No instalments yet. A term loan generates its schedule on disbursement; a revolving line's instalments appear as billing cycles are charged."
      />
    </div>
  );
}
