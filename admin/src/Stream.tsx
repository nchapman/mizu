import { useCallback, useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { StreamCard } from "@/StreamCard";
import {
  api,
  deletePost,
  type Post,
  type Stream,
  type StreamItem,
  type TimelineItem,
  Unauthorized,
} from "@/api";
import { cn } from "@/lib/utils";

type Filter = "all" | "unread" | "yours";

interface Props {
  onAuthLost: () => void;
  // The home view passes its post-edit affordance down so own-post cards
  // can hand control back to the composer.
  onEditOwn: (p: Post) => void;
  // Bumping refreshToken forces a full re-fetch (e.g. after composer
  // submit, draft publish, or a deletion that originated outside Stream).
  refreshToken?: number;
  onPostsChanged?: () => void;
}

export function StreamView({ onAuthLost, onEditOwn, refreshToken = 0, onPostsChanged }: Props) {
  const [items, setItems] = useState<StreamItem[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [done, setDone] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState("");

  // fetchPage takes the cursor as an argument rather than reading state, so
  // a stale closure can't make the filter-reset effect fetch page 2 instead
  // of page 1.
  const fetchPage = useCallback(
    async (cursorArg: string | undefined) => {
      setLoading(true);
      setErr("");
      try {
        const params = new URLSearchParams();
        if (cursorArg) params.set("cursor", cursorArg);
        if (filter !== "all") params.set("filter", filter);
        const t = await api<Stream>(`/admin/api/stream?${params}`);
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
    [filter, onAuthLost],
  );

  useEffect(() => {
    setItems([]);
    setCursor(undefined);
    setDone(false);
    fetchPage(undefined);
  }, [filter, refreshToken, fetchPage]);

  const setRead = useCallback(
    async (it: TimelineItem, next: boolean) => {
      // Optimistic update.
      setItems((prev) =>
        prev.map((x) =>
          x.kind === "feed" && x.item.id === it.id
            ? { kind: "feed", item: { ...x.item, read: next } }
            : x,
        ),
      );
      try {
        await api(`/admin/api/items/${it.id}/read`, { method: next ? "POST" : "DELETE" });
      } catch (e) {
        if (e instanceof Unauthorized) return onAuthLost();
        setItems((prev) =>
          prev.map((x) =>
            x.kind === "feed" && x.item.id === it.id
              ? { kind: "feed", item: { ...x.item, read: !next } }
              : x,
          ),
        );
        setErr((e as Error).message);
      }
    },
    [onAuthLost],
  );

  const onMarkRead = useCallback((it: TimelineItem) => setRead(it, true), [setRead]);
  const onMarkUnread = useCallback((it: TimelineItem) => setRead(it, false), [setRead]);

  const onDeleteOwn = useCallback(
    async (p: Post) => {
      const label = p.title || p.body.slice(0, 40);
      if (!confirm(`Delete "${label}"?`)) return;
      try {
        await deletePost(p.id);
        setItems((prev) =>
          prev.filter((x) => !(x.kind === "own" && x.post.id === p.id)),
        );
        onPostsChanged?.();
      } catch (e) {
        if (e instanceof Unauthorized) return onAuthLost();
        setErr((e as Error).message);
      }
    },
    [onAuthLost, onPostsChanged],
  );

  return (
    <div>
      <FilterPills value={filter} onChange={setFilter} />

      {err && (
        <div role="alert" className="mb-3 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
          {err}
        </div>
      )}

      {items.length === 0 && !loading && (
        <p className="py-8 text-center text-sm text-muted-foreground">
          Nothing here yet. Subscribe to a feed or compose a post to see it here.
        </p>
      )}

      {items.length === 0 && loading && (
        <div className="space-y-4 py-2">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      )}

      {items.map((it) => (
        <StreamCard
          key={`${it.kind}:${it.kind === "feed" ? it.item.id : it.post.id}`}
          item={it}
          onMarkRead={onMarkRead}
          onMarkUnread={onMarkUnread}
          onEditOwn={onEditOwn}
          onDeleteOwn={onDeleteOwn}
        />
      ))}

      {!done && items.length > 0 && (
        <div className="py-4 text-center">
          <Button variant="outline" size="sm" onClick={() => fetchPage(cursor)} disabled={loading}>
            {loading ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </div>
  );
}

function FilterPills({ value, onChange }: { value: Filter; onChange: (f: Filter) => void }) {
  const opts: { id: Filter; label: string }[] = [
    { id: "all", label: "All" },
    { id: "unread", label: "Unread" },
    { id: "yours", label: "Yours" },
  ];
  return (
    <div role="tablist" aria-label="Stream filter" className="mb-4 flex gap-1">
      {opts.map((o) => (
        <button
          key={o.id}
          role="tab"
          aria-selected={value === o.id}
          onClick={() => onChange(o.id)}
          className={cn(
            "rounded-full px-3 py-1 text-xs font-medium transition-colors",
            value === o.id
              ? "bg-secondary text-foreground"
              : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
