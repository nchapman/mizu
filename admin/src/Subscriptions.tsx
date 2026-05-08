import { useEffect, useState } from "react";
import { api, Subscription, Unauthorized } from "./api";

export function SubscriptionsView({ onAuthLost }: { onAuthLost: () => void }) {
  const [list, setList] = useState<Subscription[]>([]);
  const [url, setUrl] = useState("");
  const [title, setTitle] = useState("");
  const [siteUrl, setSiteUrl] = useState("");
  const [category, setCategory] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  async function load() {
    setErr("");
    try {
      setList(await api<Subscription[]>("/admin/api/subscriptions"));
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    }
  }

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function add(e: React.FormEvent) {
    e.preventDefault();
    if (!url.trim()) return;
    setErr("");
    setBusy(true);
    try {
      await api("/admin/api/subscriptions", {
        method: "POST",
        body: JSON.stringify({ url: url.trim(), title, site_url: siteUrl, category }),
      });
      setUrl("");
      setTitle("");
      setSiteUrl("");
      setCategory("");
      await load();
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function remove(s: Subscription) {
    if (!confirm(`Unsubscribe from ${s.title || s.url}?`)) return;
    setErr("");
    try {
      await api(`/admin/api/subscriptions?url=${encodeURIComponent(s.url)}`, { method: "DELETE" });
      await load();
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    }
  }

  return (
    <div>
      <form onSubmit={add} style={{ marginBottom: "2em", border: "1px solid #ddd", borderRadius: 8, padding: "1em" }}>
        <input
          placeholder="Feed URL (https://example.com/feed.xml)"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          style={{ width: "100%", marginBottom: ".5em", padding: ".4em", fontSize: "1em" }}
        />
        <div style={{ display: "flex", gap: ".5em", marginBottom: ".5em" }}>
          <input placeholder="Title (optional)" value={title} onChange={(e) => setTitle(e.target.value)}
            style={{ flex: 1, padding: ".4em", fontSize: "1em" }} />
          <input placeholder="Category (optional)" value={category} onChange={(e) => setCategory(e.target.value)}
            style={{ flex: 1, padding: ".4em", fontSize: "1em" }} />
        </div>
        <input
          placeholder="Site URL (optional)"
          value={siteUrl}
          onChange={(e) => setSiteUrl(e.target.value)}
          style={{ width: "100%", marginBottom: ".5em", padding: ".4em", fontSize: "1em" }}
        />
        {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: ".5em" }}>{err}</div>}
        <button type="submit" disabled={busy || !url.trim()}>
          {busy ? "Subscribing…" : "Subscribe"}
        </button>
      </form>

      {list.length === 0 ? (
        <p style={{ color: "#888" }}>No subscriptions yet.</p>
      ) : (
        list.map((s) => (
          <div key={s.id} style={{ borderBottom: "1px solid #eee", padding: ".75em 0" }}>
            <div style={{ display: "flex", justifyContent: "space-between", gap: "1em" }}>
              <div style={{ minWidth: 0, flex: 1 }}>
                <div style={{ fontWeight: 500 }}>{s.title || s.url}</div>
                <div style={{ color: "#888", fontSize: ".85em", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {s.url}
                </div>
                <div style={{ color: "#888", fontSize: ".8em", marginTop: ".25em" }}>
                  {s.category && <span>{s.category} · </span>}
                  {s.last_fetched_at
                    ? <>Last fetched {new Date(s.last_fetched_at).toLocaleString()}</>
                    : <>Never fetched</>}
                  {s.last_error && <span style={{ color: "#b00" }}> · {s.last_error}</span>}
                </div>
              </div>
              <button type="button" onClick={() => remove(s)}
                style={{ background: "none", border: "none", color: "#b00", cursor: "pointer", fontSize: ".85em" }}>
                Unsubscribe
              </button>
            </div>
          </div>
        ))
      )}
    </div>
  );
}
