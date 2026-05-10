import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MentionsView } from "./Mentions";
import { queueFetch } from "./test/fetch";

const mention = (over: Partial<{
  id: number;
  source: string;
  source_host: string;
  target: string;
  target_path: string;
  target_title: string;
  verified_at: string;
  received_at: string;
}> = {}) => ({
  id: over.id ?? 1,
  source: over.source ?? "https://alice.test/2026/05/09/re-bob",
  source_host: over.source_host ?? "alice.test",
  target: over.target ?? "https://example.test/2026/05/09/hello",
  target_path: over.target_path ?? "/2026/05/09/hello",
  target_title: over.target_title ?? "Hello from Bob",
  received_at: over.received_at ?? new Date().toISOString(),
  verified_at: over.verified_at ?? new Date().toISOString(),
});

describe("MentionsView", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("renders an empty-state when there are no mentions", async () => {
    queueFetch([{ status: 200, body: [] }]);
    render(<MentionsView onAuthLost={() => {}} />);
    expect(await screen.findByText(/No mentions yet\./i)).toBeInTheDocument();
  });

  it("renders the actor, the post title, and the source URL", async () => {
    queueFetch([{ status: 200, body: [mention()] }]);
    render(<MentionsView onAuthLost={() => {}} />);
    expect(await screen.findByText("alice.test")).toBeInTheDocument();
    expect(screen.getByText(/mentioned you/i)).toBeInTheDocument();
    // Post title links to the on-site target.
    const titleLink = screen.getByRole("link", { name: "Hello from Bob" });
    expect(titleLink).toHaveAttribute("href", "/2026/05/09/hello");
    // Source link points off-site.
    const sourceLink = screen.getByRole("link", { name: /alice\.test\/2026\/05\/09\/re-bob/ });
    expect(sourceLink).toHaveAttribute("href", "https://alice.test/2026/05/09/re-bob");
    expect(sourceLink).toHaveAttribute("target", "_blank");
  });

  it("falls back to the target path when the post title is missing (e.g. deleted post)", async () => {
    queueFetch([{ status: 200, body: [mention({ target_title: "" })] }]);
    render(<MentionsView onAuthLost={() => {}} />);
    expect(await screen.findByRole("link", { name: "/2026/05/09/hello" })).toBeInTheDocument();
  });

  it("calls onAuthLost when initial load returns 401", async () => {
    queueFetch([{ status: 401 }]);
    const onAuthLost = vi.fn();
    render(<MentionsView onAuthLost={onAuthLost} />);
    await waitFor(() => expect(onAuthLost).toHaveBeenCalled());
  });

  it("surfaces server error message", async () => {
    queueFetch([{ status: 500, body: "boom" }]);
    render(<MentionsView onAuthLost={() => {}} />);
    expect(await screen.findByText("boom")).toBeInTheDocument();
  });
});
