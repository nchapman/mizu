import { useEffect, useState } from "react";

type Post = {
  id: string;
  title?: string;
  date: string;
  tags?: string[];
  body: string;
  path: string;
};

export function App() {
  const [posts, setPosts] = useState<Post[]>([]);
  const [body, setBody] = useState("");
  const [title, setTitle] = useState("");
  const [showTitle, setShowTitle] = useState(false);
  const [posting, setPosting] = useState(false);

  async function load() {
    const r = await fetch("/admin/api/posts");
    setPosts(await r.json());
  }
  useEffect(() => {
    load();
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!body.trim()) return;
    setPosting(true);
    try {
      const r = await fetch("/admin/api/posts", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ title: showTitle ? title : "", body }),
      });
      if (!r.ok) throw new Error(await r.text());
      setBody("");
      setTitle("");
      setShowTitle(false);
      await load();
    } finally {
      setPosting(false);
    }
  }

  return (
    <div style={{ font: "15px/1.5 system-ui", maxWidth: 640, margin: "2em auto", padding: "0 1em" }}>
      <h1 style={{ fontSize: "1.2em" }}>repeat</h1>

      <form onSubmit={submit} style={{ marginBottom: "2em", border: "1px solid #ddd", borderRadius: 8, padding: "1em" }}>
        {showTitle && (
          <input
            placeholder="Title"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            style={{ width: "100%", marginBottom: ".5em", padding: ".4em", fontSize: "1em" }}
          />
        )}
        <textarea
          placeholder="What's on your mind?"
          value={body}
          onChange={(e) => setBody(e.target.value)}
          rows={showTitle ? 8 : 3}
          style={{ width: "100%", padding: ".4em", fontSize: "1em", resize: "vertical" }}
        />
        <div style={{ display: "flex", justifyContent: "space-between", marginTop: ".5em" }}>
          <button type="button" onClick={() => setShowTitle((v) => !v)} style={{ background: "none", border: "none", color: "#666", cursor: "pointer" }}>
            {showTitle ? "− title" : "+ title"}
          </button>
          <button type="submit" disabled={posting || !body.trim()}>
            {posting ? "Posting…" : "Post"}
          </button>
        </div>
      </form>

      {posts.map((p) => (
        <article key={p.id} style={{ marginBottom: "1.5em", paddingBottom: "1em", borderBottom: "1px solid #eee" }}>
          <div style={{ color: "#888", fontSize: ".85em" }}>
            <a href={p.path} style={{ color: "inherit" }}>
              {new Date(p.date).toLocaleString()}
            </a>
          </div>
          {p.title && <h2 style={{ margin: ".2em 0", fontSize: "1.1em" }}>{p.title}</h2>}
          <div style={{ whiteSpace: "pre-wrap" }}>{p.body}</div>
        </article>
      ))}
    </div>
  );
}
