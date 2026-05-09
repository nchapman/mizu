import { useEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import {
  api, Post, Draft, Unauthorized, uploadMedia,
  updatePost, deletePost, createDraft, updateDraft, publishDraft,
} from "./api";
import { TimelineView } from "./Timeline";
import { SubscriptionsView } from "./Subscriptions";
import { DraftsView } from "./Drafts";

type Me = { configured: boolean; authenticated: boolean };
type Tab = "home" | "drafts" | "timeline" | "subs";

// EditTarget is the cross-tab handoff used when an action in another
// tab (e.g. "edit this draft") needs to drop a record into the home
// composer. The home view consumes it once and clears it.
export type EditTarget =
  | { kind: "post"; id: string; title: string; body: string }
  | { kind: "draft"; id: string; title: string; body: string };

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
  const [editTarget, setEditTarget] = useState<EditTarget | null>(null);

  async function logout() {
    await fetch("/admin/api/logout", { method: "POST" });
    onLogout();
  }

  function editDraft(d: Draft) {
    setEditTarget({ kind: "draft", id: d.id, title: d.title ?? "", body: d.body });
    setTab("home");
  }

  return (
    <div style={shellStyle}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "1em" }}>
        <h1 style={{ fontSize: "1.2em", margin: 0 }}>repeat</h1>
        <button type="button" onClick={logout} style={linkBtn}>Sign out</button>
      </div>
      <nav style={{ display: "flex", gap: ".5em", borderBottom: "1px solid #ddd", marginBottom: "1.5em" }}>
        <TabBtn active={tab === "home"} onClick={() => setTab("home")}>Home</TabBtn>
        <TabBtn active={tab === "drafts"} onClick={() => setTab("drafts")}>Drafts</TabBtn>
        <TabBtn active={tab === "timeline"} onClick={() => setTab("timeline")}>Timeline</TabBtn>
        <TabBtn active={tab === "subs"} onClick={() => setTab("subs")}>Subscriptions</TabBtn>
      </nav>
      {tab === "home" && (
        <HomeView
          onAuthLost={onLogout}
          editTarget={editTarget}
          onEditConsumed={() => setEditTarget(null)}
        />
      )}
      {tab === "drafts" && <DraftsView onAuthLost={onLogout} onEdit={editDraft} />}
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

