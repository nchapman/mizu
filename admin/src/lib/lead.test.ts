import { describe, it, expect } from "vitest";
import { extractLead } from "./lead";

describe("extractLead", () => {
  it("returns empty lead for empty input", () => {
    expect(extractLead("")).toEqual({ leadHTML: "", hasMore: false });
  });

  it("returns the input as the lead when there is only one paragraph", () => {
    const out = extractLead("<p>just one</p>");
    expect(out.leadHTML).toBe("<p>just one</p>");
    expect(out.hasMore).toBe(false);
  });

  it("uses the first paragraph as the lead and reports hasMore", () => {
    const out = extractLead("<p>first</p><p>second</p><p>third</p>");
    expect(out.leadHTML).toBe("<p>first</p>");
    expect(out.hasMore).toBe(true);
  });

  it("skips empty wrapper elements when picking the lead", () => {
    const out = extractLead('<div></div><p>real lead</p><p>more</p>');
    expect(out.leadHTML).toBe("<p>real lead</p>");
    expect(out.hasMore).toBe(true);
  });

  it("treats a leading <img> wrapper as the lead", () => {
    const out = extractLead('<figure><img src="x"></figure><p>caption follows</p>');
    expect(out.leadHTML).toBe('<figure><img src="x"></figure>');
    expect(out.hasMore).toBe(true);
  });

  it("returns the whole HTML when there is no block structure", () => {
    const out = extractLead("just text");
    expect(out.leadHTML).toBe("just text");
    expect(out.hasMore).toBe(false);
  });

  it("does not flag hasMore when trailing children are all decorative whitespace", () => {
    const out = extractLead("<p>lead</p><div></div>");
    expect(out.leadHTML).toBe("<p>lead</p>");
    expect(out.hasMore).toBe(false);
  });
});
