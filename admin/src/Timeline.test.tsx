import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TimelineView } from "./Timeline";
import { queueFetch } from "./test/fetch";

const item = (over: Partial<{ id: number; title: string; read: boolean; content: string }> = {}) => ({
  id: over.id ?? 1,
  feed_id: 1,
  feed_title: "Test Feed",
  url: "https://example/post",
  title: over.title ?? "An item",
  content: over.content ?? "<p>body</p>",
  read: over.read ?? false,
});

describe("TimelineView", () => {
  it("renders an empty state when the timeline has no items", async () => {
    queueFetch([{ status: 200, body: { items: [] } }]);
    render(<TimelineView onAuthLost={() => {}} />);
    expect(await screen.findByText(/Nothing here yet/i)).toBeInTheDocument();
  });

  it("lists items, including feed title and item title", async () => {
    queueFetch([{ status: 200, body: { items: [item({ title: "Hello" })] } }]);
    render(<TimelineView onAuthLost={() => {}} />);
    expect(await screen.findByRole("heading", { name: "Hello" })).toBeInTheDocument();
    expect(screen.getByText("Test Feed")).toBeInTheDocument();
  });

  it("renders feed content as HTML (sanitized server-side)", async () => {
    queueFetch([{ status: 200, body: { items: [item({ content: "<p>safe <em>content</em></p>" })] } }]);
    const { container } = render(<TimelineView onAuthLost={() => {}} />);
    await screen.findByRole("heading", { name: "An item" });
    expect(container.querySelector(".feed-content em")?.textContent).toBe("content");
  });

  it("hides Load more when next_cursor is absent", async () => {
    queueFetch([{ status: 200, body: { items: [item()] } }]);
    render(<TimelineView onAuthLost={() => {}} />);
    await screen.findByRole("heading", { name: "An item" });
    expect(screen.queryByRole("button", { name: /Load more/i })).toBeNull();
  });

  it("paginates via Load more, appending without duplicating", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [item({ id: 1, title: "A" })], next_cursor: "100:1" } },
      { status: 200, body: { items: [item({ id: 2, title: "B" })] } },
    ]);
    render(<TimelineView onAuthLost={() => {}} />);
    await screen.findByRole("heading", { name: "A" });

    await userEvent.click(screen.getByRole("button", { name: /Load more/i }));
    await screen.findByRole("heading", { name: "B" });

    // Both still present (append, no replace).
    expect(screen.getByRole("heading", { name: "A" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "B" })).toBeInTheDocument();

    // Second URL carries the cursor.
    expect(fn.mock.calls[1][0]).toContain("cursor=100%3A1");
  });

  it("toggles unread filter and re-issues request with unread=1", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [item({ id: 1 })] } },
      { status: 200, body: { items: [item({ id: 1 })] } },
    ]);
    render(<TimelineView onAuthLost={() => {}} />);
    await screen.findByRole("heading", { name: "An item" });

    await userEvent.click(screen.getByRole("checkbox", { name: /Unread only/i }));
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect(fn.mock.calls[1][0]).toContain("unread=1");
    // First call had no unread param.
    expect(fn.mock.calls[0][0]).not.toContain("unread");
  });

  it("optimistically marks an item read and POSTs to the API", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [item({ id: 5, read: false })] } },
      { status: 204 },
    ]);
    render(<TimelineView onAuthLost={() => {}} />);
    await screen.findByRole("heading", { name: "An item" });

    await userEvent.click(screen.getByRole("button", { name: "Mark read" }));
    // Button flips immediately (optimistic).
    expect(await screen.findByRole("button", { name: "Mark unread" })).toBeInTheDocument();

    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect(fn.mock.calls[1][0]).toBe("/admin/api/items/5/read");
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("POST");
  });

  it("marks unread via DELETE", async () => {
    const fn = queueFetch([
      { status: 200, body: { items: [item({ id: 5, read: true })] } },
      { status: 204 },
    ]);
    render(<TimelineView onAuthLost={() => {}} />);
    await screen.findByRole("button", { name: "Mark unread" });
    await userEvent.click(screen.getByRole("button", { name: "Mark unread" }));
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect((fn.mock.calls[1][1] as RequestInit).method).toBe("DELETE");
  });

  it("reverts the optimistic update if the API call fails", async () => {
    queueFetch([
      { status: 200, body: { items: [item({ id: 5, read: false })] } },
      { status: 500, body: "boom" },
    ]);
    render(<TimelineView onAuthLost={() => {}} />);
    await screen.findByRole("heading", { name: "An item" });
    await userEvent.click(screen.getByRole("button", { name: "Mark read" }));
    // After failure, the button label should revert.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Mark read" })).toBeInTheDocument(),
    );
    expect(screen.getByText("boom")).toBeInTheDocument();
  });

  it("calls onAuthLost when initial load returns 401", async () => {
    queueFetch([{ status: 401 }]);
    const onAuthLost = vi.fn();
    render(<TimelineView onAuthLost={onAuthLost} />);
    await waitFor(() => expect(onAuthLost).toHaveBeenCalled());
  });
});