function HomeView({
  onAuthLost,
  editTarget,
  onEditConsumed,
}: {
  onAuthLost: () => void;
  editTarget: EditTarget | null;
  onEditConsumed: () => void;
}) {
  const [posts, setPosts] = useState<Post[]>([]);
  const [body, setBody] = useState("");
  const [title, setTitle] = useState("");
  const [showTitle, setShowTitle] = useState(false);
  const [editingPostId, setEditingPostId] = useState<string | null>(null);
  const [editingDraftId, setEditingDraftId] = useState<string | null>(null);
  const [posting, setPosting] = useState(false);
  const [err, setErr] = useState("");
  const [uploading, setUploading] = useState(false);
  const [dragActive, setDragActive] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  function resetComposer() {
    setEditingPostId(null);
    setEditingDraftId(null);
    setBody("");
    setTitle("");
    setShowTitle(false);
  }

  function startEdit(p: Post) {
    setEditingPostId(p.id);
    setEditingDraftId(null);
    setTitle(p.title ?? "");
    setBody(p.body);
    setShowTitle(!!p.title);
    setErr("");
    queueMicrotask(() => {
      textareaRef.current?.focus();
      window.scrollTo({ top: 0, behavior: "smooth" });
    });
  }

  // When a sibling tab hands us something to edit (e.g. a draft from
  // the Drafts tab), load it into the composer and clear the handoff
  // so a re-render or tab switch doesn't reapply it. onEditConsumed
  // is intentionally omitted from the dep list — Shell re-creates it
  // every render, and we only want to react to editTarget changes.
  useEffect(() => {
    if (!editTarget) return;
    if (editTarget.kind === "draft") {
      setEditingDraftId(editTarget.id);
      setEditingPostId(null);
    } else {
      setEditingPostId(editTarget.id);
      setEditingDraftId(null);
    }
    setTitle(editTarget.title);
    setBody(editTarget.body);
    setShowTitle(!!editTarget.title);
    setErr("");
    onEditConsumed();
    queueMicrotask(() => {
      textareaRef.current?.focus();
      window.scrollTo({ top: 0, behavior: "smooth" });
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editTarget]);

  async function remove(p: Post) {
    const label = p.title || p.body.slice(0, 40);
    if (!confirm(`Delete "${label}"?`)) return;
    setErr("");
    try {
      await deletePost(p.id);
      if (editingPostId === p.id) resetComposer();
      await load();
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    }
  }

  // Inserts text at the textarea's caret. flushSync forces React to
  // commit the new body synchronously so we can move the caret in the
  // same tick — without it, a queueMicrotask hack would race the
  // commit. Using the functional setState form is critical here:
  // sequential calls in a loop must each see the latest body, not a
  // stale snapshot from the original render.
  function insertAtCaret(text: string) {
    const ta = textareaRef.current;
    let caret = -1;
    flushSync(() => {
      setBody((prev) => {
        const start = ta?.selectionStart ?? prev.length;
        const end = ta?.selectionEnd ?? prev.length;
        caret = start + text.length;
        return prev.slice(0, start) + text + prev.slice(end);
      });
    });
    if (ta && caret >= 0) {
      ta.focus();
      ta.setSelectionRange(caret, caret);
    }
  }

  async function uploadFiles(files: File[]) {
    if (files.length === 0) return;
    setErr("");
    setUploading(true);
    try {
      for (const f of files) {
        const m = await uploadMedia(f);
        const alt = f.name.replace(/\.[^.]+$/, "");
        insertAtCaret(`![${alt}](${m.url})\n`);
      }
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    } finally {
      setUploading(false);
    }
  }

  function onPaste(e: React.ClipboardEvent<HTMLTextAreaElement>) {
    const files = Array.from(e.clipboardData.files).filter((f) => f.type.startsWith("image/"));
    if (files.length === 0) return;
    e.preventDefault();
    void uploadFiles(files);
  }

  function onDrop(e: React.DragEvent<HTMLTextAreaElement>) {
    e.preventDefault();
    setDragActive(false);
    const files = Array.from(e.dataTransfer.files).filter((f) => f.type.startsWith("image/"));
    if (files.length > 0) void uploadFiles(files);
  }

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
      const payload = { title: showTitle ? title : "", body };
      if (editingPostId) {
        await updatePost(editingPostId, payload);
      } else if (editingDraftId) {
        // Capture latest edits before publishing so they aren't lost
        // if the publish call partially fails downstream.
        await updateDraft(editingDraftId, payload);
        await publishDraft(editingDraftId);
      } else {
        await api("/admin/api/posts", { method: "POST", body: JSON.stringify(payload) });
      }
      resetComposer();
      await load();
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    } finally {
      setPosting(false);
    }
  }

  // Save Draft is only meaningful when the composer isn't editing a
  // published post. When editing an existing draft it updates in
  // place; otherwise it creates a new one.
  async function saveDraft() {
    if (!body.trim()) return;
    setPosting(true);
    setErr("");
    try {
      const payload = { title: showTitle ? title : "", body };
      if (editingDraftId) {
        await updateDraft(editingDraftId, payload);
      } else {
        await createDraft(payload);
      }
      resetComposer();
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
          ref={textareaRef}
          placeholder="What's on your mind? (paste or drop images to upload)"
          value={body}
          onChange={(e) => setBody(e.target.value)}
          onPaste={onPaste}
          onDrop={onDrop}
          onDragOver={(e) => { e.preventDefault(); setDragActive(true); }}
          onDragLeave={() => setDragActive(false)}
          rows={showTitle ? 8 : 3}
          style={{
            width: "100%", padding: ".4em", fontSize: "1em", resize: "vertical",
            outline: dragActive ? "2px dashed #4a90e2" : undefined,
          }}
        />
        <input
          ref={fileInputRef}
          type="file"
          accept="image/*"
          multiple
          style={{ display: "none" }}
          onChange={(e) => {
            const files = Array.from(e.target.files ?? []);
            e.target.value = "";
            void uploadFiles(files);
          }}
        />
        <div style={{ display: "flex", justifyContent: "space-between", marginTop: ".5em" }}>
          <div style={{ display: "flex", gap: ".5em", alignItems: "center" }}>
            <button type="button" onClick={() => setShowTitle((v) => !v)} style={linkBtn}>
              {showTitle ? "− title" : "+ title"}
            </button>
            <button type="button" onClick={() => fileInputRef.current?.click()} style={linkBtn} disabled={uploading}>
              {uploading ? "uploading…" : "+ image"}
            </button>
          </div>
          <div style={{ display: "flex", gap: ".5em", alignItems: "center" }}>
            {(editingPostId || editingDraftId) && (
              <button type="button" onClick={resetComposer} style={linkBtn}>
                cancel
              </button>
            )}
            {!editingPostId && (
              <button
                type="button"
                onClick={saveDraft}
                disabled={posting || uploading || !body.trim()}
                style={linkBtn}
              >
                {editingDraftId ? "save draft" : "+ draft"}
              </button>
            )}
            <button type="submit" disabled={posting || uploading || !body.trim()}>
              {posting
                ? "Saving…"
                : editingPostId
                ? "Save"
                : editingDraftId
                ? "Publish"
                : "Post"}
            </button>
          </div>
        </div>
      </form>

      {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: "1em" }}>{err}</div>}

      {posts.map((p) => {
        const isEditing = editingPostId === p.id;
        return (
          <article
            key={p.id}
            style={{
              marginBottom: "1.5em",
              paddingBottom: "1em",
              borderBottom: "1px solid #eee",
              opacity: isEditing ? 0.5 : 1,
            }}
          >
            <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline", gap: "1em" }}>
              <div style={{ color: "#888", fontSize: ".85em" }}>
                <a href={p.path} style={{ color: "inherit" }}>
                  {new Date(p.date).toLocaleString()}
                </a>
              </div>
              <div style={{ display: "flex", gap: ".5em", fontSize: ".85em" }}>
                <button type="button" onClick={() => startEdit(p)} style={linkBtn} disabled={isEditing}>
                  edit
                </button>
                <button type="button" onClick={() => remove(p)} style={{ ...linkBtn, color: "#b00" }}>
                  delete
                </button>
              </div>
            </div>
            {p.title && <h2 style={{ margin: ".2em 0", fontSize: "1.1em" }}>{p.title}</h2>}
            <div style={{ whiteSpace: "pre-wrap" }}>{p.body}</div>
          </article>
        );
      })}
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
