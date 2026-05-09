import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "./App";
import { queueFetch } from "./test/fetch";

beforeEach(() => {
  // Many flows go through window.confirm; default to true so destructive
  // tests proceed without prompting.
  vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
  // Reset the URL hash between tests so route state from one test doesn't
  // leak into the next (the Shell reads window.location.hash on mount).
  window.location.hash = "";
});
afterEach(() => vi.unstubAllGlobals());

async function openMenu() {
  await userEvent.click(screen.getByRole("button", { name: /menu/i }));
}

// withMe shortcuts the app's initial GET /admin/api/me. Anything passed
// after is the rest of the staged response queue.
function withMe(me: { configured: boolean; authenticated: boolean }, ...rest: { status: number; body?: unknown }[]) {
  return queueFetch([{ status: 200, body: me }, ...rest]);
}

describe("App auth screens", () => {
  it("renders the Setup screen when /me reports unconfigured", async () => {
    withMe({ configured: false, authenticated: false });
    render(<App />);
    expect(await screen.findByRole("heading", { name: /Welcome to repeat/i })).toBeInTheDocument();
    expect(screen.getByLabelText(/Setup token/i)).toBeInTheDocument();
  });

  it("renders the Login screen when configured but not authenticated", async () => {
    withMe({ configured: true, authenticated: false });
    render(<App />);
    expect(await screen.findByRole("heading", { name: /Sign in/i })).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Password")).toBeInTheDocument();
  });

  it("renders the Shell when authenticated", async () => {
    withMe(
      { configured: true, authenticated: true },
      { status: 200, body: [] }, // /admin/api/posts initial load
    );
    render(<App />);
    // The brand button anchors the new top bar.
    expect(await screen.findByRole("button", { name: "repeat" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /menu/i })).toBeInTheDocument();
  });

  it("shows a retry banner when /me fails", async () => {
    queueFetch([{ status: 500 }, { status: 200, body: { configured: false, authenticated: false } }]);
    render(<App />);
    expect(await screen.findByText(/Could not reach the server/i)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Retry/i }));
    // After retry, the configured-false state means the Setup screen renders.
    expect(await screen.findByRole("heading", { name: /Welcome to repeat/i })).toBeInTheDocument();
  });
});

describe("App route navigation", () => {
  it("loads the Drafts view from the overflow menu", async () => {
    withMe(
      { configured: true, authenticated: true },
      { status: 200, body: [] }, // posts
      { status: 200, body: [] }, // drafts
    );
    render(<App />);
    await screen.findByRole("button", { name: "repeat" });
    await openMenu();
    await userEvent.click(await screen.findByRole("menuitem", { name: /Drafts/i }));
    expect(await screen.findByText(/No drafts\./i)).toBeInTheDocument();
  });

  it("loads the Timeline view from the overflow menu", async () => {
    withMe(
      { configured: true, authenticated: true },
      { status: 200, body: [] },              // posts
      { status: 200, body: { items: [] } },   // timeline
    );
    render(<App />);
    await screen.findByRole("button", { name: "repeat" });
    await openMenu();
    await userEvent.click(await screen.findByRole("menuitem", { name: /Timeline/i }));
    expect(await screen.findByText(/Nothing here yet/i)).toBeInTheDocument();
  });

  it("loads the Subscriptions view from the overflow menu", async () => {
    withMe(
      { configured: true, authenticated: true },
      { status: 200, body: [] },  // posts
      { status: 200, body: [] },  // subs
    );
    render(<App />);
    await screen.findByRole("button", { name: "repeat" });
    await openMenu();
    await userEvent.click(await screen.findByRole("menuitem", { name: /Subscriptions/i }));
    expect(await screen.findByText(/No subscriptions yet\./i)).toBeInTheDocument();
  });

  it("loads the Settings placeholder from the overflow menu", async () => {
    withMe(
      { configured: true, authenticated: true },
      { status: 200, body: [] }, // posts
    );
    render(<App />);
    await screen.findByRole("button", { name: "repeat" });
    await openMenu();
    await userEvent.click(await screen.findByRole("menuitem", { name: /Settings/i }));
    expect(await screen.findByRole("heading", { name: /Settings/i })).toBeInTheDocument();
  });
});

describe("App Login", () => {
  it("re-loads /me after a successful POST /login", async () => {
    queueFetch([
      { status: 200, body: { configured: true, authenticated: false } },
      { status: 204 },                                                       // POST /login
      { status: 200, body: { configured: true, authenticated: true } },     // re-load /me
      { status: 200, body: [] },                                              // /posts
    ]);
    render(<App />);
    await screen.findByPlaceholderText("Password");
    await userEvent.type(screen.getByPlaceholderText("Password"), "hunter22pw");
    await userEvent.click(screen.getByRole("button", { name: /Sign in/i }));
    expect(await screen.findByRole("button", { name: "repeat" })).toBeInTheDocument();
  });

  it("surfaces 'Wrong password.' on 401", async () => {
    queueFetch([
      { status: 200, body: { configured: true, authenticated: false } },
      { status: 401, body: "" },
    ]);
    render(<App />);
    await screen.findByPlaceholderText("Password");
    await userEvent.type(screen.getByPlaceholderText("Password"), "wrong");
    await userEvent.click(screen.getByRole("button", { name: /Sign in/i }));
    expect(await screen.findByText("Wrong password.")).toBeInTheDocument();
  });
});

