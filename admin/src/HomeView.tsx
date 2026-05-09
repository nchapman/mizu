import { useEffect, useRef, useState } from "react";
import { Composer, type ComposerHandle } from "./Composer";
import { PostList } from "./PostList";
import type { EditTarget } from "./Shell";
import type { Post } from "./api";

interface Props {
  onAuthLost: () => void;
  // One-shot edit handoff from a sibling tab (e.g. Drafts → composer).
  // HomeView mirrors this into local state and acks via onEditConsumed.
  editTarget: EditTarget | null;
  onEditConsumed: () => void;
}

export function HomeView({ onAuthLost, editTarget, onEditConsumed }: Props) {
  const [target, setTarget] = useState<EditTarget | null>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const composerRef = useRef<ComposerHandle>(null);

  useEffect(() => {
    if (!editTarget) return;
    setTarget(editTarget);
    onEditConsumed();
  }, [editTarget, onEditConsumed]);

  function startEditPost(p: Post) {
    setTarget({ kind: "post", id: p.id, title: p.title ?? "", body: p.body });
  }

  function handleDeleted(id: string) {
    // Clear target first so the composer's next render sees null labels;
    // reset() then wipes form state and the loadedTargetRef.
    if (target?.kind === "post" && target.id === id) {
      setTarget(null);
      composerRef.current?.reset();
    }
    setRefreshToken((t) => t + 1);
  }

  return (
    <div>
      <Composer
        ref={composerRef}
        target={target}
        onSubmitted={() => {
          setTarget(null);
          setRefreshToken((t) => t + 1);
        }}
        // No refreshToken bump: saving a draft doesn't change the published list.
        onDraftSaved={() => setTarget(null)}
        onCancel={() => setTarget(null)}
        onAuthLost={onAuthLost}
      />
      <PostList
        refreshToken={refreshToken}
        editingPostId={target?.kind === "post" ? target.id : null}
        onEdit={startEditPost}
        onDeleted={handleDeleted}
        onAuthLost={onAuthLost}
      />
    </div>
  );
}
