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
type MeFixture = {
  configured: boolean;
  authenticated: boolean;
  setup_window?: { open: boolean; expires_at?: string };
};
function withMe(me: MeFixture, ...rest: { status: number; body?: unknown }[]) {
  // The /me payload now includes setup_window for unconfigured installs;
  // tests that pass {configured:false} without specifying one get an
  // open window by default so the wizard renders.
  const enriched: MeFixture =
    me.configured || me.setup_window !== undefined
      ? me
      : { ...me, setup_window: { open: true } };
  return queueFetch([{ status: 200, body: enriched }, ...rest]);
}

describe("App auth screens", () => {
  it("renders the Wizard welcome when /me reports unconfigured", async () => {
    withMe({ configured: false, authenticated: false });
    render(<App />);
    expect(await screen.findByRole("heading", { name: /Welcome to mizu/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Get started/i })).toBeInTheDocument();
  });

  it("renders the closed-window page when the setup window has expired", async () => {
    withMe({ configured: false, authenticated: false, setup_window: { open: false } });
    render(<App />);
    expect(await screen.findByRole("heading", { name: /Setup window has closed/i })).toBeInTheDocument();
  });

  it("renders the Login screen when configured but not authenticated", async () => {
    withMe({ configured: true, authenticated: false });
    render(<App />);
    expect(await screen.findByRole("heading", { name: /Sign in/i })).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
  });

  it("renders the Shell when authenticated", async () => {
    withMe(
      { configured: true, authenticated: true },
      { status: 200, body: { items: [] } }, // /admin/api/stream initial load
      { status: 200, body: [] },             // /admin/api/drafts (drawer count)
    );
    render(<App />);
    // The brand button anchors the new top bar.
    expect(await screen.findByRole("button", { name: "mizu" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /menu/i })).toBeInTheDocument();
  });

  it("shows a retry banner when /me fails", async () => {
    queueFetch([
      { status: 500 },
      { status: 200, body: { configured: false, authenticated: false, setup_window: { open: true } } },
    ]);
    render(<App />);
    expect(await screen.findByText(/Could not reach the server/i)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Retry/i }));
    // After retry, the configured-false state means the wizard renders.
    expect(await screen.findByRole("heading", { name: /Welcome to mizu/i })).toBeInTheDocument();
  });
});

