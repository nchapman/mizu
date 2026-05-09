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
  load: (target: EditTarget) => void;
  reset: () => void;
}

interface Props {
  onSubmitted: () => void;
  onDraftSaved: () => void;
  onAuthLost: () => void;
  // Fires whenever the composer's editing target changes (load, reset,
  // submit). The parent uses this to dim the row in PostList and to
  // know which post a delete-while-editing affects.
  onTargetChange?: (target: EditTarget | null) => void;
}

export const Composer = forwardRef<ComposerHandle, Props>(function Composer(
  { onSubmitted, onDraftSaved, onAuthLost, onTargetChange },
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
  // The composer's current editing target. Drives the submit/save-draft
  // labels and the cancel button visibility. Mutated only via the
  // imperative load() / reset() handle so its update lands in the same
  // React batch as setBody and setEditorKey — if these arrived through
  // a prop+useEffect path, the still-mounted Lexical editor would get
  // an intermediate render where its update listener fires onChange("")
  // and clobbers the just-set body.
  const [target, setTarget] = useState<EditTarget | null>(null);

  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const editorRef = useRef<MarkdownEditorHandle>(null);

  useEffect(() => {
    onTargetChange?.(target);
  }, [target, onTargetChange]);

  const reset = useCallback(() => {
    setTarget(null);
    setBody("");
    setTitle("");
    setShowTitle(false);
    setErr("");
    setEditorKey((k) => k + 1);
  }, []);

  // Each load() bumps focusTick; an effect keyed on it focuses+scrolls
  // AFTER the new editor instance has mounted. Doing this synchronously
  // in load() (e.g. via queueMicrotask) would focus the still-mounted
  // OLD editor, whose selection-change update listener then fires
  // onChange("") and clobbers the body we just set.
  const [focusTick, setFocusTick] = useState(0);

  const load = useCallback((t: EditTarget) => {
    setTarget(t);
    setBody(t.body);
    setTitle(t.title);
    setShowTitle(!!t.title);
    setErr("");
    setEditorKey((k) => k + 1);
    setFocusTick((n) => n + 1);
  }, []);

  useEffect(() => {
    if (focusTick === 0) return;
    if (mode === "md") textareaRef.current?.focus();
    else editorRef.current?.focus();
    window.scrollTo({ top: 0, behavior: "smooth" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusTick]);

  useImperativeHandle(ref, () => ({ load, reset }), [load, reset]);

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

  // Cancel just clears the form; reset() also nulls target which fires
  // onTargetChange so the dimmed PostList row brightens.

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
              <button type="button" onClick={reset} style={linkBtn}>
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
