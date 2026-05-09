import { useEffect, useState } from "react";
import { Unauthorized, api, deletePost, type Post } from "./api";
import { linkBtn } from "./styles";

interface Props {
  refreshToken: number;
  onEdit: (p: Post) => void;
  onDeleted: (id: string) => void;
  onAuthLost: () => void;
  // True when a sibling component (the composer) is currently editing
  // this post — used to dim the row and disable its edit button.
  editingPostId: string | null;
}

export function PostList({ refreshToken, onEdit, onDeleted, onAuthLost, editingPostId }: Props) {
  const [posts, setPosts] = useState<Post[]>([]);
  const [err, setErr] = useState("");

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const list = await api<Post[]>("/admin/api/posts");
        if (!cancelled) {
          setPosts(list);
          setErr("");
        }
      } catch (e) {
        if (cancelled) return;
        if (e instanceof Unauthorized) return onAuthLost();
        setErr((e as Error).message);
      }
    })();
    return () => { cancelled = true; };
  }, [refreshToken, onAuthLost]);

  async function remove(p: Post) {
    const label = p.title || p.body.slice(0, 40);
    if (!confirm(`Delete "${label}"?`)) return;
    setErr("");
    try {
      await deletePost(p.id);
      onDeleted(p.id);
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    }
  }

  return (
    <>
      {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: "1em" }}>{err}</div>}
      {posts.map((p) => (
        <PostRow
          key={p.id}
          post={p}
          isEditing={editingPostId === p.id}
          onEdit={() => onEdit(p)}
          onDelete={() => remove(p)}
        />
      ))}
    </>
  );
}

interface RowProps {
  post: Post;
  isEditing: boolean;
  onEdit: () => void;
  onDelete: () => void;
}

function PostRow({ post, isEditing, onEdit, onDelete }: RowProps) {
  return (
    <article
      style={{
        marginBottom: "1.5em",
        paddingBottom: "1em",
        borderBottom: "1px solid #eee",
        opacity: isEditing ? 0.5 : 1,
      }}
    >
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline", gap: "1em" }}>
        <div style={{ color: "#888", fontSize: ".85em" }}>
          <a href={post.path} style={{ color: "inherit" }}>
            {new Date(post.date).toLocaleString()}
          </a>
        </div>
        <div style={{ display: "flex", gap: ".5em", fontSize: ".85em" }}>
          <button type="button" onClick={onEdit} style={linkBtn} disabled={isEditing}>
            edit
          </button>
          <button type="button" onClick={onDelete} style={{ ...linkBtn, color: "#b00" }}>
            delete
          </button>
        </div>
      </div>
      {post.title && <h2 style={{ margin: ".2em 0", fontSize: "1.1em" }}>{post.title}</h2>}
      <div className="post-rendered" dangerouslySetInnerHTML={{ __html: post.html }} />
    </article>
  );
}
