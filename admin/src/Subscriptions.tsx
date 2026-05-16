import { useEffect, useState } from "react";
import { AlertTriangle, Rss, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { EmptyState } from "@/components/EmptyState";
import { api, Subscription, Unauthorized } from "@/api";
import { relativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";

export function SubscriptionsView({ onAuthLost }: { onAuthLost: () => void }) {
  const [list, setList] = useState<Subscription[]>([]);
  const [url, setUrl] = useState("");
  const [title, setTitle] = useState("");
  const [siteUrl, setSiteUrl] = useState("");
  const [category, setCategory] = useState("");
  const [showOptional, setShowOptional] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [loaded, setLoaded] = useState(false);

  async function load() {
    setErr("");
    try {
      setList(await api<Subscription[]>("/admin/api/subscriptions"));
      setLoaded(true);
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
      setLoaded(true);
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
      setShowOptional(false);
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
      <h2 className="mb-1 text-lg font-semibold">Subscriptions</h2>
      <p className="mb-4 text-sm text-muted-foreground">
        Feeds you follow. New posts appear in your stream.
      </p>

      <form
        onSubmit={add}
        className="mb-6 space-y-2 rounded-xl border border-border bg-card p-3 shadow-sm"
      >
        <div className="flex gap-2">
          <Input
            placeholder="Site or feed URL (e.g. news.ycombinator.com)"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            className="flex-1"
          />
          <Button type="submit" size="sm" disabled={busy || !url.trim()}>
            {busy ? "Subscribing…" : "Subscribe"}
          </Button>
        </div>

        {showOptional ? (
          <div className="space-y-2 pt-1">
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
          </div>
        ) : (
          <button
            type="button"
            onClick={() => setShowOptional(true)}
            className="text-xs text-muted-foreground hover:text-foreground hover:underline"
          >
            Add title, category, or site URL
          </button>
        )}

        {err && (
          <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {err}
          </div>
        )}
      </form>

      {!loaded ? null : list.length === 0 ? (
        <EmptyState
          icon={Rss}
          title="No subscriptions yet"
          description="Paste a feed URL above to start following a site. New posts will land in your stream."
        />
      ) : (
        <ul className="divide-y divide-border">
          {list.map((s) => (
            <SubscriptionRow key={s.id} s={s} onRemove={remove} />
          ))}
        </ul>
      )}
    </div>
  );
}

function SubscriptionRow({ s, onRemove }: { s: Subscription; onRemove: (s: Subscription) => void }) {
  const failing = !!s.last_error;
  const initial = (s.title || hostOf(s.url) || "?").slice(0, 1).toUpperCase();

  return (
    <li className="px-1 py-3">
      <div className="flex items-start gap-3">
        <div
          aria-hidden
          className={cn(
            "mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-full text-sm font-semibold",
            failing
              ? "bg-destructive/10 text-destructive"
              : "bg-accent text-foreground/70",
          )}
        >
          {failing ? <AlertTriangle className="size-4" /> : initial}
        </div>

        <div className="min-w-0 flex-1">
          <div className="flex items-baseline justify-between gap-2">
            <div className="min-w-0 truncate font-medium text-foreground">
              {s.title || hostOf(s.url)}
            </div>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs text-muted-foreground"
                >
                  Manage
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {s.site_url && (
                  <DropdownMenuItem asChild>
                    <a href={s.site_url} target="_blank" rel="noopener noreferrer">
                      Open site
                    </a>
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem asChild>
                  <a href={s.url} target="_blank" rel="noopener noreferrer">
                    Open feed XML
                  </a>
                </DropdownMenuItem>
                <DropdownMenuItem
                  onSelect={() => onRemove(s)}
                  className="text-destructive focus:text-destructive"
                >
                  <Trash2 />
                  Unsubscribe
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>

          <div className="mt-0.5 truncate text-xs text-muted-foreground">
            <Rss className="mr-1 inline size-3 align-[-1px]" />
            {s.url}
          </div>

          <div className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
            {s.category && (
              <>
                <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-foreground/70">
                  {s.category}
                </span>
                <span>·</span>
              </>
            )}
            {failing ? (
              <span className="text-destructive">Failing · {s.last_error}</span>
            ) : s.last_fetched_at ? (
              <span>Last fetched {relativeTime(s.last_fetched_at)}</span>
            ) : (
              <span>Not yet fetched</span>
            )}
          </div>
        </div>
      </div>
    </li>
  );
}

function hostOf(raw: string): string {
  try {
    return new URL(raw).host;
  } catch {
    return raw;
  }
}
