"use client";

import { usePathname } from "next/navigation";
import {
  ArrowLeftRight,
  BookOpen,
  Building2,
  FileSignature,
  Landmark,
  LayoutDashboard,
  Network,
  RefreshCw,
  Repeat,
  ScrollText,
  Search,
  Users,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

// Who you are in the app. There is no observer who sees all of it: a back office
// sees one bank, a customer sees one account, the central bank sees reserves and
// settlement, the clearing house sees the network. The identity is derived from
// the URL and persisted nowhere — a view that is not addressable is not a view,
// so "the customer's version of this account" has to be something you can link
// to, refresh into and go back out of.
//
// A customer identity IS one deposit account: there is no party master, so
// "Alice Andersson" the identity is the pair (Aurora, that account), and a
// second account would be a second identity.
export type Identity =
  | { persona: "central-bank" }
  | { persona: "clearing-house" }
  | { persona: "bank"; pid: string }
  | { persona: "customer"; pid: string; did: string };

export type Persona = Identity["persona"];

export interface NavItem {
  href: string;
  label: string;
  icon: LucideIcon;
  // True only for a persona's home. Every other entry names a section, so a
  // detail page below it keeps its parent highlighted.
  exact?: boolean;
}

// A persona prefix without its context addresses nothing, so "/bank" with no pid
// is no identity at all rather than a bank with an undefined id.
export function identityFromPathname(pathname: string): Identity | null {
  const [prefix, pid, did] = pathname.split("/").filter(Boolean);
  if (prefix === "central-bank") return { persona: "central-bank" };
  if (prefix === "clearing-house") return { persona: "clearing-house" };
  if (prefix === "bank" && pid) return { persona: "bank", pid };
  if (prefix === "customer" && pid && did) return { persona: "customer", pid, did };
  return null;
}

// Null on `/` (always the lobby) and under `/learn/*`, which sit outside the
// persona system.
export function useIdentity(): Identity | null {
  return identityFromPathname(usePathname());
}

export function homeFor(identity: Identity): string {
  switch (identity.persona) {
    case "central-bank":
      return "/central-bank";
    case "clearing-house":
      return "/clearing-house";
    case "bank":
      return `/bank/${identity.pid}`;
    case "customer":
      return `/customer/${identity.pid}/${identity.did}`;
  }
}

// The operator key the proxy routes on — the first segment after /api. One
// function off the same switch as homeFor, because an identity that named a
// persona and a backend separately could name a pair that does not exist.
//
// Three of the four personas are institutions with a listener of their own. A
// customer is not: they reach their bank's, which is what a retail app does. A
// retail client has no clearing-house connection in the real thing either.
export function backendFor(identity: Identity): string {
  switch (identity.persona) {
    case "central-bank":
      return "central-bank";
    case "clearing-house":
      return "clearing-house";
    case "bank":
    case "customer":
      return `bank/${identity.pid}`;
  }
}

// The settlement layer, and nothing that shows an individual payment: a real
// central bank sees reserves move, not who paid whom. That subtraction is the
// point of the split rather than a gap.
const CENTRAL_BANK_NAV: NavItem[] = [
  { href: "/central-bank", label: "Reserves", icon: Landmark, exact: true },
  { href: "/central-bank/audit", label: "Audit", icon: ScrollText },
];

// The network. Clearing is the clearing house's and settlement is the central
// bank's; the CSM keeps the read side of settlements because it needs to know
// whether the cycle it closed has settled, and reading is not doing.
const CLEARING_HOUSE_NAV: NavItem[] = [
  { href: "/clearing-house", label: "Network", icon: LayoutDashboard, exact: true },
  { href: "/clearing-house/payments", label: "Payments", icon: ArrowLeftRight },
  { href: "/clearing-house/cycles", label: "Clearing cycles", icon: RefreshCw },
  { href: "/clearing-house/settlements", label: "Settlements", icon: Building2 },
  { href: "/clearing-house/schemes", label: "Schemes", icon: Network },
  { href: "/clearing-house/directory", label: "Directory", icon: Search },
];

export function navFor(identity: Identity): NavItem[] {
  switch (identity.persona) {
    case "central-bank":
      return CENTRAL_BANK_NAV;
    case "clearing-house":
      return CLEARING_HOUSE_NAV;
    case "bank": {
      const base = `/bank/${identity.pid}`;
      return [
        { href: base, label: "Customers", icon: Users, exact: true },
        { href: `${base}/payments`, label: "Payments", icon: ArrowLeftRight },
        // The book transfer, beside Payments and not inside it. Two customers of
        // THIS bank paying each other never reaches a scheme: nothing crosses
        // between institutions, so there is no position to clear and no reserve
        // to move, and this bank posts both legs itself. It is the route a
        // submitted payment is refused in favour of.
        { href: `${base}/transfers`, label: "Transfers", icon: Repeat },
        // Mandates are here and not on the clearing house's console. In SEPA
        // the CREDITOR holds the mandate, so the row is the collecting bank's
        // and what this bank sees is its own customers' authorisations. See
        // payment.Mandate.
        { href: `${base}/mandates`, label: "Mandates", icon: FileSignature },
        // This bank's COPY of the scheme's directory, which is a different
        // screen from the clearing house's entry of the same name: that one is
        // the publication, this one is a snapshot of it in this bank's own
        // database, and every payment this bank submits routes from it.
        { href: `${base}/directory`, label: "Directory", icon: Search },
        { href: `${base}/ledger`, label: "General ledger", icon: BookOpen },
        { href: `${base}/transactions`, label: "Transactions", icon: ArrowLeftRight },
        { href: `${base}/facilities`, label: "Facilities", icon: Landmark },
        { href: `${base}/audit`, label: "Ledger audit", icon: ScrollText },
        { href: `${base}/deposit-audit`, label: "Deposit audit", icon: ScrollText },
      ];
    }
    // A customer has one screen and therefore no navigation. What they hold,
    // what they can do with it and what has happened to it are one page, because
    // sending is an act rather than a place: the empty list is what the shell
    // reads to render no tab strip at all.
    case "customer":
      return [];
  }
}
