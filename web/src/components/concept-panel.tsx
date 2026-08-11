"use client";

import { useSearchParams } from "next/navigation";
import { PanelRightClose } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useConceptPanel } from "./concept-panel-provider";
import { ConceptMarkdown } from "./concept-markdown";
import { parseConceptLinks } from "./concept-links";
import { hintContent, type HintKey } from "./hint-content";

// Resolves the concept to show: the URL's `concept` param if valid, else the
// page default. Returns null when neither is set.
function useActiveConcept(defaultConcept: HintKey | null): HintKey | null {
  const searchParams = useSearchParams();
  const fromUrl = searchParams.get("concept");
  if (fromUrl && fromUrl in hintContent) return fromUrl as HintKey;
  return defaultConcept;
}

// The panel body — shared by the desktop rail and the mobile sheet.
export function ConceptPanelBody({ onCollapse }: { onCollapse?: () => void }) {
  const { openConcept, defaultConcept } = useConceptPanel();
  const active = useActiveConcept(defaultConcept);

  if (!active) {
    return (
      // Named after what a reader can act on rather than after one control:
      // Learn has no “?” anywhere, so pointing at one described a way in that
      // does not exist on the surface where this panel is emptiest.
      <div className="p-4 text-sm text-muted-foreground">
        Explanations open here — from a “?” beside a field, a linked term, or a
        question you have just answered.
      </div>
    );
  }

  const entry = hintContent[active];
  const related = parseConceptLinks(entry.body).filter((k) => k !== active);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-2 border-b px-4 py-3">
        <h2 className="flex-1 text-sm font-semibold">{entry.title}</h2>
        {onCollapse && (
          <Button
            variant="ghost"
            size="icon"
            aria-label="Collapse concept panel"
            onClick={onCollapse}
          >
            <PanelRightClose className="size-4" />
          </Button>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <ConceptMarkdown body={entry.body} />
        {related.length > 0 && (
          <div className="mt-4 border-t pt-3">
            <p className="mb-2 text-xs text-muted-foreground">Related</p>
            <div className="flex flex-wrap gap-2">
              {related.map((key) => (
                <button
                  key={key}
                  type="button"
                  onClick={() => openConcept(key)}
                  className="rounded-full border px-2.5 py-0.5 text-xs text-muted-foreground transition-colors hover:border-foreground/30 hover:text-foreground"
                >
                  {hintContent[key].title}
                </button>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
