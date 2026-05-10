import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from "react";
import { flushSync } from "react-dom";
import { Code, FileText, Heading, ImagePlus, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

import {
  Unauthorized,
  api,
  createDraft,
  publishDraft,
  updateDraft,
  updatePost,
  uploadMedia,
} from "@/api";
import { MarkdownEditor, type MarkdownEditorHandle } from "@/MarkdownEditor";
import type { EditTarget } from "@/Shell";
import { cn } from "@/lib/utils";

type EditorMode = "rich" | "md";

export interface ComposerHandle {
  load: (target: EditTarget) => void;
  reset: () => void;
  // prefill seeds a brand-new post with body content (e.g. a quoted feed
  // item) and optionally surfaces an "Inspired by" pill so the operator
  // sees the provenance while writing. No edit target is set.
  prefill: (body: string, inspiration?: Inspiration) => void;
}

export interface Inspiration {
  feedTitle: string;
}

interface Props {
  onSubmitted: () => void;
  onDraftSaved: () => void;
  onAuthLost: () => void;
  // Fires whenever the composer's editing target changes (load, reset,
  // submit). Currently unused by HomeView; kept as a hook for future
  // surfaces (e.g. dimming the corresponding stream card while editing).
  onTargetChange?: (target: EditTarget | null) => void;
  // Optional: a count + opener for the drafts drawer pill in the toolbar.
  draftsCount?: number;
  onOpenDrafts?: () => void;
}

export const Composer = forwardRef<ComposerHandle, Props>(function Composer(
  { onSubmitted, onDraftSaved, onAuthLost, onTargetChange, draftsCount, onOpenDrafts },
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
  // React batch as setBody and setEditorKey — if these arrived through a
  // prop+useEffect path, the still-mounted Lexical editor would get an
  // intermediate render where its update listener fires onChange("") and
  // clobbers the just-set body.
  const [target, setTarget] = useState<EditTarget | null>(null);
  const [inspiration, setInspiration] = useState<Inspiration | null>(null);

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
    setInspiration(null);
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
    setInspiration(null);
    setEditorKey((k) => k + 1);
    setFocusTick((n) => n + 1);
  }, []);

  const prefill = useCallback((nextBody: string, source?: Inspiration) => {
    setTarget(null);
    setBody(nextBody);
    setTitle("");
    setShowTitle(false);
    setErr("");
    setInspiration(source ?? null);
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

  useImperativeHandle(ref, () => ({ load, reset, prefill }), [load, reset, prefill]);

  // Inserts text at the textarea's caret. flushSync forces React to commit
  // the new body synchronously so we can move the caret in the same tick.
  // Functional setState is critical: sequential calls in a loop must each
  // see the latest body, not a stale snapshot.
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

  // Flipping into rich means the editor has to re-init from the current
  // (possibly md-edited) body. Flipping into md is just a render swap —
  // body is already current.
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
        // Capture latest edits before publishing so they aren't lost if
        // the publish call partially fails downstream.
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

  // Save Draft is only meaningful when not editing a published post. When
  // editing an existing draft it updates in place; otherwise it creates a
  // new one.
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

  const submitLabel = posting
    ? "Saving…"
    : target?.kind === "post"
    ? "Save"
    : target?.kind === "draft"
    ? "Publish"
    : "Post";

  const draftLabel = target?.kind === "draft" ? "save draft" : "draft";

  return (
    <TooltipProvider delayDuration={250}>
      <form
        onSubmit={submit}
        className="mb-6 rounded-xl border border-border bg-card p-4 shadow-sm"
      >
        {inspiration && (
          <div
            role="status"
            className="mb-3 flex items-center justify-between gap-2 rounded-md bg-accent/40 px-3 py-1.5 text-xs text-muted-foreground"
          >
            <span className="truncate">
              Inspired by: <span className="font-medium text-foreground/80">{inspiration.feedTitle}</span>
            </span>
            <button
              type="button"
              onClick={() => setInspiration(null)}
              className="text-muted-foreground hover:text-foreground"
              aria-label="Clear inspiration"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>
        )}
        {showTitle && (
          <Input
            placeholder="Title"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            className="mb-3 text-base"
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
            onDragOver={(e) => {
              e.preventDefault();
              setDragActive(true);
            }}
            onDragLeave={() => setDragActive(false)}
            rows={showTitle ? 8 : 3}
            className={cn(
              "w-full resize-y rounded-md border border-input bg-transparent px-3 py-2 text-sm font-mono shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring",
              dragActive && "outline outline-2 outline-dashed outline-primary",
            )}
          />
        )}
        <input
          ref={fileInputRef}
          type="file"
          accept="image/*"
          multiple
          className="hidden"
          onChange={(e) => {
            const files = Array.from(e.target.files ?? []);
            e.target.value = "";
            if (mode === "rich") void uploadFilesRich(files);
            else void uploadFilesMd(files);
          }}
        />
        <div className="mt-3 flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-1">
            <ToolbarButton
              label={showTitle ? "Hide title" : "Add title"}
              onClick={() => setShowTitle((v) => !v)}
              ariaName="title"
            >
              <Heading />
            </ToolbarButton>
            <ToolbarButton
              label={uploading ? "Uploading…" : "Add image"}
              onClick={() => fileInputRef.current?.click()}
              disabled={uploading}
              ariaName="image"
            >
              <ImagePlus />
            </ToolbarButton>
            <ToolbarButton
              label={mode === "rich" ? "Edit raw Markdown source" : "Back to rich editor"}
              ariaName="source"
              onClick={() => setEditorMode(mode === "rich" ? "md" : "rich")}
              active={mode === "md"}
            >
              <Code />
            </ToolbarButton>
            {onOpenDrafts && draftsCount !== undefined && draftsCount > 0 && (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={onOpenDrafts}
                className="text-muted-foreground"
              >
                <FileText />
                Drafts · {draftsCount}
              </Button>
            )}
          </div>
          <div className="flex items-center gap-2">
            {target && (
              <Button type="button" variant="ghost" size="sm" onClick={reset}>
                cancel
              </Button>
            )}
            {target?.kind !== "post" && (
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={saveDraft}
                disabled={posting || uploading || !body.trim()}
              >
                {draftLabel}
              </Button>
            )}
            <Button type="submit" size="sm" disabled={posting || uploading || !body.trim()}>
              {submitLabel}
            </Button>
          </div>
        </div>
        {err && (
          <div role="alert" className="mt-3 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {err}
          </div>
        )}
      </form>
    </TooltipProvider>
  );
});

function ToolbarButton({
  label,
  ariaName,
  onClick,
  disabled,
  active,
  children,
}: {
  label: string;
  // Stable accessible name surfaced via aria-label so tests and assistive
  // tech don't depend on the icon's tooltip.
  ariaName: string;
  onClick: () => void;
  disabled?: boolean;
  // True when the button represents a currently-engaged mode (e.g. source
  // editor active). Renders a subtle filled background as feedback.
  active?: boolean;
  children: React.ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={ariaName}
          aria-pressed={active}
          onClick={onClick}
          disabled={disabled}
          className={cn(
            "h-8 w-8 text-muted-foreground",
            active && "bg-accent text-foreground",
          )}
        >
          {children}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}