describe("App Setup", () => {
  it("validates token, password length, and password match before submitting", async () => {
    withMe({ configured: false, authenticated: false });
    render(<App />);
    await screen.findByRole("heading", { name: /Welcome to repeat/i });

    // No token → error.
    await userEvent.click(screen.getByRole("button", { name: /Set password/i }));
    expect(await screen.findByText(/Setup token required/i)).toBeInTheDocument();

    // Token + short password.
    await userEvent.type(screen.getByLabelText(/Setup token/i), "tok");
    const pwInputs = screen.getAllByLabelText(/password/i);
    await userEvent.type(pwInputs[0], "short");
    await userEvent.type(pwInputs[1], "short");
    await userEvent.click(screen.getByRole("button", { name: /Set password/i }));
    expect(await screen.findByText(/at least 8 characters/i)).toBeInTheDocument();

    // Mismatched.
    await userEvent.clear(pwInputs[0]);
    await userEvent.clear(pwInputs[1]);
    await userEvent.type(pwInputs[0], "longenough");
    await userEvent.type(pwInputs[1], "different1");
    await userEvent.click(screen.getByRole("button", { name: /Set password/i }));
    expect(await screen.findByText(/Passwords don't match/i)).toBeInTheDocument();
  });

  it("calls onDone (which triggers /me reload) on successful setup", async () => {
    queueFetch([
      { status: 200, body: { configured: false, authenticated: false } },
      { status: 204 }, // POST /setup
      { status: 200, body: { configured: true, authenticated: true } },
      { status: 200, body: [] }, // /posts
    ]);
    render(<App />);
    await screen.findByRole("heading", { name: /Welcome to repeat/i });
    await userEvent.type(screen.getByLabelText(/Setup token/i), "tok");
    const pwInputs = screen.getAllByLabelText(/password/i);
    await userEvent.type(pwInputs[0], "longenough");
    await userEvent.type(pwInputs[1], "longenough");
    await userEvent.click(screen.getByRole("button", { name: /Set password/i }));
    expect(await screen.findByRole("button", { name: "repeat" })).toBeInTheDocument();
  });
});

describe("App home view", () => {
  it("posts a new note and refetches the list", async () => {
    const fn = withMe(
      { configured: true, authenticated: true },
      { status: 200, body: [] },                                              // initial /posts
      { status: 201, body: { id: "p1", body: "x", html: "<p>x</p>", date: "2026-05-09T00:00:00Z", path: "/p" } },
      {
        status: 200,
        body: [
          { id: "p1", body: "hello world", html: "<p>hello world</p>", date: "2026-05-09T00:00:00Z", path: "/p" },
        ],
      },
    );
    render(<App />);
    await screen.findByRole("button", { name: "repeat" });

    // Switch to source mode so we can drive the textarea (Lexical's
    // contenteditable is unreliable under jsdom).
    await userEvent.click(screen.getByRole("button", { name: "source" }));
    const ta = screen.getByPlaceholderText(/What's on your mind/i);
    await userEvent.type(ta, "hello world");
    await userEvent.click(screen.getByRole("button", { name: "Post" }));

    // The published post appears in the list.
    await waitFor(() =>
      expect(screen.getByText("hello world")).toBeInTheDocument(),
    );

    // The POST went where we expected.
    const postCall = fn.mock.calls.find((c) =>
      c[0] === "/admin/api/posts" && (c[1] as RequestInit | undefined)?.method === "POST",
    );
    expect(postCall).toBeTruthy();
    expect(JSON.parse((postCall![1] as RequestInit).body as string)).toMatchObject({
      title: "",
      body: "hello world",
    });
  });

  it("logs out via Sign out and triggers a /me reload", async () => {
    const fn = withMe(
      { configured: true, authenticated: true },
      { status: 200, body: [] },                                              // posts
      { status: 204 },                                                         // logout
      { status: 200, body: { configured: true, authenticated: false } },     // re-load
    );
    render(<App />);
    await screen.findByRole("button", { name: "repeat" });
    await openMenu();
    await userEvent.click(await screen.findByRole("menuitem", { name: /Sign out/i }));
    expect(await screen.findByPlaceholderText("Password")).toBeInTheDocument();
    expect(fn.mock.calls.find((c) => c[0] === "/admin/api/logout")).toBeTruthy();
  });
});
