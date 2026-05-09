import { useCallback, useEffect, useRef, useState } from "react";

import { Composer, type ComposerHandle } from "@/Composer";
import { DraftsDrawer } from "@/DraftsDrawer";
import { StreamView } from "@/Stream";
import type { Draft, Post, TimelineItem } from "@/api";
import { Unauthorized, listDrafts } from "@/api";
import type { EditTarget } from "@/Shell";

interface Props {
  onAuthLost: () => void;
  // One-shot edit handoff (e.g. drafts drawer → composer). HomeView
  // forwards into the composer's imperative load() and acks via
  // onEditConsumed.
  editTarget: EditTarget | null;
  onEditConsumed: () => void;
}

export function HomeView({ onAuthLost, editTarget, onEditConsumed }: Props) {
  const composerRef = useRef<ComposerHandle>(null);
  const [streamRefresh, setStreamRefresh] = useState(0);
  const [draftsOpen, setDraftsOpen] = useState(false);
  const [draftsCount, setDraftsCount] = useState(0);
  const [draftsRefresh, setDraftsRefresh] = useState(0);

  const refreshDraftsCount = useCallback(async () => {
    try {
      const drafts = await listDrafts();
      setDraftsCount(drafts.length);
    } catch (e) {
      if (e instanceof Unauthorized) onAuthLost();
    }
  }, [onAuthLost]);

  useEffect(() => {
    refreshDraftsCount();
  }, [refreshDraftsCount]);

  // Cmd/Ctrl+Shift+D toggles the drawer.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === "d") {
        e.preventDefault();
        setDraftsOpen((v) => !v);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    if (!editTarget) return;
    composerRef.current?.load(editTarget);
    onEditConsumed();
  }, [editTarget, onEditConsumed]);

  function startEditPost(p: Post) {
    composerRef.current?.load({
      kind: "post",
      id: p.id,
      title: p.title ?? "",
      body: p.body,
    });
  }

  function startEditDraft(d: Draft) {
    composerRef.current?.load({
      kind: "draft",
      id: d.id,
      title: d.title ?? "",
      body: d.body,
    });
  }

  function replyTo(it: TimelineItem) {
    // Quote the feed item's title (or a slice of the body if no title)
    // and link back to the source. The pill keeps the provenance visible.
    const headline = (it.title ?? "").trim() || (it.feed_title ?? "").trim() || "(untitled)";
    const link = it.url ? `[${headline}](${it.url})` : headline;
    const quote = `> ${link}\n> — ${it.feed_title}\n\n`;
    composerRef.current?.prefill(quote, { feedTitle: it.feed_title });
  }

  function handleDraftSaved() {
    setDraftsRefresh((n) => n + 1);
    refreshDraftsCount();
  }

  function handleSubmitted() {
    // Force the stream to reload so a new/updated post shows up immediately.
    setStreamRefresh((n) => n + 1);
    refreshDraftsCount();
  }

  // Drawer-driven mutations (publish, delete) can produce a new own-post in
  // the stream or remove one. Bump both counters so neither view goes stale.
  function handleDrawerChanged() {
    refreshDraftsCount();
    setStreamRefresh((n) => n + 1);
  }

  function openDrafts() {
    setDraftsRefresh((n) => n + 1);
    setDraftsOpen(true);
  }

  return (
    <div>
      <Composer
        ref={composerRef}
        onSubmitted={handleSubmitted}
        onDraftSaved={handleDraftSaved}
        onAuthLost={onAuthLost}
        draftsCount={draftsCount}
        onOpenDrafts={openDrafts}
      />
      <StreamView
        onAuthLost={onAuthLost}
        onEditOwn={startEditPost}
        onReply={replyTo}
        refreshToken={streamRefresh}
        onPostsChanged={() => setStreamRefresh((n) => n + 1)}
      />
      <DraftsDrawer
        open={draftsOpen}
        onOpenChange={setDraftsOpen}
        onAuthLost={onAuthLost}
        onEdit={startEditDraft}
        refreshKey={draftsRefresh}
        onChanged={handleDrawerChanged}
      />
    </div>
  );
}
