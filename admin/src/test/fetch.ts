import { vi, afterEach } from "vitest";

export type Reply = { status: number; body?: unknown };

// queueFetch installs a fetch stub that returns the queued replies in
// order. Each call shifts one off; calls past the queue throw so tests
// fail loudly rather than hanging.
//
// 204 responses use `null` for the body — the Response constructor
// rejects empty-string bodies on 204 per the Fetch spec.
//
// Cleanup is owned here: each call registers its own afterEach to
// unstub all globals. Callers don't need their own afterEach for the
// fetch stub — they only need one if they're stubbing other globals
// (e.g. `confirm`) directly.
export function queueFetch(replies: Reply[]) {
  const fn = vi.fn().mockImplementation(() => {
    const r = replies.shift();
    if (!r) throw new Error("fetch called more times than staged");
    const body = r.status === 204
      ? null
      : r.body === undefined ? "" : typeof r.body === "string" ? r.body : JSON.stringify(r.body);
    return Promise.resolve(new Response(body, { status: r.status }));
  });
  vi.stubGlobal("fetch", fn);
  afterEach(() => vi.unstubAllGlobals());
  return fn;
}
