import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SubscriptionsView } from "./Subscriptions";
import { queueFetch } from "./test/fetch";

const sub = (over: Partial<{ id: number; url: string; title: string; site_url: string; category: string; last_error: string; last_fetched_at: string }> = {}) => ({
  id: over.id ?? 1,
  url: over.url ?? "https://a.test/feed",
  title: over.title ?? "Alpha Feed",
  site_url: over.site_url,
  category: over.category,
  last_error: over.last_error,
  last_fetched_at: over.last_fetched_at,
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

  it("lists subscriptions with title, feed URL, and category", async () => {
    queueFetch([{ status: 200, body: [sub({ title: "Tech Blog", category: "tech" })] }]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    expect(await screen.findByText("Tech Blog")).toBeInTheDocument();
    expect(screen.getByText("https://a.test/feed")).toBeInTheDocument();
    expect(screen.getByText("tech")).toBeInTheDocument();
  });

  it("flags failing feeds with the error message", async () => {
    queueFetch([{ status: 200, body: [sub({ last_error: "404 not found" })] }]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    expect(await screen.findByText(/Failing · 404 not found/)).toBeInTheDocument();
  });

  it("optional title/category/site URL inputs are hidden until disclosed", async () => {
    queueFetch([{ status: 200, body: [] }]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    await screen.findByText(/No subscriptions yet\./);
    expect(screen.queryByPlaceholderText(/Title \(optional\)/i)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Add title, category, or site URL/i }));
    expect(screen.getByPlaceholderText(/Title \(optional\)/i)).toBeInTheDocument();
  });

  it("subscribes via the form, clears inputs, and refetches the list", async () => {
    const fn = queueFetch([
      { status: 200, body: [] },
      { status: 201, body: { id: 7, url: "https://b.test/feed", title: "B" } },
      { status: 200, body: [sub({ id: 7, url: "https://b.test/feed", title: "B" })] },
    ]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    await screen.findByText(/No subscriptions yet\./);

    await userEvent.type(screen.getByPlaceholderText(/Feed URL/i), "https://b.test/feed");
    await userEvent.click(screen.getByRole("button", { name: "Subscribe" }));

    await waitFor(() => expect(screen.getByText("B", { selector: ".font-medium" })).toBeInTheDocument());

    expect(fn.mock.calls[1][0]).toBe("/admin/api/subscriptions");
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("POST");
    const body = JSON.parse((fn.mock.calls[1][1] as RequestInit).body as string);
    expect(body).toMatchObject({ url: "https://b.test/feed" });
    expect(screen.getByPlaceholderText(/Feed URL/i)).toHaveValue("");
  });

  it("does not submit when URL is blank", async () => {
    const fn = queueFetch([{ status: 200, body: [] }]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    await screen.findByText(/No subscriptions yet\./);
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

  it("removes a subscription via the Manage menu", async () => {
    const fn = queueFetch([
      { status: 200, body: [sub()] },
      { status: 204 },
      { status: 200, body: [] },
    ]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    await screen.findByText("Alpha Feed");
    await userEvent.click(screen.getByRole("button", { name: /Manage/i }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /Unsubscribe/i }));
    await waitFor(() => expect(screen.getByText(/No subscriptions yet\./)).toBeInTheDocument());
    expect(fn.mock.calls[1][0]).toBe("/admin/api/subscriptions?url=https%3A%2F%2Fa.test%2Ffeed");
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("DELETE");
  });

  it("aborts unsubscribe when confirm() returns false", async () => {
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(false));
    const fn = queueFetch([{ status: 200, body: [sub()] }]);
    render(<SubscriptionsView onAuthLost={() => {}} />);
    await screen.findByText("Alpha Feed");
    await userEvent.click(screen.getByRole("button", { name: /Manage/i }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /Unsubscribe/i }));
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("calls onAuthLost when initial load returns 401", async () => {
    queueFetch([{ status: 401 }]);
    const onAuthLost = vi.fn();
    render(<SubscriptionsView onAuthLost={onAuthLost} />);
    await waitFor(() => expect(onAuthLost).toHaveBeenCalled());
  });
});
