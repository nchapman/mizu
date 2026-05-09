import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, Subscription, Unauthorized } from "@/api";
import { relativeTime } from "@/lib/relativeTime";

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
      <h2 className="mb-4 text-lg font-semibold">Subscriptions</h2>

      <form
        onSubmit={add}
        className="mb-6 space-y-2 rounded-xl border border-border bg-card p-4 shadow-sm"
      >
        <Input
          placeholder="Feed URL (https://example.com/feed.xml)"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
        />
        <div className="flex gap-2">
          <Input
            placeholder="Title (optional)"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            className="flex-1"
          />
          <Input
            placeholder="Category (optional)"
            value={category}
            onChange={(e) => setCategory(e.target.value)}
            className="flex-1"
          />
        </div>
        <Input
          placeholder="Site URL (optional)"
          value={siteUrl}
          onChange={(e) => setSiteUrl(e.target.value)}
        />
        {err && (
          <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {err}
          </div>
        )}
        <div className="flex justify-end">
          <Button type="submit" size="sm" disabled={busy || !url.trim()}>
            {busy ? "Subscribing…" : "Subscribe"}
          </Button>
        </div>
      </form>

      {list.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">No subscriptions yet.</p>
      ) : (
        <ul className="divide-y divide-border">
          {list.map((s) => (
            <li key={s.id} className="flex items-start justify-between gap-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="font-medium text-foreground">{s.title || s.url}</div>
                <div className="truncate text-xs text-muted-foreground">{s.url}</div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {s.category && <span>{s.category} · </span>}
                  {s.last_fetched_at
                    ? <>Last fetched {relativeTime(s.last_fetched_at)}</>
                    : <>Never fetched</>}
                  {s.last_error && (
                    <span className="text-destructive"> · {s.last_error}</span>
                  )}
                </div>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => remove(s)}
                className="text-destructive hover:text-destructive"
              >
                Unsubscribe
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
