import { useCallback, useEffect, useRef, useState } from "react";

import { Composer, type ComposerHandle } from "@/Composer";
import { DraftsDrawer } from "@/DraftsDrawer";
import { PostList } from "@/PostList";
import type { Draft, Post } from "@/api";
import { Unauthorized, listDrafts } from "@/api";
import type { EditTarget } from "@/Shell";

interface Props {
  onAuthLost: () => void;
  // One-shot edit handoff from a sibling view (e.g. Drafts → composer).
  // HomeView forwards into the composer's imperative load() and acks via
  // onEditConsumed.
  editTarget: EditTarget | null;
  onEditConsumed: () => void;
}

export function HomeView({ onAuthLost, editTarget, onEditConsumed }: Props) {
  const composerRef = useRef<ComposerHandle>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const [editingPostId, setEditingPostId] = useState<string | null>(null);
  const [draftsOpen, setDraftsOpen] = useState(false);
  const [draftsCount, setDraftsCount] = useState(0);
  // Bumped on draft mutations and drawer open so DraftsView refetches.
  const [draftsRefresh, setDraftsRefresh] = useState(0);

  // Lightweight count fetch so the toolbar pill can render even when the
  // drawer is closed. The drawer's own list reload uses draftsRefresh.
  const refreshDraftsCount = useCallback(async () => {
    try {
      const drafts = await listDrafts();
      setDraftsCount(drafts.length);
    } catch (e) {
      if (e instanceof Unauthorized) onAuthLost();
      // Otherwise silent — the count is non-critical chrome.
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

  function handleDeleted(id: string) {
    if (editingPostId === id) composerRef.current?.reset();
    setRefreshToken((t) => t + 1);
  }

  // Stable identity so Composer's onTargetChange effect doesn't re-fire
  // every render of HomeView.
  const handleTargetChange = useCallback((t: EditTarget | null) => {
    setEditingPostId(t?.kind === "post" ? t.id : null);
  }, []);

  function handleDraftSaved() {
    setDraftsRefresh((n) => n + 1);
    refreshDraftsCount();
  }

  function handleSubmitted() {
    setRefreshToken((t) => t + 1);
    // Publishing a draft removes it; refresh the count.
    refreshDraftsCount();
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
        onTargetChange={handleTargetChange}
        onAuthLost={onAuthLost}
        draftsCount={draftsCount}
        onOpenDrafts={openDrafts}
      />
      <PostList
        refreshToken={refreshToken}
        editingPostId={editingPostId}
        onEdit={startEditPost}
        onDeleted={handleDeleted}
        onAuthLost={onAuthLost}
      />
      <DraftsDrawer
        open={draftsOpen}
        onOpenChange={setDraftsOpen}
        onAuthLost={onAuthLost}
        onEdit={startEditDraft}
        refreshKey={draftsRefresh}
        onChanged={refreshDraftsCount}
      />
    </div>
  );
}
