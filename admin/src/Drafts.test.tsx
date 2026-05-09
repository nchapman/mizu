import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftsView } from "./Drafts";
import { queueFetch } from "./test/fetch";

const sample = (over: Partial<{ id: string; title: string; body: string; html: string }> = {}) => ({
  id: over.id ?? "d1",
  title: over.title ?? "Draft One",
  body: over.body ?? "raw markdown",
  html: over.html ?? "<p>raw markdown</p>",
  created: "2026-05-09T00:00:00Z",
});

describe("DraftsView", () => {
  beforeEach(() => {
    // confirm() defaults to true so the destructive flows proceed.
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("renders an empty-state message when there are no drafts", async () => {
    queueFetch([{ status: 200, body: [] }]);
    render(<DraftsView onAuthLost={() => {}} onEdit={() => {}} />);
    expect(await screen.findByText(/No drafts\./i)).toBeInTheDocument();
  });

  it("lists drafts and renders the server-sanitized HTML", async () => {
    queueFetch([
      { status: 200, body: [sample({ html: "<p>hello <em>world</em></p>" })] },
    ]);
    const { container } = render(<DraftsView onAuthLost={() => {}} onEdit={() => {}} />);
    expect(await screen.findByRole("heading", { name: "Draft One" })).toBeInTheDocument();
    // dangerouslySetInnerHTML preserves the inline tag.
    expect(container.querySelector(".post-rendered em")).not.toBeNull();
    expect(container.querySelector(".post-rendered em")?.textContent).toBe("world");
  });

  it("calls onEdit with the clicked draft", async () => {
    queueFetch([{ status: 200, body: [sample()] }]);
    const onEdit = vi.fn();
    render(<DraftsView onAuthLost={() => {}} onEdit={onEdit} />);
    await screen.findByRole("heading", { name: "Draft One" });
    await userEvent.click(screen.getByRole("button", { name: "edit" }));
    expect(onEdit).toHaveBeenCalledTimes(1);
    expect(onEdit.mock.calls[0][0].id).toBe("d1");
  });

  it("deletes a draft and refetches the list", async () => {
    const fn = queueFetch([
      { status: 200, body: [sample()] },          // initial load
      { status: 204 },                             // DELETE
      { status: 200, body: [] },                   // reload
    ]);
    render(<DraftsView onAuthLost={() => {}} onEdit={() => {}} />);
    await screen.findByRole("heading", { name: "Draft One" });

    await userEvent.click(screen.getByRole("button", { name: "delete" }));
    await waitFor(() => expect(screen.getByText(/No drafts\./)).toBeInTheDocument());

    expect(fn.mock.calls[1][0]).toBe("/admin/api/drafts/d1");
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("DELETE");
  });

  it("publishes a draft and refetches the list", async () => {
    const fn = queueFetch([
      { status: 200, body: [sample()] },
      { status: 200, body: { id: "p1", date: "2026-05-09T00:00:00Z", body: "x", html: "<p>x</p>", path: "/p1" } },
      { status: 200, body: [] },
    ]);
    render(<DraftsView onAuthLost={() => {}} onEdit={() => {}} />);
    await screen.findByRole("heading", { name: "Draft One" });

    await userEvent.click(screen.getByRole("button", { name: "publish" }));
    await waitFor(() => expect(screen.getByText(/No drafts\./)).toBeInTheDocument());

    expect(fn.mock.calls[1][0]).toBe("/admin/api/drafts/d1/publish");
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("POST");
  });

  it("aborts destructive actions when confirm() returns false", async () => {
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(false));
    const fn = queueFetch([{ status: 200, body: [sample()] }]);
    render(<DraftsView onAuthLost={() => {}} onEdit={() => {}} />);
    await screen.findByRole("heading", { name: "Draft One" });
    await userEvent.click(screen.getByRole("button", { name: "delete" }));
    // Only the initial GET, no DELETE.
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("calls onAuthLost when initial load returns 401", async () => {
    queueFetch([{ status: 401 }]);
    const onAuthLost = vi.fn();
    render(<DraftsView onAuthLost={onAuthLost} onEdit={() => {}} />);
    await waitFor(() => expect(onAuthLost).toHaveBeenCalled());
  });

  it("shows error text after a failed publish", async () => {
    queueFetch([
      { status: 200, body: [sample()] },
      { status: 500, body: "publish exploded" },
    ]);
    render(<DraftsView onAuthLost={() => {}} onEdit={() => {}} />);
    await screen.findByRole("heading", { name: "Draft One" });
    await userEvent.click(screen.getByRole("button", { name: "publish" }));
    expect(await screen.findByText("publish exploded")).toBeInTheDocument();
  });
});
