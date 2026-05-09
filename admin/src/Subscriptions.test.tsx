import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SubscriptionsView } from "./Subscriptions";
import { queueFetch } from "./test/fetch";

const sub = (over: Partial<{ id: number; url: string; title: string; category: string; last_error: string }> = {}) => ({
  id: over.id ?? 1,
  url: over.url ?? "https://a/feed",
  title: over.title ?? "A",
  category: over.category,
  last_error: over.last_error,
});

describe("SubscriptionsView", () => {
  beforeEach(() => {
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("renders an empty-state message when there are no subscriptions", async () => {
    queueFetch([{ status: 200, body: [] }]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    expect(await screen.findByText(/No subscriptions yet\./i)).toBeInTheDocument();
  });

  it("lists subscriptions including title, URL, and category", async () => {
    queueFetch([{ status: 200, body: [sub({ title: "Tech Blog", category: "tech" })] }]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    expect(await screen.findByText("Tech Blog")).toBeInTheDocument();
    expect(screen.getByText("https://a/feed")).toBeInTheDocument();
    expect(screen.getByText(/tech ·/)).toBeInTheDocument();
  });

  it("renders last_error in the row when present", async () => {
    queueFetch([{ status: 200, body: [sub({ last_error: "404 not found" })] }]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    expect(await screen.findByText(/404 not found/)).toBeInTheDocument();
  });

  it("subscribes via the form, clears inputs, and refetches the list", async () => {
    const fn = queueFetch([
      { status: 200, body: [] },
      { status: 201, body: { id: 7, url: "https://b/feed", title: "B" } },
      { status: 200, body: [sub({ id: 7, url: "https://b/feed", title: "B" })] },
    ]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    await screen.findByText(/No subscriptions yet\./);

    await userEvent.type(screen.getByPlaceholderText(/Feed URL/i), "https://b/feed");
    await userEvent.type(screen.getByPlaceholderText("Title (optional)"), "B");
    await userEvent.click(screen.getByRole("button", { name: "Subscribe" }));

    await waitFor(() => expect(screen.getByText("B")).toBeInTheDocument());

    // The POST went where we expected.
    expect(fn.mock.calls[1][0]).toBe("/admin/api/subscriptions");
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("POST");
    const body = JSON.parse((fn.mock.calls[1][1] as RequestInit).body as string);
    expect(body).toMatchObject({ url: "https://b/feed", title: "B" });

    // URL field cleared after success.
    expect(screen.getByPlaceholderText(/Feed URL/i)).toHaveValue("");
  });

  it("does not submit when URL is blank", async () => {
    const fn = queueFetch([{ status: 200, body: [] }]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    await screen.findByText(/No subscriptions yet\./);

    // Submit button is disabled when URL is empty — try to click and verify
    // no network call happened beyond the initial load.
    const btn = screen.getByRole("button", { name: "Subscribe" });
    expect(btn).toBeDisabled();
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("surfaces server errors from a failed Subscribe", async () => {
    queueFetch([
      { status: 200, body: [] },
      { status: 400, body: "invalid feed url" },
    ]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    await screen.findByText(/No subscriptions yet\./);

    await userEvent.type(screen.getByPlaceholderText(/Feed URL/i), "bogus");
    await userEvent.click(screen.getByRole("button", { name: "Subscribe" }));
    expect(await screen.findByText("invalid feed url")).toBeInTheDocument();
  });

  it("removes a subscription via the API and refetches", async () => {
    const fn = queueFetch([
      { status: 200, body: [sub()] },
      { status: 204 },
      { status: 200, body: [] },
    ]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    await screen.findByText("A");

    await userEvent.click(screen.getByRole("button", { name: "Unsubscribe" }));
    await waitFor(() => expect(screen.getByText(/No subscriptions yet\./)).toBeInTheDocument());

    expect(fn.mock.calls[1][0]).toBe("/admin/api/subscriptions?url=https%3A%2F%2Fa%2Ffeed");
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("DELETE");
  });

  it("aborts unsubscribe when confirm() returns false", async () => {
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(false));
    const fn = queueFetch([{ status: 200, body: [sub()] }]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    await screen.findByText("A");
    await userEvent.click(screen.getByRole("button", { name: "Unsubscribe" }));
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("calls onAuthLost when initial load returns 401", async () => {
    queueFetch([{ status: 401 }]);
    const onAuthLost = vi.fn();
    render(<SubscriptionsView onAuthLost={onAuthLost} />);
    await waitFor(() => expect(onAuthLost).toHaveBeenCalled());
  });
});
