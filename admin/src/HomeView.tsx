import { useCallback, useEffect, useRef, useState } from "react";
import { Composer, type ComposerHandle } from "./Composer";
import { PostList } from "./PostList";
import type { EditTarget } from "./Shell";
import type { Post } from "./api";

interface Props {
  onAuthLost: () => void;
  // One-shot edit handoff from a sibling tab (e.g. Drafts → composer).
  // HomeView forwards into the composer's imperative load() and acks via
  // onEditConsumed.
  editTarget: EditTarget | null;
  onEditConsumed: () => void;
}

export function HomeView({ onAuthLost, editTarget, onEditConsumed }: Props) {
  const composerRef = useRef<ComposerHandle>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const [editingPostId, setEditingPostId] = useState<string | null>(null);

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

  function handleDeleted(id: string) {
    if (editingPostId === id) composerRef.current?.reset();
    setRefreshToken((t) => t + 1);
  }

  // Stable identity so Composer's onTargetChange effect doesn't re-fire
  // every render of HomeView.
  const handleTargetChange = useCallback((t: EditTarget | null) => {
    setEditingPostId(t?.kind === "post" ? t.id : null);
  }, []);

  return (
    <div>
      <Composer
        ref={composerRef}
        onSubmitted={() => setRefreshToken((t) => t + 1)}
        // No refreshToken bump: saving a draft doesn't change the published list.
        onDraftSaved={() => {}}
        onTargetChange={handleTargetChange}
        onAuthLost={onAuthLost}
      />
      <PostList
        refreshToken={refreshToken}
        editingPostId={editingPostId}
        onEdit={startEditPost}
        onDeleted={handleDeleted}
        onAuthLost={onAuthLost}
      />
    </div>
  );
}
