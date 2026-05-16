// MarkdownEditor — a Lexical-backed prose editor that produces and
// consumes Markdown. Markdown stays the source of truth: the parent
// passes `initialValue` (a Markdown string), receives every edit via
// `onChange(markdown)`, and gets to decide when to bump the `key` on
// this component to force a re-init from a new value (e.g. when an
// edit-target loads from another tab, or after resetComposer).
//
// Image paste/drop is delegated to the parent via `onUploadImages`,
// which uploads the files and returns Markdown image-syntax strings;
// the editor parses them through the IMAGE transformer so they render
// inline as <img>. ImageNodes serialize back to `![alt](url)`.

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
  $insertNodes,
  COMMAND_PRIORITY_LOW,
  PASTE_COMMAND,
  DROP_COMMAND,
} from "lexical";

import {
  $createImageNode,
  IMAGE_TRANSFORMER,
  ImageNode,
} from "@/lex/ImageNode";
import { Toolbar } from "@/lex/Toolbar";

const ALL_TRANSFORMERS = [IMAGE_TRANSFORMER, ...TRANSFORMERS];

export interface MarkdownEditorHandle {
  // insertText drops a literal string at the caret. The string is NOT
  // re-parsed as Markdown — it lands as plain text in the rich tree.
  insertText(text: string): void;
  // insertImage inserts an inline ImageNode at the caret. Use this for
  // uploaded images so they render as <img> rather than `![…](…)` text.
  insertImage(src: string, altText: string): void;
  focus(): void;
}

interface Props {
  initialValue: string;
  onChange: (markdown: string) => void;
  // Returns one {src, alt} per uploaded file; the editor inserts an
  // ImageNode at the current selection for each. Errors should be
  // handled by the parent.
  onUploadImages?: (files: File[]) => Promise<UploadedImage[]>;
  placeholder?: string;
  minHeight?: string;
  // When true, render the formatting toolbar above the content area.
  showToolbar?: boolean;
}

export interface UploadedImage {
  src: string;
  altText: string;
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
          onChange($convertToMarkdownString(ALL_TRANSFORMERS));
        });
      }}
    />
  );
}

function PasteAndDropBridge({
  onUploadImages,
}: {
  onUploadImages?: (files: File[]) => Promise<UploadedImage[]>;
}) {
  const [editor] = useLexicalComposerContext();

  useEffect(() => {
    if (!onUploadImages) return;

    const insertImages = (uploads: UploadedImage[]) => {
      if (uploads.length === 0) return;
      // Focus first so a selection exists; otherwise $insertNodes silently
      // drops the images when the editor has never been focused (e.g. user
      // hits the upload button without clicking into the editor).
      editor.focus(() => {
        editor.update(() => {
          $insertNodes(uploads.map((u) => $createImageNode(u.src, u.altText)));
        });
      });
    };

    const handleFiles = (files: File[]) => {
      const images = files.filter((f) => f.type.startsWith("image/"));
      if (images.length === 0) return false;
      void onUploadImages(images).then(insertImages);
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
      insertImage(src: string, altText: string) {
        // Focus first so $insertNodes has a selection to anchor to —
        // otherwise the call no-ops when the editor isn't already focused.
        editor.focus(() => {
          editor.update(() => {
            $insertNodes([$createImageNode(src, altText)]);
          });
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
  { initialValue, onChange, onUploadImages, placeholder, minHeight, showToolbar },
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
        ImageNode,
      ],
      onError(err: Error) {
        console.error("lexical:", err);
      },
      editorState: () => $convertFromMarkdownString(initialValue, ALL_TRANSFORMERS),
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  return (
    <LexicalComposer initialConfig={initialConfig}>
        <div className="lex-editor-shell">
          {showToolbar && <Toolbar />}
          <div className="lex-content-wrap">
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
          </div>
          <HistoryPlugin />
          <ListPlugin />
          <LinkPlugin />
          <MarkdownShortcutPlugin transformers={ALL_TRANSFORMERS} />
          <ChangeBridge onChange={onChange} />
          <PasteAndDropBridge onUploadImages={onUploadImages} />
          <ImperativeBridge handleRef={ref} />
        </div>
      </LexicalComposer>
  );
});
