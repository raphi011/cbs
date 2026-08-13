"use client";

import { useMemo } from "react";

import { cn } from "@/lib/utils";
import {
  CANVAS,
  layout,
  restingByWire,
  type Edge,
  type InstitutionNode,
  type Point,
  type Resting,
} from "@/lib/network-graph";
import type { NetworkFlow } from "@/lib/types";

// The mesh, drawn. Institutions are nodes, EBICS connections are wires, and a
// file nobody has collected is a dot resting on one — which is what settling
// before releasing leaves behind, and the one state worth drawing differently.
//
// The topology is the lesson before any file moves: a bank reaches the clearing
// house and the settlement agent and never another bank, and a bank the scheme
// has not admitted is here with no wire at all. Holding a database is not the
// same as being reachable.

const HOST = { w: 52, h: 22 };
const BANK = { w: 44, h: 20 };

export function FlowGraph({
  flow,
  className,
  onSelectWire,
  selectedWire,
}: {
  flow: NetworkFlow;
  className?: string;
  // Clicking a wire narrows the list beneath the drawing to that wire's files.
  onSelectWire?: (key: string | null) => void;
  selectedWire?: string | null;
}) {
  const graph = useMemo(() => layout(flow), [flow]);
  const resting = useMemo(
    () => restingByWire(flow.crossings, flow.wires),
    [flow.crossings, flow.wires],
  );

  return (
    <svg
      viewBox={`0 0 ${CANVAS.width} ${CANVAS.height}`}
      className={cn("w-full", className)}
      role="img"
      aria-label="The institutions of this deployment and the files in flight between them"
    >
      {graph.edges.map((e) => (
        <Wire
          key={e.key}
          edge={e}
          resting={resting.get(e.key) ?? EMPTY}
          selected={selectedWire === e.key}
          onSelect={onSelectWire}
        />
      ))}
      {graph.nodes.map((n) => (
        <Pill key={n.bic} node={n} />
      ))}
    </svg>
  );
}

const EMPTY: Resting = { towardHost: [], towardSubscriber: [] };

function Wire({
  edge,
  resting,
  selected,
  onSelect,
}: {
  edge: Edge;
  resting: Resting;
  selected: boolean;
  onSelect?: (key: string | null) => void;
}) {
  const waiting = resting.towardHost.length + resting.towardSubscriber.length;
  const label = `${edge.subscriber} → ${edge.host}${
    waiting > 0 ? ` — ${waiting} waiting` : ""
  }`;
  return (
    <g
      className={onSelect ? "cursor-pointer" : undefined}
      onClick={onSelect ? () => onSelect(selected ? null : edge.key) : undefined}
    >
      <title>{label}</title>
      {/* An invisible fat stroke under the visible one: a 1px wire is not a
          click target, and widening the drawn line to be one would make the
          mesh look heavier than it is. */}
      {onSelect && <path d={edge.path} className="fill-none stroke-transparent" strokeWidth={12} />}
      <path
        d={edge.path}
        strokeWidth={selected ? 1.75 : 1}
        // The reserve conversation is drawn apart from the payment path,
        // because a wire to the settlement agent carries a bank's own money
        // and never a customer's.
        strokeDasharray={edge.toSettlementAgent ? "3 3" : undefined}
        className={cn(
          "fill-none transition-[stroke,stroke-width]",
          selected ? "stroke-foreground" : "stroke-border",
        )}
      />
      {resting.towardHost.length > 0 && (
        <Waiting at={edge.restAt.towardHost} count={resting.towardHost.length} />
      )}
      {resting.towardSubscriber.length > 0 && (
        <Waiting at={edge.restAt.towardSubscriber} count={resting.towardSubscriber.length} />
      )}
    </g>
  );
}

// A file resting on a wire, drawn at the end it is travelling towards. It
// appears when one institution puts a file where its recipient can reach it and
// goes when that recipient takes it, so the dots ARE the animation — nothing
// here is timed.
function Waiting({ at, count }: { at: Point; count: number }) {
  const many = count > 1;
  return (
    <g className="animate-in fade-in zoom-in duration-300">
      {/* A ring of the drawing's own background, so the dot sits ON the wire
          rather than being crossed by it. */}
      <circle cx={at.x} cy={at.y} r={many ? 9 : 6} className="fill-card" />
      <circle cx={at.x} cy={at.y} r={many ? 8 : 4.5} className="fill-primary" />
      {many && (
        <text
          x={at.x}
          y={at.y}
          textAnchor="middle"
          dominantBaseline="central"
          className="fill-primary-foreground text-[9px] font-semibold tabular-nums"
        >
          {count}
        </text>
      )}
    </g>
  );
}

// An institution. Hosts are wider and filled; a member bank is outlined,
// because what separates them here is that one is dialled and the other dials.
function Pill({ node }: { node: InstitutionNode }) {
  const host = node.role !== "member bank";
  const box = host ? HOST : BANK;
  return (
    <g>
      <title>{`${node.name} · ${node.bic} · ${node.role}`}</title>
      <rect
        x={node.x - box.w / 2}
        y={node.y - box.h / 2}
        width={box.w}
        height={box.h}
        rx={box.h / 2}
        className={cn("stroke-border", host ? "fill-muted" : "fill-card")}
        strokeWidth={1}
      />
      <text
        x={node.x}
        y={node.y}
        textAnchor="middle"
        dominantBaseline="central"
        className="fill-foreground text-[9px] font-medium"
      >
        {node.code}
      </text>
    </g>
  );
}
