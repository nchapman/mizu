import { useEffect, useState } from "react";
import { Draft, Unauthorized, listDrafts, deleteDraft, publishDraft } from "./api";

export function DraftsView({
  onAuthLost,
  onEdit,
}: {
  onAuthLost: () => void;
  onEdit: (d: Draft) => void;
}) {
  const [drafts, setDrafts] = useState<Draft[]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState("");

  async function load() {
    setErr("");
    try {
      setDrafts(await listDrafts());
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    }
  }

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function publish(d: Draft) {
    if (!confirm(`Publish "${d.title || d.body.slice(0, 40)}" now?`)) return;
    setErr("");
    setBusy(d.id);
    try {
      await publishDraft(d.id);
      await load();
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  async function remove(d: Draft) {
    if (!confirm(`Delete draft "${d.title || d.body.slice(0, 40)}"?`)) return;
    setErr("");
    setBusy(d.id);
    try {
      await deleteDraft(d.id);
      await load();
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  if (drafts.length === 0) {
    return <p style={{ color: "#888" }}>No drafts. Compose something on Home and hit "+ draft" to save it without publishing.</p>;
  }

  return (
    <div>
      {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: "1em" }}>{err}</div>}
      {drafts.map((d) => (
        <article key={d.id} style={{ marginBottom: "1.5em", paddingBottom: "1em", borderBottom: "1px solid #eee" }}>
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline", gap: "1em" }}>
            <div style={{ color: "#888", fontSize: ".85em" }}>
              {new Date(d.created).toLocaleString()}
            </div>
            <div style={{ display: "flex", gap: ".5em", fontSize: ".85em" }}>
              <button type="button" onClick={() => onEdit(d)} disabled={busy === d.id} style={linkBtn}>
                edit
              </button>
              <button type="button" onClick={() => publish(d)} disabled={busy === d.id} style={linkBtn}>
                publish
              </button>
              <button type="button" onClick={() => remove(d)} disabled={busy === d.id} style={{ ...linkBtn, color: "#b00" }}>
                delete
              </button>
            </div>
          </div>
          {d.title && <h2 style={{ margin: ".2em 0", fontSize: "1.1em" }}>{d.title}</h2>}
          <div style={{ whiteSpace: "pre-wrap", color: "#444" }}>{d.body}</div>
        </article>
      ))}
    </div>
  );
}

const linkBtn: React.CSSProperties = {
  background: "none", border: "none", color: "#666", cursor: "pointer",
};