describe("App route navigation", () => {
  it("opens the drafts drawer from the composer pill", async () => {
    const draft = {
      id: "d1",
      title: "Half-finished",
      body: "raw",
      html: "<p>raw</p>",
      created: "2026-05-09T00:00:00Z",
    };
    withMe(
      { configured: true, authenticated: true },
      { status: 200, body: { items: [] } }, // /stream
      { status: 200, body: [draft] },       // initial drafts count
      { status: 200, body: [draft] },       // drawer-open refetch
    );
    render(<App />);
    await screen.findByRole("button", { name: "mizu" });
    // The pill only renders once the count fetch resolves with a non-zero list.
    await userEvent.click(await screen.findByRole("button", { name: /Drafts ·/i }));
    expect(await screen.findByRole("heading", { name: "Drafts" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Half-finished" })).toBeInTheDocument();
  });

  it("loads the Subscriptions view from the overflow menu", async () => {
    withMe(
      { configured: true, authenticated: true },
      { status: 200, body: { items: [] } }, // /stream
      { status: 200, body: [] },             // drafts (count)
      { status: 200, body: [] },             // subs
    );
    render(<App />);
    await screen.findByRole("button", { name: "mizu" });
    await openMenu();
    await userEvent.click(await screen.findByRole("menuitem", { name: /Subscriptions/i }));
    expect(await screen.findByText(/No subscriptions yet/i)).toBeInTheDocument();
  });

  it("loads the Settings panel from the overflow menu", async () => {
    withMe(
      { configured: true, authenticated: true },
      { status: 200, body: { items: [] } }, // /stream
      { status: 200, body: [] },             // drafts (count)
    );
    render(<App />);
    await screen.findByRole("button", { name: "mizu" });
    await openMenu();
    await userEvent.click(await screen.findByRole("menuitem", { name: /Settings/i }));
    expect(await screen.findByRole("heading", { name: /^Settings$/ })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: /^Change password$/ })).toBeInTheDocument();
  });
});

describe("App Login", () => {
  it("re-loads /me after a successful POST /login", async () => {
    queueFetch([
      { status: 200, body: { configured: true, authenticated: false } },
      { status: 204 },                                                       // POST /login
      { status: 200, body: { configured: true, authenticated: true } },     // re-load /me
      { status: 200, body: { items: [] } },                                   // /stream
      { status: 200, body: [] },                                              // /drafts (count)
    ]);
    render(<App />);
    await screen.findByLabelText("Email");
    await userEvent.type(screen.getByLabelText("Email"), "a@b.com");
    await userEvent.type(screen.getByLabelText("Password"), "hunter22pw");
    await userEvent.click(screen.getByRole("button", { name: /Sign in/i }));
    expect(await screen.findByRole("button", { name: "mizu" })).toBeInTheDocument();
  });

  it("surfaces 'Wrong email or password.' on 401", async () => {
    queueFetch([
      { status: 200, body: { configured: true, authenticated: false } },
      { status: 401, body: "" },
    ]);
    render(<App />);
    await screen.findByLabelText("Email");
    await userEvent.type(screen.getByLabelText("Email"), "a@b.com");
    await userEvent.type(screen.getByLabelText("Password"), "wrong");
    await userEvent.click(screen.getByRole("button", { name: /Sign in/i }));
    expect(await screen.findByText("Wrong email or password.")).toBeInTheDocument();
  });
});

describe("App Wizard", () => {
  async function advanceToAccount() {
    await userEvent.click(await screen.findByRole("button", { name: /Get started/i }));
  }

  it("validates email, password length, and match before POSTing /setup", async () => {
    withMe({ configured: false, authenticated: false });
    render(<App />);
    await advanceToAccount();

    // Empty email.
    await userEvent.click(screen.getByRole("button", { name: /Create account/i }));
    expect(await screen.findByText(/Email required/i)).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Email"), "a@b.com");
    await userEvent.type(screen.getByLabelText("New password"), "short");
    await userEvent.type(screen.getByLabelText(/Confirm password/i), "short");
    await userEvent.click(screen.getByRole("button", { name: /Create account/i }));
    expect(await screen.findByText(/at least 8 characters/i)).toBeInTheDocument();

    await userEvent.clear(screen.getByLabelText("New password"));
    await userEvent.clear(screen.getByLabelText(/Confirm password/i));
    await userEvent.type(screen.getByLabelText("New password"), "longenough");
    await userEvent.type(screen.getByLabelText(/Confirm password/i), "different1");
    await userEvent.click(screen.getByRole("button", { name: /Create account/i }));
    expect(await screen.findByText(/Passwords don't match/i)).toBeInTheDocument();
  });

  it("advances from account to site step on successful /setup", async () => {
    queueFetch([
      { status: 200, body: { configured: false, authenticated: false, setup_window: { open: true } } },
      { status: 204 }, // POST /setup
    ]);
    render(<App />);
    await advanceToAccount();
    await userEvent.type(screen.getByLabelText("Email"), "alice@example.com");
    await userEvent.type(screen.getByLabelText("New password"), "longenough");
    await userEvent.type(screen.getByLabelText(/Confirm password/i), "longenough");
    await userEvent.click(screen.getByRole("button", { name: /Create account/i }));
    expect(await screen.findByRole("heading", { name: /About your site/i })).toBeInTheDocument();
  });
});

describe("App home view", () => {
  it("posts a new note and refetches the list", async () => {
    const fn = withMe(
      { configured: true, authenticated: true },
      { status: 200, body: { items: [] } },                                   // initial /stream
      { status: 200, body: [] },                                              // initial /drafts (count)
      { status: 201, body: { id: "p1", body: "x", html: "<p>x</p>", date: "2026-05-09T00:00:00Z", path: "/p" } },
      // After submit, HomeView fires refreshDraftsCount synchronously and
      // bumps streamRefresh. /drafts lands first because it's invoked
      // inside the same event handler; /stream re-fetches after commit.
      { status: 200, body: [] },                                              // post-submit /drafts refresh
      {
        status: 200,
        body: {
          items: [
            {
              kind: "own",
              post: {
                id: "p1",
                body: "hello world",
                html: "<p>hello world</p>",
                date: "2026-05-09T00:00:00Z",
                path: "/p",
              },
            },
          ],
        },
      },
    );
    render(<App />);
    await screen.findByRole("button", { name: "mizu" });

    // Switch to source mode so we can drive the textarea (Lexical's
    // contenteditable is unreliable under jsdom).
    await userEvent.click(screen.getByRole("button", { name: "source" }));
    const ta = screen.getByPlaceholderText(/What's on your mind/i);
    await userEvent.type(ta, "hello world");
    await userEvent.click(screen.getByRole("button", { name: "Post" }));

    // The published post appears in the stream.
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
      { status: 200, body: { items: [] } },                                   // /stream
      { status: 200, body: [] },                                              // drafts (count)
      { status: 204 },                                                         // logout
      { status: 200, body: { configured: true, authenticated: false } },     // re-load
    );
    render(<App />);
    await screen.findByRole("button", { name: "mizu" });
    await openMenu();
    await userEvent.click(await screen.findByRole("menuitem", { name: /Sign out/i }));
    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
    expect(fn.mock.calls.find((c) => c[0] === "/admin/api/logout")).toBeTruthy();
  });
});
