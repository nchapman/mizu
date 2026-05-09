import { useEffect, useState } from "react";
import { Pencil, Send, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
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

  if (drafts.length === 0) {
    return (
      <p className="py-8 text-center text-sm text-muted-foreground">
        No drafts. Compose something and hit "draft" to save it without publishing.
      </p>
    );
  }

  return (
    <div className="space-y-4">
      {err && (
        <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
          {err}
        </div>
      )}
      {drafts.map((d) => (
        <article key={d.id} className="border-b border-border pb-4">
          <div className="mb-1 flex items-center justify-between text-xs text-muted-foreground">
            <span>{relativeTime(d.created)}</span>
            <div className="flex gap-1">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => onEdit(d)}
                disabled={busy === d.id}
              >
                <Pencil />
                edit
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => publish(d)}
                disabled={busy === d.id}
              >
                <Send />
                publish
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => remove(d)}
                disabled={busy === d.id}
                className="text-destructive hover:text-destructive"
              >
                <Trash2 />
                delete
              </Button>
            </div>
          </div>
          {d.title && <h2 className="mb-1 text-base font-semibold leading-snug">{d.title}</h2>}
          <div
            className="post-rendered post-rendered-muted text-sm"
            dangerouslySetInnerHTML={{ __html: d.html }}
          />
        </article>
      ))}
    </div>
  );
}
