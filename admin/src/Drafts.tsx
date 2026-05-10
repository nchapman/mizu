import { useEffect, useState } from "react";
import { FileText, MoreHorizontal, Send, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { EmptyState } from "@/components/EmptyState";
import { Draft, Unauthorized, listDrafts, deleteDraft, publishDraft } from "@/api";
import { relativeTime } from "@/lib/relativeTime";

export function DraftsView({
  onAuthLost,
  onEdit,
  refreshKey = 0,
  onChanged,
}: {
  onAuthLost: () => void;
  onEdit: (d: Draft) => void;
  // Bumping refreshKey forces a re-fetch — used by the surrounding drawer
  // when it opens, or when the composer saves a new draft.
  refreshKey?: number;
  onChanged?: () => void;
}) {
  const [drafts, setDrafts] = useState<Draft[]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const [loaded, setLoaded] = useState(false);

  async function load() {
    setErr("");
    try {
      setDrafts(await listDrafts());
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
  }, [refreshKey]);

  async function publish(d: Draft) {
    if (!confirm(`Publish "${d.title || d.body.slice(0, 40)}" now?`)) return;
    setErr("");
    setBusy(d.id);
    try {
      await publishDraft(d.id);
      await load();
      onChanged?.();
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
      onChanged?.();
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  if (!loaded) return null;

  if (drafts.length === 0) {
    return (
      <EmptyState
        icon={FileText}
        title="No drafts"
        description={`Compose something and hit "draft" to save it without publishing.`}
      />
    );
  }

  return (
    <div>
      {err && (
        <div role="alert" className="mb-3 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
          {err}
        </div>
      )}
      <ul className="divide-y divide-border">
        {drafts.map((d) => {
          const headline = d.title?.trim() || firstLine(d.body) || "Untitled draft";
          const showBody = !!d.title?.trim();
          return (
            <li key={d.id} className="px-1 py-3">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0 flex-1">
                  <h2 className="truncate text-base font-semibold leading-snug">
                    <button
                      type="button"
                      onClick={() => onEdit(d)}
                      disabled={busy === d.id}
                      className="text-foreground hover:underline disabled:opacity-50"
                    >
                      {headline}
                    </button>
                  </h2>
                  <div className="mt-0.5 text-xs text-muted-foreground">
                    {d.title?.trim() ? "Post" : "Note"} · saved {relativeTime(d.created)}
                  </div>
                  {showBody && (
                    <div
                      className="post-rendered post-rendered-muted mt-2 line-clamp-3 text-sm"
                      dangerouslySetInnerHTML={{ __html: d.html }}
                    />
                  )}
                </div>

                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    onClick={() => onEdit(d)}
                    disabled={busy === d.id}
                  >
                    Continue
                  </Button>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground"
                        aria-label="More"
                        disabled={busy === d.id}
                      >
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onSelect={() => publish(d)}>
                        <Send />
                        Publish now
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        onSelect={() => remove(d)}
                        className="text-destructive focus:text-destructive"
                      >
                        <Trash2 />
                        Delete draft
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function firstLine(body: string): string {
  const trimmed = body.trim();
  const i = trimmed.indexOf("\n");
  return i === -1 ? trimmed : trimmed.slice(0, i);
}
