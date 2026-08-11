"use client";

import Link from "next/link";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

import { cn } from "@/lib/utils";
import { useConceptPanel } from "./concept-panel-provider";
import { conceptUrlTransform, preprocessConceptMarkdown } from "./concept-links";
import { hintContent, type HintKey } from "./hint-content";

// Renders a concept body as markdown. `concept:` links swap the panel; internal
// paths use next/link; everything else opens in a new tab.
export function ConceptMarkdown({ body, className }: { body: string; className?: string }) {
  const { openConcept } = useConceptPanel();
  const source = preprocessConceptMarkdown(body);

  return (
    <div
      className={cn(
        "text-sm leading-relaxed text-muted-foreground",
        "[&_p]:mb-3 [&_strong]:font-medium [&_strong]:text-foreground",
        "[&_ul]:mb-3 [&_ul]:list-disc [&_ul]:pl-5 [&_li]:mb-1",
        "[&_ol]:mb-3 [&_ol]:list-decimal [&_ol]:pl-5",
        "[&_table]:my-3 [&_table]:w-full [&_table]:border-collapse [&_table]:text-xs",
        "[&_th]:border-b [&_th]:border-border [&_th]:px-2 [&_th]:py-1 [&_th]:text-left [&_th]:font-medium [&_th]:text-foreground",
        "[&_td]:border-b [&_td]:border-border/50 [&_td]:px-2 [&_td]:py-1 [&_td]:align-top",
        "[&_tbody_tr:last-child_td]:border-0",
        "[&_h3]:mb-1.5 [&_h3]:mt-4 [&_h3]:text-sm [&_h3]:font-semibold [&_h3]:text-foreground",
        // `pre` is a component override below, which owns its own box.
        "[&_code]:rounded [&_code]:bg-muted [&_code]:px-1 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-xs",
        "[&_pre_code]:bg-transparent [&_pre_code]:p-0",
        className,
      )}
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        urlTransform={conceptUrlTransform}
        components={{
          // The T-account and payment-lifecycle diagrams are wider than a phone,
          // and `overflow-x-auto` alone gave no sign of it: the line simply
          // stopped mid-word. The fade says there is more to the right, and
          // `tabIndex` is what makes a scrollable region reachable without a
          // pointer — a keyboard user could not scroll it at all otherwise.
          pre({ children }) {
            return (
              <div className="relative my-3">
                <pre
                  tabIndex={0}
                  role="region"
                  aria-label="Diagram, scrolls sideways"
                  className="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs text-foreground focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
                >
                  {children}
                </pre>
                <span
                  aria-hidden="true"
                  className="pointer-events-none absolute inset-y-0 right-0 w-6 rounded-r-md bg-linear-to-l from-muted to-transparent"
                />
              </div>
            );
          },
          a({ href, children }) {
            if (href?.startsWith("concept:")) {
              const key = href.slice("concept:".length);
              if (key in hintContent) {
                return (
                  <button
                    type="button"
                    onClick={() => openConcept(key as HintKey)}
                    className="text-primary underline decoration-dotted underline-offset-2 hover:decoration-solid"
                  >
                    {children}
                  </button>
                );
              }
              return <span>{children}</span>;
            }
            if (href?.startsWith("/")) {
              return (
                <Link href={href} className="text-primary underline underline-offset-2">
                  {children}
                </Link>
              );
            }
            return (
              <a
                href={href}
                target="_blank"
                rel="noreferrer"
                className="text-primary underline underline-offset-2"
              >
                {children}
              </a>
            );
          },
        }}
      >
        {source}
      </ReactMarkdown>
    </div>
  );
}
