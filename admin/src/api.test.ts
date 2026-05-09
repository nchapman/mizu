import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, Unauthorized, uploadMedia } from "./api";

// fetchMock captures the (url, init) call and returns whatever the test
// pre-staged as the next response.
type Staged = { status: number; body?: string; bodyJSON?: unknown };

function stageResponse(staged: Staged): Response {
  const body = staged.bodyJSON !== undefined
    ? JSON.stringify(staged.bodyJSON)
    : (staged.body ?? "");
  return new Response(body, {
    status: staged.status,
    headers: { "content-type": "application/json" },
  });
}

describe("api", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("parses 200 JSON", async () => {
    fetchMock.mockResolvedValueOnce(stageResponse({ status: 200, bodyJSON: { ok: true } }));
    const got = await api<{ ok: boolean }>("/x");
    expect(got).toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledWith("/x", expect.objectContaining({ headers: {} }));
  });

  it("returns undefined on 204", async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    const got = await api("/x");
    expect(got).toBeUndefined();
  });

  it("throws Unauthorized on 401", async () => {
    fetchMock.mockResolvedValueOnce(new Response("nope", { status: 401 }));
    await expect(api("/x")).rejects.toBeInstanceOf(Unauthorized);
  });

  it("throws Error with response body on non-2xx", async () => {
    fetchMock.mockResolvedValueOnce(new Response("bad request: missing field", { status: 400 }));
    await expect(api("/x")).rejects.toThrow("bad request: missing field");
  });

  it("falls back to HTTP <code> when body is empty on non-2xx", async () => {
    fetchMock.mockResolvedValueOnce(new Response("", { status: 500 }));
    await expect(api("/x")).rejects.toThrow("HTTP 500");
  });

  it("adds JSON content-type for string bodies", async () => {
    fetchMock.mockResolvedValueOnce(stageResponse({ status: 200, bodyJSON: {} }));
    await api("/x", { method: "POST", body: JSON.stringify({ a: 1 }) });
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.headers).toMatchObject({ "content-type": "application/json" });
  });

  it("does NOT add content-type for FormData bodies", async () => {
    fetchMock.mockResolvedValueOnce(stageResponse({ status: 200, bodyJSON: {} }));
    const fd = new FormData();
    fd.append("file", new Blob(["x"]), "x.png");
    await api("/x", { method: "POST", body: fd });
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.headers).not.toHaveProperty("content-type");
  });

  it("preserves caller-supplied headers and lets them override defaults", async () => {
    fetchMock.mockResolvedValueOnce(stageResponse({ status: 200, bodyJSON: {} }));
    await api("/x", {
      method: "POST",
      body: JSON.stringify({}),
      headers: { "x-custom": "1", "content-type": "application/x-overridden" },
    });
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.headers).toMatchObject({
      "x-custom": "1",
      "content-type": "application/x-overridden",
    });
  });
});

describe("uploadMedia", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(
      new Response(JSON.stringify({ name: "a.png", url: "/m/a.png", size: 1, mime: "image/png" }), { status: 201 }),
    ));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("posts a multipart form with the file field set", async () => {
    const file = new File(["x"], "a.png", { type: "image/png" });
    const out = await uploadMedia(file);
    expect(out.name).toBe("a.png");
    const fetchMock = (globalThis.fetch as unknown as ReturnType<typeof vi.fn>);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/admin/api/media");
    expect(init.method).toBe("POST");
    expect(init.body).toBeInstanceOf(FormData);
    const fd = init.body as FormData;
    expect((fd.get("file") as File).name).toBe("a.png");
  });
});
