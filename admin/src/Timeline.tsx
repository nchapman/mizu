import { useCallback, useEffect, useState } from "react";
import { api, Timeline, TimelineItem, Unauthorized } from "./api";

export function TimelineView({ onAuthLost }: { onAuthLost: () => void }) {
  const [items, setItems] = useState<TimelineItem[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [done, setDone] = useState(false);
  const [unreadOnly, setUnreadOnly] = useState(false);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState("");

  // fetchPage takes the cursor as an argument rather than reading it from
  // state, so a stale closure can't make the filter-reset effect fetch
  // page 2 instead of page 1.
  const fetchPage = useCallback(
    async (cursorArg: string | undefined) => {
      setLoading(true);
      setErr("");
      try {
        const params = new URLSearchParams();
        if (cursorArg) params.set("cursor", cursorArg);
        if (unreadOnly) params.set("unread", "1");
        const t = await api<Timeline>(`/admin/api/timeline?${params}`);
        setItems((prev) => (cursorArg ? [...prev, ...t.items] : t.items));
        setCursor(t.next_cursor);
        setDone(!t.next_cursor);
      } catch (e) {
        if (e instanceof Unauthorized) return onAuthLost();
        setErr((e as Error).message);
      } finally {
        setLoading(false);
      }
    },
    [unreadOnly, onAuthLost],
  );

  // Reset the list whenever the unread filter toggles.
  useEffect(() => {
    setItems([]);
    setCursor(undefined);
    setDone(false);
    fetchPage(undefined);
  }, [unreadOnly, fetchPage]);

  async function toggleRead(it: TimelineItem) {
    const next = !it.read;
    // Optimistic update.
    setItems((prev) => prev.map((x) => (x.id === it.id ? { ...x, read: next } : x)));
    try {
      await api(`/admin/api/items/${it.id}/read`, { method: next ? "POST" : "DELETE" });
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      // Revert on failure.
      setItems((prev) => prev.map((x) => (x.id === it.id ? { ...x, read: !next } : x)));
      setErr((e as Error).message);
    }
  }

  return (
    <div>
      <div style={{ display: "flex", gap: "1em", marginBottom: "1em", alignItems: "center" }}>
        <label style={{ fontSize: ".9em", color: "#555" }}>
          <input type="checkbox" checked={unreadOnly} onChange={(e) => setUnreadOnly(e.target.checked)} />
          {" "}Unread only
        </label>
      </div>

      {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: "1em" }}>{err}</div>}

      {items.length === 0 && !loading && (
        <p style={{ color: "#888" }}>Nothing here yet. Subscribe to a feed to see items.</p>
      )}

      {items.map((it) => (
        <article
          key={it.id}
          style={{
            marginBottom: "1.5em",
            paddingBottom: "1em",
            borderBottom: "1px solid #eee",
            opacity: it.read ? 0.55 : 1,
          }}
        >
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline", color: "#888", fontSize: ".85em" }}>
            <span>
              <strong style={{ color: "#444" }}>{it.feed_title}</strong>
              {it.author && <> · {it.author}</>}
              {it.published_at && <> · {new Date(it.published_at).toLocaleString()}</>}
            </span>
            <button
              type="button"
              onClick={() => toggleRead(it)}
              style={{ background: "none", border: "none", color: "#888", cursor: "pointer", fontSize: ".85em" }}
            >
              {it.read ? "Mark unread" : "Mark read"}
            </button>
          </div>
          {it.title && (
            <h2 style={{ margin: ".2em 0", fontSize: "1.05em" }}>
              {it.url ? (
                <a href={it.url} target="_blank" rel="noopener noreferrer" style={{ color: "inherit" }}>
                  {it.title}
                </a>
              ) : (
                it.title
              )}
            </h2>
          )}
          {it.content && (
            // Server sanitizes feed content with bluemonday UGCPolicy at ingest,
            // so rendering as HTML here is intentional.
            <div className="feed-content" dangerouslySetInnerHTML={{ __html: it.content }} />
          )}
        </article>
      ))}

      {!done && (
        <button type="button" onClick={() => fetchPage(cursor)} disabled={loading}>
          {loading ? "Loading…" : "Load more"}
        </button>
      )}
    </div>
  );
}
