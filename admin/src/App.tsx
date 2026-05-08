import { useEffect, useState } from "react";
import { api, Post, Unauthorized } from "./api";
import { TimelineView } from "./Timeline";
import { SubscriptionsView } from "./Subscriptions";

type Me = { configured: boolean; authenticated: boolean };
type Tab = "home" | "timeline" | "subs";

export function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [initErr, setInitErr] = useState(false);

  async function loadMe() {
    try {
      setInitErr(false);
      const r = await fetch("/admin/api/me");
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      setMe(await r.json());
    } catch {
      setInitErr(true);
    }
  }
  useEffect(() => {
    loadMe();
  }, []);

  if (initErr) {
    return (
      <div style={shellStyle}>
        <p>Could not reach the server.</p>
        <button type="button" onClick={loadMe}>Retry</button>
      </div>
    );
  }
  if (!me) return null;
  if (!me.configured) return <Setup onDone={loadMe} />;
  if (!me.authenticated) return <Login onDone={loadMe} />;
  return <Shell onLogout={loadMe} />;
}

const shellStyle: React.CSSProperties = {
  font: "15px/1.5 system-ui",
  maxWidth: 680,
  margin: "2em auto",
  padding: "0 1em",
};

function Shell({ onLogout }: { onLogout: () => void }) {
  const [tab, setTab] = useState<Tab>("home");

  async function logout() {
    await fetch("/admin/api/logout", { method: "POST" });
    onLogout();
  }

  return (
    <div style={shellStyle}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "1em" }}>
        <h1 style={{ fontSize: "1.2em", margin: 0 }}>repeat</h1>
        <button type="button" onClick={logout} style={linkBtn}>Sign out</button>
      </div>
      <nav style={{ display: "flex", gap: ".5em", borderBottom: "1px solid #ddd", marginBottom: "1.5em" }}>
        <TabBtn active={tab === "home"} onClick={() => setTab("home")}>Home</TabBtn>
        <TabBtn active={tab === "timeline"} onClick={() => setTab("timeline")}>Timeline</TabBtn>
        <TabBtn active={tab === "subs"} onClick={() => setTab("subs")}>Subscriptions</TabBtn>
      </nav>
      {tab === "home" && <HomeView onAuthLost={onLogout} />}
      {tab === "timeline" && <TimelineView onAuthLost={onLogout} />}
      {tab === "subs" && <SubscriptionsView onAuthLost={onLogout} />}
    </div>
  );
}

const linkBtn: React.CSSProperties = {
  background: "none", border: "none", color: "#666", cursor: "pointer",
};

function TabBtn({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        background: "none", border: "none", cursor: "pointer", padding: ".5em .75em",
        borderBottom: active ? "2px solid #333" : "2px solid transparent",
        color: active ? "#111" : "#666",
        fontWeight: active ? 500 : 400,
        marginBottom: -1,
      }}
    >
      {children}
    </button>
  );
}

function HomeView({ onAuthLost }: { onAuthLost: () => void }) {
  const [posts, setPosts] = useState<Post[]>([]);
  const [body, setBody] = useState("");
  const [title, setTitle] = useState("");
  const [showTitle, setShowTitle] = useState(false);
  const [posting, setPosting] = useState(false);
  const [err, setErr] = useState("");

  async function load() {
    try {
      setPosts(await api<Post[]>("/admin/api/posts"));
      setErr("");
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    }
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!body.trim()) return;
    setPosting(true);
    setErr("");
    try {
      await api("/admin/api/posts", {
        method: "POST",
        body: JSON.stringify({ title: showTitle ? title : "", body }),
      });
      setBody("");
      setTitle("");
      setShowTitle(false);
      await load();
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    } finally {
      setPosting(false);
    }
  }

  return (
    <div>
      <form onSubmit={submit} style={{ marginBottom: "2em", border: "1px solid #ddd", borderRadius: 8, padding: "1em" }}>
        {showTitle && (
          <input
            placeholder="Title"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            style={{ width: "100%", marginBottom: ".5em", padding: ".4em", fontSize: "1em" }}
          />
        )}
        <textarea
          placeholder="What's on your mind?"
          value={body}
          onChange={(e) => setBody(e.target.value)}
          rows={showTitle ? 8 : 3}
          style={{ width: "100%", padding: ".4em", fontSize: "1em", resize: "vertical" }}
        />
        <div style={{ display: "flex", justifyContent: "space-between", marginTop: ".5em" }}>
          <button type="button" onClick={() => setShowTitle((v) => !v)} style={linkBtn}>
            {showTitle ? "− title" : "+ title"}
          </button>
          <button type="submit" disabled={posting || !body.trim()}>
            {posting ? "Posting…" : "Post"}
          </button>
        </div>
      </form>

      {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: "1em" }}>{err}</div>}

      {posts.map((p) => (
        <article key={p.id} style={{ marginBottom: "1.5em", paddingBottom: "1em", borderBottom: "1px solid #eee" }}>
          <div style={{ color: "#888", fontSize: ".85em" }}>
            <a href={p.path} style={{ color: "inherit" }}>
              {new Date(p.date).toLocaleString()}
            </a>
          </div>
          {p.title && <h2 style={{ margin: ".2em 0", fontSize: "1.1em" }}>{p.title}</h2>}
          <div style={{ whiteSpace: "pre-wrap" }}>{p.body}</div>
        </article>
      ))}
    </div>
  );
}

