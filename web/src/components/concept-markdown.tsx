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
export function ConceptMarkdown({ body }: { body: string }) {
  const { openConcept } = useConceptPanel();
  const source = preprocessConceptMarkdown(body);

  return (
    <div
      className={cn(
        "text-sm leading-relaxed text-muted-foreground",
        "[&_p]:mb-3 [&_strong]:font-medium [&_strong]:text-foreground",
        "[&_ul]:mb-3 [&_ul]:list-disc [&_ul]:pl-5 [&_li]:mb-1",
        "[&_h3]:mb-1.5 [&_h3]:mt-4 [&_h3]:text-sm [&_h3]:font-semibold [&_h3]:text-foreground",
        "[&_pre]:my-3 [&_pre]:overflow-x-auto [&_pre]:rounded-md [&_pre]:bg-muted [&_pre]:p-3 [&_pre]:font-mono [&_pre]:text-xs [&_pre]:text-foreground",
        "[&_code]:rounded [&_code]:bg-muted [&_code]:px-1 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-xs",
        "[&_pre_code]:bg-transparent [&_pre_code]:p-0",
      )}
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        urlTransform={conceptUrlTransform}
        components={{
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
