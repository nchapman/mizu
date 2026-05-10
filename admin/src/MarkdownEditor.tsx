// MarkdownEditor — a Lexical-backed prose editor that produces and
// consumes Markdown. Markdown stays the source of truth: the parent
// passes `initialValue` (a Markdown string), receives every edit via
// `onChange(markdown)`, and gets to decide when to bump the `key` on
// this component to force a re-init from a new value (e.g. when an
// edit-target loads from another tab, or after resetComposer).
//
// Image paste/drop is delegated to the parent via `onUploadImages`,
// which uploads the files and returns Markdown image-syntax strings;
// the editor inserts them at the caret. We don't register a custom
// ImageNode for the prototype, so the inserted text remains as
// `![alt](url)` in rich mode rather than rendering inline — but it
// roundtrips through the markdown serializer cleanly.

import { forwardRef, useEffect, useImperativeHandle, useMemo } from "react";
import type { Ref } from "react";
import "./MarkdownEditor.css";
import { LexicalComposer } from "@lexical/react/LexicalComposer";
import { RichTextPlugin } from "@lexical/react/LexicalRichTextPlugin";
import { ContentEditable } from "@lexical/react/LexicalContentEditable";
import { HistoryPlugin } from "@lexical/react/LexicalHistoryPlugin";
import { LinkPlugin } from "@lexical/react/LexicalLinkPlugin";
import { ListPlugin } from "@lexical/react/LexicalListPlugin";
import { MarkdownShortcutPlugin } from "@lexical/react/LexicalMarkdownShortcutPlugin";
import { OnChangePlugin } from "@lexical/react/LexicalOnChangePlugin";
import { LexicalErrorBoundary } from "@lexical/react/LexicalErrorBoundary";
import { useLexicalComposerContext } from "@lexical/react/LexicalComposerContext";
import {
  TRANSFORMERS,
  $convertFromMarkdownString,
  $convertToMarkdownString,
} from "@lexical/markdown";
import { HeadingNode, QuoteNode } from "@lexical/rich-text";
import { ListNode, ListItemNode } from "@lexical/list";
import { LinkNode, AutoLinkNode } from "@lexical/link";
import { CodeNode, CodeHighlightNode } from "@lexical/code";
import {
  $getSelection,
  $isRangeSelection,
  COMMAND_PRIORITY_LOW,
  PASTE_COMMAND,
  DROP_COMMAND,
} from "lexical";

export interface MarkdownEditorHandle {
  // insertText drops a literal string at the caret. The string is NOT
  // re-parsed as Markdown — it lands as plain text in the rich tree.
  // Used today for inline `![alt](url)` after image upload, which
  // roundtrips correctly through the Markdown serializer even though
  // it does not render as an image in rich mode.
  insertText(text: string): void;
  focus(): void;
}

interface Props {
  initialValue: string;
  onChange: (markdown: string) => void;
  // Returns one Markdown snippet per uploaded file; the editor inserts
  // them at the current selection. Errors should be handled by the
  // parent (which owns the uploading/error UI).
  onUploadImages?: (files: File[]) => Promise<string[]>;
  placeholder?: string;
  minHeight?: string;
}

const editorTheme = {
  paragraph: "lex-p",
  heading: { h1: "lex-h1", h2: "lex-h2", h3: "lex-h3", h4: "lex-h4" },
  list: { ul: "lex-ul", ol: "lex-ol", listitem: "lex-li" },
  link: "lex-link",
  text: {
    bold: "lex-bold",
    italic: "lex-italic",
    code: "lex-code-inline",
  },
  quote: "lex-quote",
  code: "lex-code-block",
};

function ChangeBridge({ onChange }: { onChange: (md: string) => void }) {
  return (
    <OnChangePlugin
      onChange={(state) => {
        state.read(() => {
          onChange($convertToMarkdownString(TRANSFORMERS));
        });
      }}
    />
  );
}

function PasteAndDropBridge({
  onUploadImages,
}: {
  onUploadImages?: (files: File[]) => Promise<string[]>;
}) {
  const [editor] = useLexicalComposerContext();

  useEffect(() => {
    if (!onUploadImages) return;

    const insertSnippets = (snippets: string[]) => {
      if (snippets.length === 0) return;
      editor.update(() => {
        const sel = $getSelection();
        if ($isRangeSelection(sel)) sel.insertText(snippets.join(""));
      });
    };

    const handleFiles = (files: File[]) => {
      const images = files.filter((f) => f.type.startsWith("image/"));
      if (images.length === 0) return false;
      void onUploadImages(images).then(insertSnippets);
      return true;
    };

    const unPaste = editor.registerCommand(
      PASTE_COMMAND,
      (event) => {
        if (!(event instanceof ClipboardEvent)) return false;
        const files = Array.from(event.clipboardData?.files ?? []);
        if (!handleFiles(files)) return false;
        event.preventDefault();
        return true;
      },
      COMMAND_PRIORITY_LOW,
    );
    const unDrop = editor.registerCommand(
      DROP_COMMAND,
      (event) => {
        const files = Array.from(event.dataTransfer?.files ?? []);
        if (!handleFiles(files)) return false;
        event.preventDefault();
        return true;
      },
      COMMAND_PRIORITY_LOW,
    );
    return () => {
      unPaste();
      unDrop();
    };
  }, [editor, onUploadImages]);

  return null;
}

function ImperativeBridge({
  handleRef,
}: {
  handleRef: Ref<MarkdownEditorHandle>;
}) {
  const [editor] = useLexicalComposerContext();
  useImperativeHandle(
    handleRef,
    () => ({
      insertText(text: string) {
        editor.update(() => {
          const sel = $getSelection();
          if ($isRangeSelection(sel)) sel.insertText(text);
        });
      },
      focus() {
        editor.focus();
      },
    }),
    [editor],
  );
  return null;
}

export const MarkdownEditor = forwardRef<MarkdownEditorHandle, Props>(function MarkdownEditor(
  { initialValue, onChange, onUploadImages, placeholder, minHeight },
  ref,
) {
  // LexicalComposer reads initialConfig once on mount, so the only
  // value that needs stable identity here is `editorState`. The parent
  // forces a fresh mount via `key` when initialValue changes from
  // outside, so capturing the prop in the factory closure is safe.
  const initialConfig = useMemo(
    () => ({
      namespace: "mizu-composer",
      theme: editorTheme,
      nodes: [
        HeadingNode,
        QuoteNode,
        ListNode,
        ListItemNode,
        LinkNode,
        AutoLinkNode,
        CodeNode,
        CodeHighlightNode,
      ],
      onError(err: Error) {
        console.error("lexical:", err);
      },
      editorState: () => $convertFromMarkdownString(initialValue, TRANSFORMERS),
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  return (
    <LexicalComposer initialConfig={initialConfig}>
        <div className="lex-editor-shell">
          <RichTextPlugin
            contentEditable={
              <ContentEditable
                className="lex-content"
                style={{ minHeight: minHeight ?? "8em" }}
              />
            }
            placeholder={
              placeholder ? (
                <div className="lex-placeholder">{placeholder}</div>
              ) : null
            }
            ErrorBoundary={LexicalErrorBoundary}
          />
          <HistoryPlugin />
          <ListPlugin />
          <LinkPlugin />
          <MarkdownShortcutPlugin transformers={TRANSFORMERS} />
          <ChangeBridge onChange={onChange} />
          <PasteAndDropBridge onUploadImages={onUploadImages} />
          <ImperativeBridge handleRef={ref} />
        </div>
      </LexicalComposer>
  );
});