function Setup({ onDone }: { onDone: () => void }) {
  const [token, setToken] = useState("");
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    if (!token.trim()) return setErr("Setup token required (printed in the server log).");
    if (pw.length < 8) return setErr("Password must be at least 8 characters.");
    if (pw !== pw2) return setErr("Passwords don't match.");
    setBusy(true);
    try {
      const r = await fetch("/admin/api/setup", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ password: pw, token: token.trim() }),
      });
      if (!r.ok) {
        setErr((await r.text()) || "Setup failed.");
        return;
      }
      onDone();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={shellStyle}>
      <h1 style={{ fontSize: "1.2em" }}>Welcome to repeat</h1>
      <p style={{ color: "#555" }}>
        Set a password to lock down the admin UI. The one-time setup token was printed to your
        server log when repeat started — paste it below to prove you're the operator.
      </p>
      <form onSubmit={submit} style={{ border: "1px solid #ddd", borderRadius: 8, padding: "1em" }}>
        <label style={{ display: "block", marginBottom: ".5em" }}>
          <div style={{ fontSize: ".9em", color: "#555" }}>Setup token</div>
          <input autoFocus value={token} onChange={(e) => setToken(e.target.value)}
            spellCheck={false} autoCapitalize="off"
            style={{ width: "100%", padding: ".4em", fontSize: "1em", fontFamily: "monospace" }} />
        </label>
        <label style={{ display: "block", marginBottom: ".5em" }}>
          <div style={{ fontSize: ".9em", color: "#555" }}>New password</div>
          <input type="password" value={pw} onChange={(e) => setPw(e.target.value)}
            style={{ width: "100%", padding: ".4em", fontSize: "1em" }} />
        </label>
        <label style={{ display: "block", marginBottom: ".5em" }}>
          <div style={{ fontSize: ".9em", color: "#555" }}>Confirm password</div>
          <input type="password" value={pw2} onChange={(e) => setPw2(e.target.value)}
            style={{ width: "100%", padding: ".4em", fontSize: "1em" }} />
        </label>
        {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: ".5em" }}>{err}</div>}
        <button type="submit" disabled={busy}>{busy ? "Saving…" : "Set password"}</button>
      </form>
    </div>
  );
}

function Login({ onDone }: { onDone: () => void }) {
  const [pw, setPw] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      const r = await fetch("/admin/api/login", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ password: pw }),
      });
      if (!r.ok) {
        setErr(r.status === 401 ? "Wrong password." : (await r.text()) || "Login failed.");
        return;
      }
      onDone();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={shellStyle}>
      <h1 style={{ fontSize: "1.2em" }}>Sign in</h1>
      <form onSubmit={submit} style={{ border: "1px solid #ddd", borderRadius: 8, padding: "1em" }}>
        <input type="password" autoFocus placeholder="Password" value={pw}
          onChange={(e) => setPw(e.target.value)}
          style={{ width: "100%", padding: ".4em", fontSize: "1em", marginBottom: ".5em" }} />
        {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: ".5em" }}>{err}</div>}
        <button type="submit" disabled={busy || !pw}>{busy ? "Signing in…" : "Sign in"}</button>
      </form>
    </div>
  );
}
