import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from "react";
import { flushSync } from "react-dom";
import {
  Unauthorized,
  api,
  createDraft,
  publishDraft,
  updateDraft,
  updatePost,
  uploadMedia,
} from "./api";
import { MarkdownEditor, type MarkdownEditorHandle } from "./MarkdownEditor";
import type { EditTarget } from "./Shell";
import { linkBtn } from "./styles";

type EditorMode = "rich" | "md";

export interface ComposerHandle {
  reset: () => void;
}

interface Props {
  // When non-null, the composer loads this content into the form. The
  // parent keeps target set throughout the edit so labels reflect what's
  // being edited; clearing it (and calling reset()) returns the composer
  // to "new post" mode.
  target: EditTarget | null;
  onSubmitted: () => void;
  onDraftSaved: () => void;
  onCancel: () => void;
  onAuthLost: () => void;
}

export const Composer = forwardRef<ComposerHandle, Props>(function Composer(
  { target, onSubmitted, onDraftSaved, onCancel, onAuthLost },
  ref,
) {
  const [body, setBody] = useState("");
  const [title, setTitle] = useState("");
  const [showTitle, setShowTitle] = useState(false);
  const [posting, setPosting] = useState(false);
  const [err, setErr] = useState("");
  const [uploading, setUploading] = useState(false);
  const [dragActive, setDragActive] = useState(false);
  const [mode, setMode] = useState<EditorMode>("rich");
  // editorKey forces the rich Lexical editor to re-init from a fresh
  // initialValue. Bumped on reset, target loads, and mode flips into
  // rich — anywhere the body changes from outside the editor's own typing.
  const [editorKey, setEditorKey] = useState(0);

  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const editorRef = useRef<MarkdownEditorHandle>(null);
  // Tracks which target id is currently loaded so the effect doesn't
  // reload (and clobber edits) when target identity changes but the
  // logical pointer is the same.
  const loadedTargetRef = useRef<string | null>(null);

  function clearForm() {
    setBody("");
    setTitle("");
    setShowTitle(false);
    setErr("");
    setEditorKey((k) => k + 1);
  }

  const reset = useCallback(() => {
    loadedTargetRef.current = null;
    clearForm();
  }, []);
  useImperativeHandle(ref, () => ({ reset }), [reset]);

  // Load the target into the form when it changes. Stays a no-op while
  // target is null on initial mount or after an organic clear, so user
  // typing isn't wiped. The id-vs-loadedTargetRef guard does double duty:
  // it suppresses re-loads on a mode flip (same target id), and it also
  // suppresses re-loads if the parent re-sets the same target while it's
  // already loaded. The latter is only safe because PostList's edit
  // button is disabled while that post is being edited — if that
  // invariant ever changes, this guard would silently skip a focus/scroll
  // the user expects.
  useEffect(() => {
    if (!target) {
      loadedTargetRef.current = null;
      return;
    }
    const id = `${target.kind}:${target.id}`;
    if (id === loadedTargetRef.current) return;
    loadedTargetRef.current = id;
    setBody(target.body);
    setTitle(target.title);
    setShowTitle(!!target.title);
    setErr("");
    setEditorKey((k) => k + 1);
    queueMicrotask(() => {
      if (mode === "md") textareaRef.current?.focus();
      else editorRef.current?.focus();
      window.scrollTo({ top: 0, behavior: "smooth" });
    });
  }, [target, mode]);

  // Inserts text at the textarea's caret. flushSync forces React to
  // commit the new body synchronously so we can move the caret in the
  // same tick. Functional setState is critical: sequential calls in a
  // loop must each see the latest body, not a stale snapshot.
  function insertAtCaret(text: string) {
    const ta = textareaRef.current;
    let caret = -1;
    flushSync(() => {
      setBody((prev) => {
        const start = ta?.selectionStart ?? prev.length;
        const end = ta?.selectionEnd ?? prev.length;
        caret = start + text.length;
        return prev.slice(0, start) + text + prev.slice(end);
      });
    });
    if (ta && caret >= 0) {
      ta.focus();
      ta.setSelectionRange(caret, caret);
    }
  }

  // Memoized because MarkdownEditor passes this to a useEffect dep list —
  // without useCallback, every keystroke would tear down and re-register
  // the editor's PASTE/DROP command listeners.
  const uploadAndCollect = useCallback(
    async (files: File[]): Promise<string[]> => {
      if (files.length === 0) return [];
      setErr("");
      setUploading(true);
      try {
        const out: string[] = [];
        for (const f of files) {
          const m = await uploadMedia(f);
          const alt = f.name.replace(/\.[^.]+$/, "");
          out.push(`![${alt}](${m.url})\n`);
        }
        return out;
      } catch (e) {
        if (e instanceof Unauthorized) {
          onAuthLost();
          return [];
        }
        setErr((e as Error).message);
        return [];
      } finally {
        setUploading(false);
      }
    },
    [onAuthLost],
  );

  async function uploadFilesMd(files: File[]) {
    const snippets = await uploadAndCollect(files);
    for (const s of snippets) insertAtCaret(s);
  }

  async function uploadFilesRich(files: File[]) {
    const snippets = await uploadAndCollect(files);
    if (snippets.length > 0) editorRef.current?.insertText(snippets.join(""));
  }

  function onPaste(e: React.ClipboardEvent<HTMLTextAreaElement>) {
    const files = Array.from(e.clipboardData.files).filter((f) => f.type.startsWith("image/"));
    if (files.length === 0) return;
    e.preventDefault();
    void uploadFilesMd(files);
  }

  function onDrop(e: React.DragEvent<HTMLTextAreaElement>) {
    e.preventDefault();
    setDragActive(false);
    const files = Array.from(e.dataTransfer.files).filter((f) => f.type.startsWith("image/"));
    if (files.length > 0) void uploadFilesMd(files);
  }

  // Flipping into rich means the editor has to re-init from the
  // current (possibly md-edited) body. Flipping into md is just a
  // render swap — body is already current.
  function setEditorMode(next: EditorMode) {
    setMode((prev) => {
      if (prev !== next && next === "rich") setEditorKey((k) => k + 1);
      return next;
    });
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!body.trim()) return;
    setPosting(true);
    setErr("");
    try {
      const payload = { title: showTitle ? title : "", body };
      if (target?.kind === "post") {
        await updatePost(target.id, payload);
      } else if (target?.kind === "draft") {
        // Capture latest edits before publishing so they aren't lost
        // if the publish call partially fails downstream.
        await updateDraft(target.id, payload);
        await publishDraft(target.id);
      } else {
        await api("/admin/api/posts", { method: "POST", body: JSON.stringify(payload) });
      }
      reset();
      onSubmitted();
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    } finally {
      setPosting(false);
    }
  }

  // Save Draft is only meaningful when not editing a published post.
  // When editing an existing draft it updates in place; otherwise it
  // creates a new one.
  async function saveDraft() {
    if (!body.trim()) return;
    setPosting(true);
    setErr("");
    try {
      const payload = { title: showTitle ? title : "", body };
      if (target?.kind === "draft") {
        await updateDraft(target.id, payload);
      } else {
        await createDraft(payload);
      }
      reset();
      onDraftSaved();
    } catch (e) {
      if (e instanceof Unauthorized) return onAuthLost();
      setErr((e as Error).message);
    } finally {
      setPosting(false);
    }
  }

  function handleCancel() {
    reset();
    onCancel();
  }

  const submitLabel = posting
    ? "Saving…"
    : target?.kind === "post"
    ? "Save"
    : target?.kind === "draft"
    ? "Publish"
    : "Post";

  const draftLabel = target?.kind === "draft" ? "save draft" : "+ draft";

  return (
    <>
      <form onSubmit={submit} style={{ marginBottom: "2em", border: "1px solid #ddd", borderRadius: 8, padding: "1em" }}>
        {showTitle && (
          <input
            placeholder="Title"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            style={{ width: "100%", marginBottom: ".5em", padding: ".4em", fontSize: "1em" }}
          />
        )}
        {mode === "rich" ? (
          <MarkdownEditor
            key={editorKey}
            ref={editorRef}
            initialValue={body}
            onChange={setBody}
            onUploadImages={uploadAndCollect}
            placeholder="What's on your mind? (paste or drop images to upload)"
            minHeight={showTitle ? "12em" : "5em"}
          />
        ) : (
          <textarea
            ref={textareaRef}
            placeholder="What's on your mind? (paste or drop images to upload)"
            value={body}
            onChange={(e) => setBody(e.target.value)}
            onPaste={onPaste}
            onDrop={onDrop}
            onDragOver={(e) => { e.preventDefault(); setDragActive(true); }}
            onDragLeave={() => setDragActive(false)}
            rows={showTitle ? 8 : 3}
            style={{
              width: "100%", padding: ".4em", fontSize: "1em", fontFamily: "ui-monospace, Menlo, monospace",
              resize: "vertical",
              outline: dragActive ? "2px dashed #4a90e2" : undefined,
            }}
          />
        )}
        <input
          ref={fileInputRef}
          type="file"
          accept="image/*"
          multiple
          style={{ display: "none" }}
          onChange={(e) => {
            const files = Array.from(e.target.files ?? []);
            e.target.value = "";
            if (mode === "rich") void uploadFilesRich(files);
            else void uploadFilesMd(files);
          }}
        />
        <div style={{ display: "flex", justifyContent: "space-between", marginTop: ".5em" }}>
          <div style={{ display: "flex", gap: ".5em", alignItems: "center" }}>
            <button type="button" onClick={() => setShowTitle((v) => !v)} style={linkBtn}>
              {showTitle ? "− title" : "+ title"}
            </button>
            <button type="button" onClick={() => fileInputRef.current?.click()} style={linkBtn} disabled={uploading}>
              {uploading ? "uploading…" : "+ image"}
            </button>
            <button
              type="button"
              onClick={() => setEditorMode(mode === "rich" ? "md" : "rich")}
              style={linkBtn}
              title={mode === "rich" ? "Edit raw Markdown source" : "Back to rich editor"}
            >
              {mode === "rich" ? "source" : "rich"}
            </button>
          </div>
          <div style={{ display: "flex", gap: ".5em", alignItems: "center" }}>
            {target && (
              <button type="button" onClick={handleCancel} style={linkBtn}>
                cancel
              </button>
            )}
            {target?.kind !== "post" && (
              <button
                type="button"
                onClick={saveDraft}
                disabled={posting || uploading || !body.trim()}
                style={linkBtn}
              >
                {draftLabel}
              </button>
            )}
            <button type="submit" disabled={posting || uploading || !body.trim()}>
              {submitLabel}
            </button>
          </div>
        </div>
      </form>

      {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: "1em" }}>{err}</div>}
    </>
  );
});
