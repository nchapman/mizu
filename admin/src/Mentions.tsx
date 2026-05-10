import { useEffect, useState } from "react";
import { AtSign, ExternalLink } from "lucide-react";

import { EmptyState } from "@/components/EmptyState";
import { listMentions, Mention, Unauthorized } from "@/api";
import { relativeTime } from "@/lib/relativeTime";

export function MentionsView({ onAuthLost }: { onAuthLost: () => void }) {
  const [list, setList] = useState<Mention[]>([]);
  const [err, setErr] = useState("");
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const m = await listMentions();
        if (cancelled) return;
        setList(m);
        setLoaded(true);
      } catch (e) {
        if (cancelled) return;
        if (e instanceof Unauthorized) return onAuthLost();
        setErr((e as Error).message);
        setLoaded(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [onAuthLost]);

  return (
    <div>
      <h2 className="mb-1 text-lg font-semibold">Mentions</h2>
      <p className="mb-4 text-sm text-muted-foreground">
        Other sites that have linked to your posts.
      </p>

      {err && (
        <div
          role="alert"
          className="mb-4 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive"
        >
          {err}
        </div>
      )}

      {!loaded ? null : list.length === 0 ? (
        <EmptyState
          icon={AtSign}
          title="No mentions yet"
          description="When other sites link to one of your posts, the verified mention will appear here."
        />
      ) : (
        <ul className="divide-y divide-border">
          {list.map((m) => (
            <MentionRow key={m.id} m={m} />
          ))}
        </ul>
      )}
    </div>
  );
}

function MentionRow({ m }: { m: Mention }) {
  const targetLabel = m.target_title?.trim() || m.target_path || m.target;
  const when = m.verified_at || m.received_at;

  return (
    <li className="px-1 py-4">
      <div className="flex items-start gap-3">
        <div
          aria-hidden
          className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full bg-accent text-foreground/70"
        >
          <AtSign className="size-4" />
        </div>

        <div className="min-w-0 flex-1">
          <header className="flex items-baseline justify-between gap-2 text-xs text-muted-foreground">
            <div className="min-w-0 truncate">
              <span className="font-medium text-foreground/80">{m.source_host}</span>
              <span> mentioned you</span>
            </div>
            <span className="shrink-0">{relativeTime(when)}</span>
          </header>

          <a
            href={m.target_path}
            className="mt-1 block truncate text-base font-semibold leading-snug text-foreground hover:underline"
          >
            {targetLabel}
          </a>

          <a
            href={m.source}
            target="_blank"
            rel="noopener noreferrer"
            className="mt-1 inline-flex max-w-full items-center gap-1 truncate text-xs text-muted-foreground hover:text-foreground hover:underline"
          >
            <ExternalLink className="size-3 shrink-0" />
            <span className="truncate">{m.source}</span>
          </a>
        </div>
      </div>
    </li>
  );
}
