import { describe, it, expect } from "vitest";
import { relativeTime } from "./relativeTime";

describe("relativeTime", () => {
  const now = Date.parse("2026-05-09T12:00:00Z");

  it("returns empty for missing input", () => {
    expect(relativeTime(undefined, now)).toBe("");
    expect(relativeTime("not a date", now)).toBe("");
  });

  it('uses "just now" for very recent timestamps', () => {
    expect(relativeTime("2026-05-09T11:59:50Z", now)).toBe("just now");
  });

  it("formats minutes, hours, and days", () => {
    expect(relativeTime("2026-05-09T11:55:00Z", now)).toBe("5m");
    expect(relativeTime("2026-05-09T09:00:00Z", now)).toBe("3h");
    expect(relativeTime("2026-05-07T12:00:00Z", now)).toBe("2d");
  });

  it("falls back to a date for items older than a week", () => {
    expect(relativeTime("2026-04-01T12:00:00Z", now)).toMatch(/[0-9]/);
  });
});
