// extractLead splits server-sanitized HTML into a lead and a "rest" flag for
// progressive disclosure. The lead is the first block-level element with
// visible content (typically the first <p>); hasMore signals there is more
// content the caller can reveal on demand.
//
// The HTML must already be sanitized — extractLead trusts its input. We rely
// on bluemonday at ingest (see CLAUDE.md) and on goldmark for own posts.
//
// We intentionally don't reimplement any sanitization here; we just walk the
// parsed DOM and slice it. The resulting lead is itself a valid HTML fragment
// that can go straight into dangerouslySetInnerHTML.

export interface Lead {
  leadHTML: string;
  hasMore: boolean;
}

export function extractLead(html: string): Lead {
  if (!html) return { leadHTML: "", hasMore: false };

  // Wrap in a known root so we can iterate the parsed children directly.
  const doc = new DOMParser().parseFromString(`<div id="__lead__">${html}</div>`, "text/html");
  const root = doc.getElementById("__lead__");
  if (!root || root.childElementCount === 0) {
    // No block structure (e.g. plain text or inline-only). Treat as a single lead.
    return { leadHTML: html, hasMore: false };
  }

  const children = Array.from(root.children);
  const firstWithContent = children.findIndex((el) => hasMeaningfulContent(el));
  if (firstWithContent < 0) {
    // All children are empty/decorative. Render the whole thing as lead.
    return { leadHTML: html, hasMore: false };
  }

  const leadEl = children[firstWithContent];
  const trailing = children.slice(firstWithContent + 1).some(hasMeaningfulContent);
  return { leadHTML: leadEl.outerHTML, hasMore: trailing };
}

function hasMeaningfulContent(el: Element): boolean {
  if (el.textContent && el.textContent.trim().length > 0) return true;
  // Bare media (image, figure, embed) without text still counts.
  if (el.querySelector("img, video, iframe, picture, audio")) return true;
  return false;
}
