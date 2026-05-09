import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StreamView } from "./Stream";
import { queueFetch } from "./test/fetch";

const item = (
  over: Partial<{ id: number; title: string; read: boolean; content: string; url: string }> = {},
) => ({
  id: over.id ?? 1,
  feed_id: 1,
  feed_title: "Test Feed",
  url: over.url ?? "https://example/post",
  title: over.title ?? "An item",
  content: over.content ?? "<p>body</p>",
  read: over.read ?? false,
});

beforeEach(() => {
  // IntersectionObserver isn't implemented in jsdom; the auto-mark-read hook
  // gates on it being present. A no-op shim keeps the hook silent in tests.
  class IO {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [] as IntersectionObserverEntry[];
    }
    root: Element | null = null;
    rootMargin = "";
    thresholds: number[] = [];
  }
  vi.stubGlobal("IntersectionObserver", IO);
});
afterEach(() => vi.unstubAllGlobals());

async function openCardMenu(article: HTMLElement) {
  await userEvent.click(within(article).getByRole("button", { name: /more/i }));
}

describe("StreamView", () => {
  it("renders an empty state when the timeline has no items", async () => {
    queueFetch([{ status: 200, body: { items: [] } }]);
    render(<StreamView onAuthLost={() => {}} />);
    expect(await screen.findByText(/Nothing here yet/i)).toBeInTheDocument();
  });

  it("renders the feed title, item title, and lead paragraph", async () => {
    queueFetch([
      {
        status: 200,
        body: { items: [item({ content: "<p>safe <em>content</em></p><p>more</p>" })] },
      },
    ]);
    const { container } = render(<StreamView onAuthLost={() => {}} />);
    expect(await screen.findByRole("heading", { name: "An item" })).toBeInTheDocument();
    expect(screen.getByText("Test Feed")).toBeInTheDocument();
    // Only the first paragraph renders until expanded.
    expect(container.querySelector(".post-rendered em")?.textContent).toBe("content");
    expect(screen.queryByText("more")).toBeNull();
  });

  it("expands long items via Read more", async () => {
    queueFetch([
      {
        status: 200,
        body: { items: [item({ content: "<p>lead</p><p>second paragraph</p>" })] },
      },
    ]);
    render(<StreamView onAuthLost={() => {}} />);
    await screen.findByRole("heading", { name: "An item" });
    expect(screen.queryByText("second paragraph")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /Read more/i }));
    expect(await screen.findByText("second paragraph")).toBeInTheDocument();
  });

  it("does not show Read more for short items", async () => {
    queueFetch([{ status: 200, body: { items: [item({ content: "<p>only one</p>" })] } }]);
    render(<StreamView onAuthLost={() => {}} />);
    await screen.findByRole("heading", { name: "An item" });
    expect(screen.queryByRole("button", { name: /Read more/i })).toBeNull();
  });

  it("paginates via Load more", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [item({ id: 1, title: "A" })], next_cursor: "100:1" } },
      { status: 200, body: { items: [item({ id: 2, title: "B" })] } },
    ]);
    render(<StreamView onAuthLost={() => {}} />);
    await screen.findByRole("heading", { name: "A" });

    await userEvent.click(screen.getByRole("button", { name: /Load more/i }));
    await screen.findByRole("heading", { name: "B" });

    expect(screen.getByRole("heading", { name: "A" })).toBeInTheDocument();
    expect(fn.mock.calls[1][0]).toContain("cursor=100%3A1");
  });

  it("toggles the Unread filter via the pill bar", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [item({ id: 1 })] } },
      { status: 200, body: { items: [item({ id: 1 })] } },
    ]);
    render(<StreamView onAuthLost={() => {}} />);
    await screen.findByRole("heading", { name: "An item" });

    await userEvent.click(screen.getByRole("tab", { name: "Unread" }));
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect(fn.mock.calls[1][0]).toContain("unread=1");
    expect(fn.mock.calls[0][0]).not.toContain("unread");
  });

  it("marks an item read via the per-card menu, optimistically", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [item({ id: 5, read: false })] } },
      { status: 204 },
    ]);
    render(<StreamView onAuthLost={() => {}} />);
    const article = (await screen.findByRole("heading", { name: "An item" })).closest("article")!;

    await openCardMenu(article);
    await userEvent.click(await screen.findByRole("menuitem", { name: "Mark read" }));

    // After the action, the menu should now offer Mark unread (optimistic flip).
    await openCardMenu(article);
    expect(await screen.findByRole("menuitem", { name: "Mark unread" })).toBeInTheDocument();

    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect(fn.mock.calls[1][0]).toBe("/admin/api/items/5/read");
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("POST");
  });

  it("marks unread via DELETE when the item starts read", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [item({ id: 5, read: true })] } },
      { status: 204 },
    ]);
    render(<StreamView onAuthLost={() => {}} />);
    const article = (await screen.findByRole("heading", { name: "An item" })).closest("article")!;
    await openCardMenu(article);
    await userEvent.click(await screen.findByRole("menuitem", { name: "Mark unread" }));
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("DELETE");
  });

  it("reverts and surfaces an error when the read mutation fails", async () => {
    queueFetch([
      { status: 200, body: { items: [item({ id: 5, read: false })] } },
      { status: 500, body: "boom" },
    ]);
    render(<StreamView onAuthLost={() => {}} />);
    const article = (await screen.findByRole("heading", { name: "An item" })).closest("article")!;
    await openCardMenu(article);
    await userEvent.click(await screen.findByRole("menuitem", { name: "Mark read" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    // Menu reverts back to "Mark read" since the mutation rolled back.
    await openCardMenu(article);
    expect(await screen.findByRole("menuitem", { name: "Mark read" })).toBeInTheDocument();
  });

  it("calls onAuthLost when initial load returns 401", async () => {
    queueFetch([{ status: 401 }]);
    const onAuthLost = vi.fn();
    render(<StreamView onAuthLost={onAuthLost} />);
    await waitFor(() => expect(onAuthLost).toHaveBeenCalled());
  });
});
