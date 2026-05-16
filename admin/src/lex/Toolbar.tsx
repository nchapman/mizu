// Inline formatting toolbar for the rich Lexical editor. Hidden by
// default; the parent (Composer) toggles visibility via a "format"
// button. Operations are intentionally narrow: bold, italic, link,
// h1/h2, bulleted/numbered list. Anything more elaborate (block quote,
// code, alignment) belongs in raw Markdown mode.

import { useCallback, useEffect, useRef, useState } from "react";
import {
  Bold,
  Check,
  Heading1,
  Heading2,
  Italic,
  Link as LinkIcon,
  List,
  ListOrdered,
  X,
} from "lucide-react";
import {
  $getSelection,
  $isRangeSelection,
  $setSelection,
  FORMAT_TEXT_COMMAND,
  type RangeSelection,
} from "lexical";
import { useLexicalComposerContext } from "@lexical/react/LexicalComposerContext";
import { $setBlocksType } from "@lexical/selection";
import { $createHeadingNode, HeadingNode } from "@lexical/rich-text";
import { $createParagraphNode } from "lexical";
import {
  INSERT_ORDERED_LIST_COMMAND,
  INSERT_UNORDERED_LIST_COMMAND,
  ListNode,
  $isListNode,
} from "@lexical/list";
import { $isHeadingNode } from "@lexical/rich-text";
import { $findMatchingParent, $getNearestNodeOfType } from "@lexical/utils";
import { TOGGLE_LINK_COMMAND, $isLinkNode } from "@lexical/link";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

interface ActiveState {
  bold: boolean;
  italic: boolean;
  link: boolean;
  h1: boolean;
  h2: boolean;
  ul: boolean;
  ol: boolean;
}

const EMPTY: ActiveState = {
  bold: false,
  italic: false,
  link: false,
  h1: false,
  h2: false,
  ul: false,
  ol: false,
};

