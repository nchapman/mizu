import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StreamView } from "./Stream";
import { queueFetch } from "./test/fetch";

const feed = (
  over: Partial<{ id: number; title: string; read: boolean; content: string; url: string }> = {},
) => ({
  kind: "feed" as const,
  item: {
    id: over.id ?? 1,
    feed_id: 1,
    feed_title: "Test Feed",
    url: over.url ?? "https://example/post",
    title: over.title ?? "An item",
    content: over.content ?? "<p>body</p>",
    read: over.read ?? false,
  },
});

const own = (
  over: Partial<{ id: string; title: string; html: string; body: string; date: string; path: string }> = {},
) => ({
  kind: "own" as const,
  post: {
    id: over.id ?? "p1",
    title: over.title ?? "My Post",
    body: over.body ?? "the body",
    html: over.html ?? "<p>the body</p>",
    date: over.date ?? "2026-05-09T00:00:00Z",
    path: over.path ?? "/2026/05/09/my-post",
  },
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
  vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
});
afterEach(() => vi.unstubAllGlobals());

async function openCardMenu(article: HTMLElement) {
  await userEvent.click(within(article).getByRole("button", { name: /more/i }));
}

const noop = () => {};

describe("StreamView", () => {
  it("renders an empty state when the stream has no items", async () => {
    queueFetch([{ status: 200, body: { items: [] } }]);
    render(<StreamView onAuthLost={noop} onEditOwn={noop} onReply={noop} />);
    expect(await screen.findByText(/Your stream is empty/i)).toBeInTheDocument();
  });

  it("renders a feed card with byline, title, and lead paragraph", async () => {
    queueFetch([
      { status: 200, body: { items: [feed({ content: "<p>safe <em>content</em></p><p>more</p>" })] } },
    ]);
    const { container } = render(<StreamView onAuthLost={noop} onEditOwn={noop} onReply={noop} />);
    expect(await screen.findByRole("heading", { name: "An item" })).toBeInTheDocument();
    expect(screen.getByText("Test Feed")).toBeInTheDocument();
    expect(container.querySelector(".post-rendered em")?.textContent).toBe("content");
    expect(screen.queryByText("more")).toBeNull();
  });

  it("renders an own-post card with You byline, title, and rendered HTML", async () => {
    queueFetch([
      { status: 200, body: { items: [own({ title: "Hello", html: "<p>Hello <em>world</em></p>" })] } },
    ]);
    const { container } = render(<StreamView onAuthLost={noop} onEditOwn={noop} onReply={noop} />);
    expect(await screen.findByRole("heading", { name: "Hello" })).toBeInTheDocument();
    expect(screen.getByText("You")).toBeInTheDocument();
    expect(container.querySelector(".post-rendered em")?.textContent).toBe("world");
  });

  it("expands long items via Read more", async () => {
    queueFetch([
      { status: 200, body: { items: [feed({ content: "<p>lead</p><p>second paragraph</p>" })] } },
    ]);
    render(<StreamView onAuthLost={noop} onEditOwn={noop} onReply={noop} />);
    await screen.findByRole("heading", { name: "An item" });
    expect(screen.queryByText("second paragraph")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /Read more/i }));
    expect(await screen.findByText("second paragraph")).toBeInTheDocument();
  });

  it("paginates via Load more, passing cursor", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [feed({ id: 1, title: "A" })], next_cursor: "abc" } },
      { status: 200, body: { items: [feed({ id: 2, title: "B" })] } },
    ]);
    render(<StreamView onAuthLost={noop} onEditOwn={noop} onReply={noop} />);
    await screen.findByRole("heading", { name: "A" });
    await userEvent.click(screen.getByRole("button", { name: /Load more/i }));
    await screen.findByRole("heading", { name: "B" });
    expect(fn.mock.calls[1][0]).toContain("cursor=abc");
  });

  it("toggles filter pills and re-issues the request with filter=unread", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [feed({ id: 1 })] } },
      { status: 200, body: { items: [feed({ id: 1 })] } },
    ]);
    render(<StreamView onAuthLost={noop} onEditOwn={noop} onReply={noop} />);
    await screen.findByRole("heading", { name: "An item" });

    await userEvent.click(screen.getByRole("tab", { name: "Unread" }));
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect(fn.mock.calls[1][0]).toContain("filter=unread");
    expect(fn.mock.calls[0][0]).not.toContain("filter=");
  });

  it("filter Yours sends filter=yours", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [feed({ id: 1 })] } },
      { status: 200, body: { items: [own()] } },
    ]);
    render(<StreamView onAuthLost={noop} onEditOwn={noop} onReply={noop} />);
    await screen.findByRole("heading", { name: "An item" });
    await userEvent.click(screen.getByRole("tab", { name: "Yours" }));
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect(fn.mock.calls[1][0]).toContain("filter=yours");
  });

  it("marks a feed item read via the per-card menu, optimistically", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [feed({ id: 5, read: false })] } },
      { status: 204 },
    ]);
    render(<StreamView onAuthLost={noop} onEditOwn={noop} onReply={noop} />);
    const article = (await screen.findByRole("heading", { name: "An item" })).closest("article")!;
    await openCardMenu(article);
    await userEvent.click(await screen.findByRole("menuitem", { name: "Mark read" }));
    await openCardMenu(article);
    expect(await screen.findByRole("menuitem", { name: "Mark unread" })).toBeInTheDocument();
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect(fn.mock.calls[1][0]).toBe("/admin/api/items/5/read");
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("POST");
  });

  it("invokes onReply from the feed-card menu with the source item", async () => {
    queueFetch([{ status: 200, body: { items: [feed({ id: 7, title: "Source" })] } }]);
    const onReply = vi.fn();
    render(<StreamView onAuthLost={noop} onEditOwn={noop} onReply={onReply} />);
    const article = (await screen.findByRole("heading", { name: "Source" })).closest("article")!;
    await openCardMenu(article);
    await userEvent.click(await screen.findByRole("menuitem", { name: /Reply with a post/i }));
    expect(onReply).toHaveBeenCalledTimes(1);
    expect(onReply.mock.calls[0][0].id).toBe(7);
  });

  it("hands off own-card Edit to onEditOwn", async () => {
    queueFetch([{ status: 200, body: { items: [own({ id: "p1", title: "Hello" })] } }]);
    const onEditOwn = vi.fn();
    render(<StreamView onAuthLost={noop} onEditOwn={onEditOwn} onReply={noop} />);
    const article = (await screen.findByRole("heading", { name: "Hello" })).closest("article")!;
    await openCardMenu(article);
    await userEvent.click(await screen.findByRole("menuitem", { name: /Edit/i }));
    expect(onEditOwn).toHaveBeenCalledTimes(1);
    expect(onEditOwn.mock.calls[0][0].id).toBe("p1");
  });

  it("deletes own posts via the menu and removes them from the list", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [own({ id: "p1", title: "Doomed" })] } },
      { status: 204 }, // DELETE
    ]);
    render(<StreamView onAuthLost={noop} onEditOwn={noop} onReply={noop} />);
    const article = (await screen.findByRole("heading", { name: "Doomed" })).closest("article")!;
    await openCardMenu(article);
    await userEvent.click(await screen.findByRole("menuitem", { name: /Delete/i }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Doomed" })).toBeNull());
    expect(fn.mock.calls[1][0]).toBe("/admin/api/posts/p1");
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("DELETE");
  });

  it("calls onAuthLost when initial load returns 401", async () => {
    queueFetch([{ status: 401 }]);
    const onAuthLost = vi.fn();
    render(<StreamView onAuthLost={onAuthLost} onEditOwn={noop} onReply={noop} />);
    await waitFor(() => expect(onAuthLost).toHaveBeenCalled());
  });
});