export function Toolbar() {
  const [editor] = useLexicalComposerContext();
  const [active, setActive] = useState<ActiveState>(EMPTY);
  // null = closed, "" or string = open with that current value.
  const [linkUrl, setLinkUrl] = useState<string | null>(null);
  const linkInputRef = useRef<HTMLInputElement>(null);
  // Selection captured when the link input opens. Restored on submit so
  // the link wraps the originally-selected text even if the user clicked
  // back into the editor (and moved Lexical's live selection) before
  // hitting Apply.
  const savedSelectionRef = useRef<RangeSelection | null>(null);

  const refresh = useCallback(() => {
    editor.getEditorState().read(() => {
      const sel = $getSelection();
      if (!$isRangeSelection(sel)) {
        setActive(EMPTY);
        return;
      }
      const node = sel.anchor.getNode();
      const block = $findMatchingParent(node, (n) => {
        const parent = n.getParent();
        return parent !== null && parent.getKey() === "root";
      });
      const heading = block && $isHeadingNode(block) ? block.getTag() : null;
      const list = $getNearestNodeOfType<ListNode>(node, ListNode);
      const linkParent = $findMatchingParent(node, (n) => $isLinkNode(n));
      setActive({
        bold: sel.hasFormat("bold"),
        italic: sel.hasFormat("italic"),
        link: linkParent !== null,
        h1: heading === "h1",
        h2: heading === "h2",
        ul: !!list && $isListNode(list) && list.getListType() === "bullet",
        ol: !!list && $isListNode(list) && list.getListType() === "number",
      });
    });
  }, [editor]);

  useEffect(() => {
    // registerUpdateListener fires on every editor state change including
    // selection-only changes, so a separate SELECTION_CHANGE_COMMAND
    // subscription would just double the work per keystroke.
    const unUpdate = editor.registerUpdateListener(() => refresh());
    refresh();
    return unUpdate;
  }, [editor, refresh]);

  const toggleHeading = (tag: "h1" | "h2") => {
    editor.update(() => {
      const sel = $getSelection();
      if (!$isRangeSelection(sel)) return;
      const isActive = tag === "h1" ? active.h1 : active.h2;
      $setBlocksType(sel, () =>
        isActive ? $createParagraphNode() : $createHeadingNode(tag),
      );
    });
  };

  const openLinkInput = () => {
    if (active.link) {
      editor.dispatchCommand(TOGGLE_LINK_COMMAND, null);
      return;
    }
    // Re-clicking the Link button while the input is open closes it,
    // making the button a consistent toggle.
    if (linkUrl !== null) {
      cancelLink();
      return;
    }
    editor.getEditorState().read(() => {
      const sel = $getSelection();
      savedSelectionRef.current = $isRangeSelection(sel) ? sel.clone() : null;
    });
    setLinkUrl("");
    // Defer focus until after the input mounts.
    queueMicrotask(() => linkInputRef.current?.focus());
  };

  const submitLink = () => {
    const url = linkUrl?.trim();
    const saved = savedSelectionRef.current;
    setLinkUrl(null);
    savedSelectionRef.current = null;
    if (!url) {
      editor.focus();
      return;
    }
    // Restore the original selection (the user may have clicked back
    // into the editor and shifted Lexical's live selection while the
    // input was open). Then dispatch — TOGGLE_LINK_COMMAND wraps the
    // current selection.
    if (saved) {
      editor.update(() => $setSelection(saved.clone()));
    }
    editor.dispatchCommand(TOGGLE_LINK_COMMAND, url);
    editor.focus();
  };

  const cancelLink = () => {
    setLinkUrl(null);
    savedSelectionRef.current = null;
    editor.focus();
  };

  const toggleList = (kind: "ul" | "ol") => {
    const isActive = kind === "ul" ? active.ul : active.ol;
    if (isActive) {
      editor.update(() => {
        const sel = $getSelection();
        if (!$isRangeSelection(sel)) return;
        $setBlocksType(sel, () => $createParagraphNode());
      });
      return;
    }
    editor.dispatchCommand(
      kind === "ul" ? INSERT_UNORDERED_LIST_COMMAND : INSERT_ORDERED_LIST_COMMAND,
      undefined,
    );
  };

  return (
    <div
      className={cn(
        "mb-2 border-b border-border pb-2",
        // Tighter row gap when the link input is open so the toolbar +
        // input read as one band instead of two stacked sections.
        linkUrl !== null && "pb-1",
      )}
    >
    <div
      role="toolbar"
      aria-label="Formatting"
      className="flex flex-wrap items-center gap-1"
    >
      <FormatBtn
        label="Bold"
        active={active.bold}
        onClick={() => editor.dispatchCommand(FORMAT_TEXT_COMMAND, "bold")}
      >
        <Bold />
      </FormatBtn>
      <FormatBtn
        label="Italic"
        active={active.italic}
        onClick={() => editor.dispatchCommand(FORMAT_TEXT_COMMAND, "italic")}
      >
        <Italic />
      </FormatBtn>
      <FormatBtn label="Link" active={active.link} onClick={openLinkInput}>
        <LinkIcon />
      </FormatBtn>
      <span className="mx-1 h-5 w-px bg-border" aria-hidden />
      <FormatBtn label="Heading 1" active={active.h1} onClick={() => toggleHeading("h1")}>
        <Heading1 />
      </FormatBtn>
      <FormatBtn label="Heading 2" active={active.h2} onClick={() => toggleHeading("h2")}>
        <Heading2 />
      </FormatBtn>
      <span className="mx-1 h-5 w-px bg-border" aria-hidden />
      <FormatBtn label="Bulleted list" active={active.ul} onClick={() => toggleList("ul")}>
        <List />
      </FormatBtn>
      <FormatBtn label="Numbered list" active={active.ol} onClick={() => toggleList("ol")}>
        <ListOrdered />
      </FormatBtn>
    </div>
    {linkUrl !== null && (
      <div className="mt-1 flex items-center gap-1">
        <Input
          ref={linkInputRef}
          type="url"
          inputMode="url"
          placeholder="https://…"
          value={linkUrl}
          onChange={(e) => setLinkUrl(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              submitLink();
            } else if (e.key === "Escape") {
              e.preventDefault();
              cancelLink();
            }
          }}
          className="h-7 flex-1 text-sm"
          aria-label="Link URL"
        />
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label="Apply link"
          onMouseDown={(e) => e.preventDefault()}
          onClick={submitLink}
          className="h-7 w-7"
        >
          <Check />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label="Cancel link"
          onMouseDown={(e) => e.preventDefault()}
          onClick={cancelLink}
          className="h-7 w-7 text-muted-foreground"
        >
          <X />
        </Button>
      </div>
    )}
    </div>
  );
}

// Need HeadingNode imported so $isHeadingNode resolves at runtime — it's
// re-exported from @lexical/rich-text alongside the node class.
void HeadingNode;

function FormatBtn({
  label,
  active,
  onClick,
  children,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      aria-label={label}
      aria-pressed={active}
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}
      className={cn(
        "h-7 w-7 text-muted-foreground",
        active && "bg-accent text-foreground",
      )}
    >
      {children}
    </Button>
  );
}
